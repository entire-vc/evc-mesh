package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions/dashboards"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// recordingPublisher stands in for Redis (the external boundary the WS hub
// listens on): it keeps every event published, with its channel.
type recordingPublisher struct {
	mu     sync.Mutex
	events []publishedEvent
}

type publishedEvent struct {
	Channel string
	Event   map[string]any
}

func (p *recordingPublisher) Publish(_ context.Context, channel string, event any) error {
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, publishedEvent{Channel: channel, Event: m})
	return nil
}

func (p *recordingPublisher) take() []publishedEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.events
	p.events = nil
	return out
}

type dashboardEnv struct {
	db                          *sqlx.DB
	h                           *DashboardHandler
	svc                         *dashboards.Service
	pub                         *recordingPublisher
	ws                          uuid.UUID
	slug                        string
	owner, admin, alice, bob, v uuid.UUID
}

// newDashboardEnv builds a workspace with an owner, an admin, two members
// (alice, bob) and a viewer, and the real service over Postgres.
func newDashboardEnv(t *testing.T) *dashboardEnv {
	t.Helper()
	db := runtimeHTTPDB(t)
	ws, owner := seedRuntimeWorkspace(t, db)
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1,$2,$3,'owner') ON CONFLICT (workspace_id, user_id) DO NOTHING`, uuid.New(), ws, owner)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), `DELETE FROM workspaces WHERE id = $1`, ws) })
	pub := &recordingPublisher{}
	svc := dashboards.NewService(db, nil, pub)
	e := &dashboardEnv{db: db, ws: ws, owner: owner, pub: pub, svc: svc, h: NewDashboardHandler(svc)}
	require.NoError(t, db.GetContext(ctx, &e.slug, `SELECT slug FROM workspaces WHERE id = $1`, ws))
	e.admin = seedChatMember(t, db, ws, uuid.Nil, "admin", "")
	e.alice = seedChatMember(t, db, ws, uuid.Nil, "member", "")
	e.bob = seedChatMember(t, db, ws, uuid.Nil, "member", "")
	e.v = seedChatMember(t, db, ws, uuid.Nil, "viewer", "")
	return e
}

// call runs one handler as a signed-in person, through the API error handler
// the server installs.
func (e *dashboardEnv) call(t *testing.T, fn echo.HandlerFunc, method, body string, user uuid.UUID, dashboardID string) (status int, out map[string]any) {
	t.Helper()
	ec := echo.New()
	ec.HTTPErrorHandler = NewHTTPErrorHandler(ec.DefaultHTTPErrorHandler)
	req := httptest.NewRequest(method, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := ec.NewContext(req, rec)
	c.Set(mw.ContextKeyUserID, user)
	c.Set(mw.ContextKeyAuthType, mw.AuthTypeUser)
	names, values := []string{"ws_id"}, []string{e.ws.String()}
	if dashboardID != "" {
		names, values = append(names, "dashboard_id"), append(values, dashboardID)
	}
	c.SetParamNames(names...)
	c.SetParamValues(values...)
	if err := fn(c); err != nil {
		ec.HTTPErrorHandler(err, c)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (e *dashboardEnv) rows(t *testing.T, where string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, e.db.Get(&n, `SELECT count(*) FROM ext_dashboards_dashboards WHERE workspace_id = $1 AND `+where, append([]any{e.ws}, args...)...))
	return n
}

func (e *dashboardEnv) list(t *testing.T, user uuid.UUID) []map[string]any {
	t.Helper()
	status, out := e.call(t, e.h.List, http.MethodGet, "", user, "")
	require.Equal(t, http.StatusOK, status, "%v", out)
	var list []map[string]any
	for _, d := range out["dashboards"].([]any) {
		list = append(list, d.(map[string]any))
	}
	return list
}

func (e *dashboardEnv) create(t *testing.T, user uuid.UUID, body string) map[string]any {
	t.Helper()
	status, out := e.call(t, e.h.Create, http.MethodPost, body, user, "")
	require.Equal(t, http.StatusCreated, status, "%v", out)
	return out
}

func version(d map[string]any) int { return int(d["version"].(float64)) }

// AC1: a new person sees one virtual dashboard and nothing is stored; the
// first change (a rename) saves it.
func TestDashboards_VirtualDefaultMaterializesOnFirstChange(t *testing.T) {
	e := newDashboardEnv(t)

	list := e.list(t, e.alice)
	require.Len(t, list, 1)
	require.Equal(t, "default", list[0]["id"])
	require.Equal(t, true, list[0]["virtual"])
	require.Equal(t, true, list[0]["is_default"])
	require.Zero(t, e.rows(t, "true"), "listing must not store anything")

	status, got := e.call(t, e.h.Get, http.MethodGet, "", e.alice, "default")
	require.Equal(t, http.StatusOK, status, "%v", got)
	require.Len(t, got["widgets"], 4, "no theme: the four standard widgets")
	require.Zero(t, e.rows(t, "true"))

	status, got = e.call(t, e.h.Patch, http.MethodPatch, `{"name":"Главная"}`, e.alice, "default")
	require.Equal(t, http.StatusOK, status, "%v", got)
	require.Equal(t, "Главная", got["name"])
	require.Equal(t, false, got["virtual"])
	require.Len(t, got["widgets"], 4, "the default composition is what gets saved")
	require.Equal(t, 1, e.rows(t, "owner_user_id = $2 AND is_default", e.alice))

	list = e.list(t, e.alice)
	require.Len(t, list, 1)
	require.Equal(t, got["id"], list[0]["id"])

	// Other members still see their own virtual one, not alice's.
	list = e.list(t, e.bob)
	require.Len(t, list, 1)
	require.Equal(t, "default", list[0]["id"])
}

// AC2: personal is private, shared is everyone's; editing a shared layout
// needs members_can_edit; a viewer cannot share; deleting a shared dashboard
// is for its author and admins.
func TestDashboards_Permissions(t *testing.T) {
	e := newDashboardEnv(t)

	personal := e.create(t, e.alice, `{"name":"Mine","template_id":"standard"}`)
	status, out := e.call(t, e.h.Get, http.MethodGet, "", e.bob, personal["id"].(string))
	require.Equal(t, http.StatusNotFound, status, "someone else's personal dashboard: %v", out)

	shared := e.create(t, e.alice, `{"name":"Team","shared":true}`)
	sid := shared["id"].(string)
	status, out = e.call(t, e.h.Get, http.MethodGet, "", e.bob, sid)
	require.Equal(t, http.StatusOK, status, "%v", out)
	ids := map[string]bool{}
	for _, d := range e.list(t, e.bob) {
		ids[d["id"].(string)] = true
	}
	require.True(t, ids[sid], "a shared dashboard is in every member's menu")
	require.False(t, ids[personal["id"].(string)])

	layout := func(ver int) string {
		return fmt.Sprintf(`{"version":%d,"widgets":[{"widget_type":"my-tasks","x":0,"y":0,"w":6,"h":4,"settings":{"limit":10}}]}`, ver)
	}
	status, out = e.call(t, e.h.PutLayout, http.MethodPut, layout(version(shared)), e.bob, sid)
	require.Equal(t, http.StatusForbidden, status, "members_can_edit off: %v", out)

	status, out = e.call(t, e.h.Patch, http.MethodPatch, `{"members_can_edit":true}`, e.alice, sid)
	require.Equal(t, http.StatusOK, status, "%v", out)
	status, out = e.call(t, e.h.PutLayout, http.MethodPut, layout(version(out)), e.bob, sid)
	require.Equal(t, http.StatusOK, status, "members_can_edit on: %v", out)
	status, out = e.call(t, e.h.PutLayout, http.MethodPut, layout(version(out)), e.v, sid)
	require.Equal(t, http.StatusForbidden, status, "a viewer does not edit even then: %v", out)

	status, out = e.call(t, e.h.Create, http.MethodPost, `{"name":"Viewer's","shared":true}`, e.v, "")
	require.Equal(t, http.StatusForbidden, status, "a viewer cannot share: %v", out)
	status, _ = e.call(t, e.h.Create, http.MethodPost, `{"name":"Viewer's own"}`, e.v, "")
	require.Equal(t, http.StatusCreated, status, "a viewer keeps personal dashboards")

	status, out = e.call(t, e.h.Delete, http.MethodDelete, "", e.bob, sid)
	require.Equal(t, http.StatusForbidden, status, "a member who is not the author: %v", out)
	status, out = e.call(t, e.h.Delete, http.MethodDelete, "", e.admin, sid)
	require.Equal(t, http.StatusOK, status, "an admin: %v", out)

	another := e.create(t, e.alice, `{"name":"Team 2","shared":true}`)
	status, out = e.call(t, e.h.Delete, http.MethodDelete, "", e.alice, another["id"].(string))
	require.Equal(t, http.StatusOK, status, "the author: %v", out)
}

// AC3: the order must name exactly the caller's menu; a personal dashboard
// cannot be hidden.
func TestDashboards_OrderAndHide(t *testing.T) {
	e := newDashboardEnv(t)
	alicePersonal := e.create(t, e.alice, `{"name":"Alice","placement":"top"}`)["id"].(string)
	shared := e.create(t, e.alice, `{"name":"Team","shared":true}`)["id"].(string)
	bobOwn := e.create(t, e.bob, `{"name":"Bob","placement":"bottom"}`)["id"].(string)
	bobDefault := ""
	for _, d := range e.list(t, e.bob) {
		if d["is_default"] == true {
			bobDefault = d["id"].(string)
		}
	}
	require.NotEmpty(t, bobDefault, "creating a dashboard saved bob's default first")

	other := newDashboardEnv(t)
	foreign := other.create(t, other.alice, `{"name":"Elsewhere","shared":true}`)["id"].(string)

	for name, body := range map[string]string{
		"someone's personal": fmt.Sprintf(`{"top":[%q,%q,%q],"bottom":[%q]}`, bobDefault, shared, alicePersonal, bobOwn),
		"another workspace":  fmt.Sprintf(`{"top":[%q,%q,%q],"bottom":[%q]}`, bobDefault, shared, foreign, bobOwn),
		"missing one":        fmt.Sprintf(`{"top":[%q],"bottom":[%q]}`, bobDefault, bobOwn),
		"repeated":           fmt.Sprintf(`{"top":[%q,%q,%q],"bottom":[%q]}`, bobDefault, shared, shared, bobOwn),
	} {
		status, out := e.call(t, e.h.Order, http.MethodPut, body, e.bob, "")
		require.Equal(t, http.StatusUnprocessableEntity, status, "%s: %v", name, out)
		require.Equal(t, "dashboard_order_mismatch", out["error_code"], name)
	}

	status, out := e.call(t, e.h.Order, http.MethodPut,
		fmt.Sprintf(`{"top":[%q],"bottom":[%q,%q]}`, bobDefault, bobOwn, shared), e.bob, "")
	require.Equal(t, http.StatusOK, status, "%v", out)
	got := []string{}
	for _, d := range out["dashboards"].([]any) {
		got = append(got, d.(map[string]any)["id"].(string))
	}
	require.Equal(t, []string{bobDefault, bobOwn, shared}, got)

	status, out = e.call(t, e.h.SetHidden, http.MethodPut, `{"hidden":true}`, e.bob, bobOwn)
	require.Equal(t, http.StatusUnprocessableEntity, status, "personal: %v", out)
	require.Equal(t, "dashboard_not_hideable", out["error_code"])

	status, out = e.call(t, e.h.SetHidden, http.MethodPut, `{"hidden":true}`, e.bob, shared)
	require.Equal(t, http.StatusOK, status, "%v", out)
	for _, d := range e.list(t, e.bob) {
		require.NotEqual(t, shared, d["id"], "hidden from bob's menu")
	}
	ids := map[string]bool{}
	for _, d := range e.list(t, e.alice) {
		ids[d["id"].(string)] = true
	}
	require.True(t, ids[shared], "still in alice's menu")
}

// AC4: two writes from the same version: the second is 409 stale_layout with
// the dashboard as the first write left it.
func TestDashboards_StaleLayout(t *testing.T) {
	e := newDashboardEnv(t)
	d := e.create(t, e.alice, `{"name":"Grid","template_id":"standard"}`)
	id, ver := d["id"].(string), version(d)

	first := fmt.Sprintf(`{"version":%d,"widgets":[{"widget_type":"mentions","x":0,"y":0,"w":12,"h":4}]}`, ver)
	second := fmt.Sprintf(`{"version":%d,"widgets":[{"widget_type":"event-feed","x":0,"y":0,"w":12,"h":6}]}`, ver)
	status, out := e.call(t, e.h.PutLayout, http.MethodPut, first, e.alice, id)
	require.Equal(t, http.StatusOK, status, "%v", out)
	require.Equal(t, ver+1, version(out))

	status, out = e.call(t, e.h.PutLayout, http.MethodPut, second, e.alice, id)
	require.Equal(t, http.StatusConflict, status, "%v", out)
	require.Equal(t, "stale_layout", out["error_code"])
	cur := out["context"].(map[string]any)["dashboard"].(map[string]any)
	require.Equal(t, ver+1, version(cur))
	widgets := cur["widgets"].([]any)
	require.Len(t, widgets, 1)
	require.Equal(t, "mentions", widgets[0].(map[string]any)["widget_type"], "the current body is the first write")

	// The virtual default has version 0; once saved, 0 is stale.
	status, out = e.call(t, e.h.PutLayout, http.MethodPut, `{"version":0,"widgets":[]}`, e.bob, "default")
	require.Equal(t, http.StatusOK, status, "%v", out)
	require.Equal(t, 1, version(out))
	status, out = e.call(t, e.h.PutLayout, http.MethodPut, `{"version":0,"widgets":[]}`, e.bob, "default")
	require.Equal(t, http.StatusConflict, status, "%v", out)

	// An invalid layout is refused with the offending fields; an unknown type
	// is kept.
	status, out = e.call(t, e.h.PutLayout, http.MethodPut, fmt.Sprintf(`{"version":%d,"widgets":[
		{"widget_type":"mentions","x":0,"y":0,"w":6,"h":4},{"widget_type":"my-tasks","x":3,"y":0,"w":6,"h":4}]}`, ver+1), e.alice, id)
	require.Equal(t, http.StatusUnprocessableEntity, status, "%v", out)
	require.Equal(t, "layout_invalid", out["error_code"])
	status, out = e.call(t, e.h.PutLayout, http.MethodPut, fmt.Sprintf(`{"version":%d,"widgets":[
		{"widget_type":"ext.someone.chart","x":0,"y":0,"w":4,"h":3,"settings":{"a":1}}]}`, ver+1), e.alice, id)
	require.Equal(t, http.StatusOK, status, "%v", out)
}

// AC5: leaving the workspace removes the person's own dashboards and keeps
// their shared ones, with no author.
func TestDashboards_MemberRemovalCleanup(t *testing.T) {
	e := newDashboardEnv(t)
	personal := e.create(t, e.bob, `{"name":"Bob's"}`)["id"].(string)
	shared := e.create(t, e.bob, `{"name":"Bob's team","shared":true}`)["id"].(string)
	_, out := e.call(t, e.h.SetHidden, http.MethodPut, `{"hidden":true}`, e.alice, shared)
	require.NotNil(t, out)
	require.Equal(t, 3, e.rows(t, "author_user_id = $2", e.bob), "default, personal, shared")

	_, err := e.db.Exec(`DELETE FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`, e.ws, e.bob)
	require.NoError(t, err)

	require.Zero(t, e.rows(t, "owner_user_id = $2", e.bob))
	require.Zero(t, e.rows(t, "id = $2", personal))
	require.Equal(t, 1, e.rows(t, "id = $2 AND scope = 'workspace' AND author_user_id IS NULL", shared))
	var placements int
	require.NoError(t, e.db.Get(&placements, `SELECT count(*) FROM ext_dashboards_placements WHERE user_id = $1`, e.bob))
	require.Zero(t, placements)

	// The orphan stays manageable by an admin.
	status, out := e.call(t, e.h.Patch, http.MethodPatch, `{"name":"Team"}`, e.admin, shared)
	require.Equal(t, http.StatusOK, status, "%v", out)
	status, out = e.call(t, e.h.Patch, http.MethodPatch, `{"shared":false}`, e.admin, shared)
	require.Equal(t, http.StatusUnprocessableEntity, status, "no author to hand it back to: %v", out)
}

// Removing a member through the ordinary RemoveMember path clears the author
// of their shared dashboards in the database (the foreign key), so each of
// them announces dashboard.updated {id, version} with its stored version.
// Their personal dashboard and other members' shared ones announce nothing.
// Control: the same removal without the hook registered publishes nothing,
// which is what this test catches if the wiring goes.
func TestDashboards_MemberRemovalPublishesSharedDashboards(t *testing.T) {
	e := newDashboardEnv(t)
	ctx := context.Background()
	members := func(hooked bool) service.WorkspaceMemberService {
		m := service.NewWorkspaceMemberService(postgres.NewWorkspaceMemberRepo(e.db), nil,
			postgres.NewProjectMemberRepo(e.db), postgres.NewActivityLogRepo(e.db), nil)
		if hooked {
			m.(interface {
				AddMemberRemovalHook(service.MemberRemovalHook)
			}).AddMemberRemovalHook(e.svc)
		}
		return m
	}

	e.create(t, e.bob, `{"name":"Bob's"}`)
	first := e.create(t, e.bob, `{"name":"Bob's team","shared":true}`)
	second := e.create(t, e.bob, `{"name":"Bob's board","shared":true}`)
	status, out := e.call(t, e.h.PutLayout, http.MethodPut,
		fmt.Sprintf(`{"version":%d,"widgets":[{"widget_type":"event-feed","x":0,"y":0,"w":12,"h":6}]}`, version(second)), e.bob, second["id"].(string))
	require.Equal(t, http.StatusOK, status, "%v", out)
	e.create(t, e.alice, `{"name":"Alice's team","shared":true}`)
	carol := seedChatMember(t, e.db, e.ws, uuid.Nil, "member", "")
	e.create(t, carol, `{"name":"Carol's team","shared":true}`)
	e.pub.take()

	// Control: without the hook the same path removes Carol and says nothing
	// about her dashboard, though its author is cleared.
	require.NoError(t, members(false).RemoveMember(ctx, e.ws, carol))
	require.Equal(t, 1, e.rows(t, "name = $2 AND author_user_id IS NULL", "Carol's team"))
	require.Empty(t, e.pub.take(), "no hook, no dashboard event")

	require.NoError(t, members(true).RemoveMember(ctx, e.ws, e.bob))
	require.Equal(t, 2, e.rows(t, "author_user_id IS NULL AND scope = 'workspace' AND name LIKE $2", "Bob's %"))
	require.Zero(t, e.rows(t, "name = $2", "Bob's"), "the personal dashboard goes with him")

	got := map[string]float64{}
	for _, ev := range e.pub.take() {
		require.Equal(t, "ws:"+e.slug, ev.Channel)
		require.Equal(t, dashboards.EventUpdated, ev.Event["event_type"])
		require.Nil(t, ev.Event["deleted"])
		got[ev.Event["id"].(string)] = ev.Event["version"].(float64)
	}
	require.Equal(t, map[string]float64{
		first["id"].(string):  float64(version(first)),
		second["id"].(string): float64(version(second) + 1),
	}, got, "one event per shared dashboard of the departed author, at its stored version")
}

// AC6 (service side): every change of a shared dashboard publishes
// dashboard.updated {id, version} on ws:<slug>; personal changes publish
// nothing.
func TestDashboards_SharedChangesPublishEvent(t *testing.T) {
	e := newDashboardEnv(t)
	e.create(t, e.alice, `{"name":"Mine"}`)
	require.Empty(t, e.pub.take(), "personal dashboards are nobody else's business")

	d := e.create(t, e.alice, `{"name":"Team","shared":true}`)
	id := d["id"].(string)
	evs := e.pub.take()
	require.Len(t, evs, 1)
	require.Equal(t, "ws:"+e.slug, evs[0].Channel)
	require.Equal(t, dashboards.EventUpdated, evs[0].Event["event_type"])
	require.Equal(t, id, evs[0].Event["id"])
	require.EqualValues(t, version(d), evs[0].Event["version"])

	status, out := e.call(t, e.h.PutLayout, http.MethodPut,
		fmt.Sprintf(`{"version":%d,"widgets":[{"widget_type":"event-feed","x":0,"y":0,"w":12,"h":6}]}`, version(d)), e.alice, id)
	require.Equal(t, http.StatusOK, status, "%v", out)
	evs = e.pub.take()
	require.Len(t, evs, 1)
	require.EqualValues(t, version(d)+1, evs[0].Event["version"])

	status, out = e.call(t, e.h.Delete, http.MethodDelete, "", e.alice, id)
	require.Equal(t, http.StatusOK, status, "%v", out)
	evs = e.pub.take()
	require.Len(t, evs, 1)
	require.Equal(t, true, evs[0].Event["deleted"])
}

// An admin who unshares another member's dashboard loses read access to it
// (204), yet the event still names the row and its stored version, so other
// tabs drop it or refetch at the right version.
func TestDashboards_AdminUnsharePublishesRowVersion(t *testing.T) {
	e := newDashboardEnv(t)
	d := e.create(t, e.alice, `{"name":"Team","shared":true}`)
	id := d["id"].(string)
	status, out := e.call(t, e.h.PutLayout, http.MethodPut,
		fmt.Sprintf(`{"version":%d,"widgets":[{"widget_type":"event-feed","x":0,"y":0,"w":12,"h":6}]}`, version(d)), e.alice, id)
	require.Equal(t, http.StatusOK, status, "%v", out)
	e.pub.take()

	status, out = e.call(t, e.h.Patch, http.MethodPatch, `{"shared":false}`, e.admin, id)
	require.Equal(t, http.StatusNoContent, status, "%v", out)

	var stored int
	require.NoError(t, e.db.Get(&stored, `SELECT version FROM ext_dashboards_dashboards WHERE id = $1 AND scope = 'personal' AND owner_user_id = $2`, id, e.alice))
	require.EqualValues(t, version(d)+1, stored)
	evs := e.pub.take()
	require.Len(t, evs, 1)
	require.Equal(t, "ws:"+e.slug, evs[0].Channel)
	require.Equal(t, id, evs[0].Event["id"])
	require.EqualValues(t, stored, evs[0].Event["version"], "the row's version, not 0")
	require.Nil(t, evs[0].Event["deleted"])
}

// The last own dashboard cannot be deleted, neither can the virtual one.
func TestDashboards_LastDashboard(t *testing.T) {
	e := newDashboardEnv(t)
	status, out := e.call(t, e.h.Delete, http.MethodDelete, "", e.alice, "default")
	require.Equal(t, http.StatusConflict, status, "%v", out)
	require.Equal(t, "last_dashboard", out["error_code"])

	_, out = e.call(t, e.h.Patch, http.MethodPatch, `{"name":"Home"}`, e.alice, "default")
	status, out = e.call(t, e.h.Delete, http.MethodDelete, "", e.alice, out["id"].(string))
	require.Equal(t, http.StatusConflict, status, "%v", out)
	require.Equal(t, "last_dashboard", out["error_code"])

	status, out = e.call(t, e.h.Duplicate, http.MethodPost, "", e.alice, "default")
	require.Equal(t, http.StatusCreated, status, "%v", out)
	require.Equal(t, "Home (copy)", out["name"])
	require.Len(t, out["widgets"], 4)
}

// The menu carries what the frontend needs without opening each dashboard:
// the distinct widget types (the mentions counter looks for "mentions") and
// the author's name, null once the author left.
func TestDashboards_ListCarriesWidgetTypesAndAuthorName(t *testing.T) {
	e := newDashboardEnv(t)
	_, err := e.db.Exec(`UPDATE users SET display_name = 'Alice A' WHERE id = $1`, e.alice)
	require.NoError(t, err)

	list := e.list(t, e.bob)
	require.Len(t, list, 1)
	require.Nil(t, list[0]["author_name"], "the virtual dashboard has no author")
	status, def := e.call(t, e.h.Get, http.MethodGet, "", e.bob, "default")
	require.Equal(t, http.StatusOK, status, "%v", def)
	require.NotEmpty(t, list[0]["widget_types"])
	require.Equal(t, def["widget_types"], list[0]["widget_types"], "virtual: the types of the default composition")

	shared := e.create(t, e.alice, `{"name":"Team","shared":true,"template_id":"empty"}`)
	sid := shared["id"].(string)
	require.Equal(t, []any{}, shared["widget_types"], "an empty dashboard lists [] rather than null")
	status, out := e.call(t, e.h.PutLayout, http.MethodPut, fmt.Sprintf(`{"version":%d,"widgets":[
		{"widget_type":"my-tasks","x":0,"y":0,"w":6,"h":4,"settings":{}},
		{"widget_type":"mentions","x":6,"y":0,"w":6,"h":4,"settings":{}},
		{"widget_type":"my-tasks","x":0,"y":4,"w":6,"h":4,"settings":{}}]}`, version(shared)), e.alice, sid)
	require.Equal(t, http.StatusOK, status, "%v", out)
	require.Equal(t, []any{"mentions", "my-tasks"}, out["widget_types"], "detail after a layout write")

	find := func(user uuid.UUID) map[string]any {
		for _, d := range e.list(t, user) {
			if d["id"] == sid {
				return d
			}
		}
		t.Fatalf("shared dashboard %s not in the menu", sid)
		return nil
	}
	got := find(e.bob)
	require.Equal(t, []any{"mentions", "my-tasks"}, got["widget_types"])
	require.Equal(t, "Alice A", got["author_name"])

	_, err = e.db.Exec(`DELETE FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`, e.ws, e.alice)
	require.NoError(t, err)
	require.Nil(t, find(e.bob)["author_name"], "the author left")
}

// The icon follows the shared §5.11 format end to end, including a long ZWJ
// emoji the column must hold.
func TestDashboards_IconFormat(t *testing.T) {
	e := newDashboardEnv(t)
	for _, icon := range []string{"icon:rocket", "🚀", "👨‍👩‍👧‍👦", ""} {
		d := e.create(t, e.alice, fmt.Sprintf(`{"name":"I","icon":%q}`, icon))
		require.Equal(t, icon, d["icon"])
	}
	for _, icon := range []string{"icon:Rocket!", "<svg>", "🚀🚀", "rocket"} {
		status, out := e.call(t, e.h.Create, http.MethodPost, fmt.Sprintf(`{"name":"I","icon":%q}`, icon), e.alice, "")
		require.Equal(t, http.StatusUnprocessableEntity, status, "%q: %v", icon, out)
	}
	id := e.list(t, e.alice)[0]["id"].(string)
	status, out := e.call(t, e.h.Patch, http.MethodPatch, `{"icon":"icon:Rocket!"}`, e.alice, id)
	require.Equal(t, http.StatusUnprocessableEntity, status, "PATCH validates too: %v", out)
}

// PRD §6.2 v3 wire forms beside the agreed ones: scope on create, the
// /sharing patch with default_placement, /copy with a placement, and the
// hidden list in the order call.
func TestDashboards_PRDWireContract(t *testing.T) {
	e := newDashboardEnv(t)

	// The virtual default speaks the same DTO: personal, top, in the list and
	// on GET, with nothing stored.
	virtual := e.list(t, e.admin)
	require.Len(t, virtual, 1)
	require.Equal(t, true, virtual[0]["virtual"])
	require.Equal(t, "personal", virtual[0]["scope"])
	require.Equal(t, "top", virtual[0]["default_placement"])
	status, out := e.call(t, e.h.Get, http.MethodGet, "", e.admin, "default")
	require.Equal(t, http.StatusOK, status, "%v", out)
	require.Equal(t, true, out["virtual"])
	require.Equal(t, "personal", out["scope"])
	require.Equal(t, "top", out["default_placement"])
	require.Equal(t, 0, e.rows(t, "owner_user_id = $2", e.admin))

	// POST scope:"workspace" shares; the DTO speaks both shared and scope.
	team := e.create(t, e.alice, `{"name":"Team","scope":"workspace"}`)
	require.Equal(t, true, team["shared"])
	require.Equal(t, "workspace", team["scope"])
	require.Equal(t, "bottom", team["default_placement"])
	teamID := team["id"].(string)
	require.Equal(t, 1, e.rows(t, "id = $2 AND scope = 'workspace' AND owner_user_id IS NULL", teamID))
	seen := false
	for _, d := range e.list(t, e.bob) {
		if d["id"] == teamID {
			seen = true
			require.Equal(t, "workspace", d["scope"])
		}
	}
	require.True(t, seen, "a workspace-scoped dashboard is in bob's menu")

	mine := e.create(t, e.alice, `{"name":"Mine","scope":"personal"}`)
	require.Equal(t, "personal", mine["scope"])
	require.Equal(t, false, mine["shared"])

	for name, body := range map[string]string{
		"unknown scope":              `{"name":"X","scope":"team"}`,
		"personal but shared true":   `{"name":"X","scope":"personal","shared":true}`,
		"workspace but shared false": `{"name":"X","scope":"workspace","shared":false}`,
	} {
		status, out = e.call(t, e.h.Create, http.MethodPost, body, e.alice, "")
		require.Equal(t, http.StatusUnprocessableEntity, status, "%s: %v", name, out)
		require.Equal(t, 0, e.rows(t, "name = 'X'"), name)
	}
	// Agreeing forms pass: an explicit shared next to the matching scope.
	both := e.create(t, e.alice, `{"name":"Both","scope":"workspace","shared":true}`)
	require.Equal(t, "workspace", both["scope"])
	status, out = e.call(t, e.h.Delete, http.MethodDelete, "", e.alice, both["id"].(string))
	require.Equal(t, http.StatusOK, status, "%v", out)
	notShared := e.create(t, e.alice, `{"name":"Plain","scope":"personal","shared":false}`)
	require.Equal(t, "personal", notShared["scope"])
	status, out = e.call(t, e.h.Create, http.MethodPost, `{"name":"X","scope":"workspace"}`, e.v, "")
	require.Equal(t, http.StatusForbidden, status, "viewer sharing via scope: %v", out)

	// PATCH /sharing: manage right only; default_placement is stored.
	for name, user := range map[string]uuid.UUID{"member": e.bob, "viewer": e.v} {
		status, out = e.call(t, e.h.Sharing, http.MethodPatch, `{"default_placement":"top"}`, user, teamID)
		require.Equal(t, http.StatusForbidden, status, "%s: %v", name, out)
	}
	status, out = e.call(t, e.h.Sharing, http.MethodPatch, `{"default_placement":"left"}`, e.alice, teamID)
	require.Equal(t, http.StatusUnprocessableEntity, status, "%v", out)
	status, out = e.call(t, e.h.Sharing, http.MethodPatch,
		`{"scope":"workspace","members_can_edit":true,"default_placement":"top"}`, e.alice, teamID)
	require.Equal(t, http.StatusOK, status, "%v", out)
	require.Equal(t, "top", out["default_placement"])
	require.Equal(t, true, out["members_can_edit"])
	require.Equal(t, 1, e.rows(t, "id = $2 AND default_placement = 'top' AND members_can_edit", teamID))
	for _, d := range e.list(t, e.bob) {
		if d["id"] == teamID {
			require.Equal(t, "top", d["placement"], "bob never placed it: the default placement applies")
		}
	}

	// Name and icon follow the edit right: a member may rename once
	// members_can_edit is on, but not touch the sharing through PATCH.
	status, out = e.call(t, e.h.Patch, http.MethodPatch, `{"name":"Ours"}`, e.bob, teamID)
	require.Equal(t, http.StatusOK, status, "%v", out)
	require.Equal(t, "Ours", out["name"])
	status, out = e.call(t, e.h.Patch, http.MethodPatch, `{"scope":"personal"}`, e.bob, teamID)
	require.Equal(t, http.StatusForbidden, status, "%v", out)

	// /copy into a chosen group makes a personal copy there.
	status, out = e.call(t, e.h.Duplicate, http.MethodPost, `{"placement":"top"}`, e.bob, teamID)
	require.Equal(t, http.StatusCreated, status, "%v", out)
	require.Equal(t, "personal", out["scope"])
	require.Equal(t, "top", out["placement"])
	copyID := out["id"].(string)
	status, out = e.call(t, e.h.Duplicate, http.MethodPost, `{"placement":"side"}`, e.bob, teamID)
	require.Equal(t, http.StatusUnprocessableEntity, status, "%v", out)

	// PUT order {top,bottom,hidden}: only a shared dashboard may be hidden.
	bobDefault := ""
	for _, d := range e.list(t, e.bob) {
		if d["is_default"] == true {
			bobDefault = d["id"].(string)
		}
	}
	require.NotEmpty(t, bobDefault)
	status, out = e.call(t, e.h.Order, http.MethodPut,
		fmt.Sprintf(`{"top":[%q],"bottom":[%q],"hidden":[%q]}`, teamID, bobDefault, copyID), e.bob, "")
	require.Equal(t, http.StatusUnprocessableEntity, status, "%v", out)
	require.Equal(t, "dashboard_not_hideable", out["error_code"])
	status, out = e.call(t, e.h.Order, http.MethodPut,
		fmt.Sprintf(`{"top":[%q],"bottom":[%q],"hidden":[%q]}`, copyID, bobDefault, teamID), e.bob, "")
	require.Equal(t, http.StatusOK, status, "%v", out)
	got := []string{}
	for _, d := range e.list(t, e.bob) {
		got = append(got, d["id"].(string))
	}
	require.Equal(t, []string{copyID, bobDefault}, got, "the hidden shared one left bob's menu")
	// Unhiding through the same call puts it back where it is listed.
	status, out = e.call(t, e.h.Order, http.MethodPut,
		fmt.Sprintf(`{"top":[%q,%q],"bottom":[%q],"hidden":[]}`, teamID, copyID, bobDefault), e.bob, "")
	require.Equal(t, http.StatusOK, status, "%v", out)
	got = got[:0]
	for _, d := range e.list(t, e.bob) {
		got = append(got, d["id"].(string))
	}
	require.Equal(t, []string{teamID, copyID, bobDefault}, got)
}
