package dashboards

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
)

// Service-level scenarios over Postgres. The HTTP acceptance tests live in
// internal/handler; these drive the service directly so every branch of the
// model is exercised in its own package.

type pubRecord struct {
	channel string
	event   map[string]any
}

type fakePublisher struct {
	mu   sync.Mutex
	got  []pubRecord
	fail bool
}

func (p *fakePublisher) Publish(_ context.Context, channel string, event any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.got = append(p.got, pubRecord{channel: channel, event: event.(map[string]any)})
	if p.fail {
		return errors.New("redis down")
	}
	return nil
}

func (p *fakePublisher) take() []pubRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.got
	p.got = nil
	return out
}

type fakeThemes struct {
	theme Theme
	err   error
}

func (f fakeThemes) DashboardTheme(context.Context, uuid.UUID) (Theme, error) { return f.theme, f.err }

type svcEnv struct {
	db                               *sqlx.DB
	s                                *Service
	pub                              *fakePublisher
	ws                               uuid.UUID
	slug                             string
	owner, admin, alice, bob, viewer Actor
}

func serviceDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres, skipping: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedUser(t *testing.T, db *sqlx.DB, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	short := id.String()[:8]
	_, err := db.Exec(`INSERT INTO users (id, email, username, password_hash, display_name) VALUES ($1,$2,$3,'x',$4)`,
		id, "dash-"+short+"@example.com", "dash-"+short, name)
	require.NoError(t, err)
	return id
}

func (e *svcEnv) member(t *testing.T, role, name string) Actor {
	t.Helper()
	id := seedUser(t, e.db, name)
	_, err := e.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1,$2,$3,$4)`, uuid.New(), e.ws, id, role)
	require.NoError(t, err)
	return Actor{UserID: id, WorkspaceID: e.ws, Role: role}
}

func newSvcEnv(t *testing.T, themes ThemeSource) *svcEnv {
	t.Helper()
	db := serviceDB(t)
	e := &svcEnv{db: db, pub: &fakePublisher{}, ws: uuid.New()}
	ownerID := seedUser(t, db, "Owner")
	e.slug = "dash-ws-" + e.ws.String()[:8]
	_, err := db.Exec(`INSERT INTO workspaces (id, name, slug, owner_id) VALUES ($1,$2,$3,$4)`, e.ws, "dash", e.slug, ownerID)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id = $1`, e.ws) })
	_, err = db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1,$2,$3,'owner')`, uuid.New(), e.ws, ownerID)
	require.NoError(t, err)
	e.owner = Actor{UserID: ownerID, WorkspaceID: e.ws, Role: roleOwner}
	e.s = NewService(db, themes, e.pub)
	e.admin = e.member(t, roleAdmin, "Admin")
	e.alice = e.member(t, "member", "Alice")
	e.bob = e.member(t, "member", "Bob")
	e.viewer = e.member(t, roleViewer, "Viewer")
	return e
}

func requireCode(t *testing.T, err error, status int, code string) *Error {
	t.Helper()
	var e *Error
	require.True(t, errors.As(err, &e), "want *Error %d %s, got %v", status, code, err)
	require.Equal(t, status, e.Status, "%v", e)
	if code != "" {
		require.Equal(t, code, e.Code, "%v", e)
	}
	return e
}

func ptr[T any](v T) *T { return &v }

func onlyMyTasks(x, y int) []Widget {
	return []Widget{{WidgetType: "my-tasks", X: x, Y: y, W: 6, H: 4}}
}

func TestServiceDB_ResolveActor(t *testing.T) {
	e := newSvcEnv(t, nil)
	ctx := context.Background()
	a, err := e.s.ResolveActor(ctx, e.ws, e.alice.UserID)
	require.NoError(t, err)
	require.Equal(t, "member", a.Role)
	_, err = e.db.Exec(`DELETE FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`, e.ws, e.owner.UserID)
	require.NoError(t, err)
	a, err = e.s.ResolveActor(ctx, e.ws, e.owner.UserID)
	require.NoError(t, err)
	require.Equal(t, roleOwner, a.Role, "the workspace owner without a membership row")
	_, err = e.s.ResolveActor(ctx, e.ws, uuid.New())
	requireCode(t, err, 403, "forbidden")

	// An owner with no membership row cannot store dashboards: a readable 409.
	_, err = e.s.Create(ctx, a, CreateInput{Name: "Mine"})
	requireCode(t, err, 409, "conflict")
}

func TestServiceDB_DefaultDashboardLifecycle(t *testing.T) {
	e := newSvcEnv(t, nil)
	ctx := context.Background()
	a := e.alice

	d, err := e.s.Get(ctx, a, DefaultID)
	require.NoError(t, err)
	require.True(t, d.Virtual)
	require.Equal(t, distinctTypes(d.Widgets), d.WidgetTypes)
	d, err = e.s.Reset(ctx, a, DefaultID)
	require.NoError(t, err)
	require.True(t, d.Virtual, "resetting the virtual one changes nothing")

	_, err = e.s.PutLayout(ctx, a, DefaultID, LayoutInput{Version: ptr(1), Widgets: onlyMyTasks(0, 0)})
	stale := requireCode(t, err, 409, "stale_layout")
	require.True(t, stale.Current.Virtual)

	_, err = e.s.PutLayout(ctx, a, DefaultID, LayoutInput{Widgets: onlyMyTasks(0, 0)})
	requireCode(t, err, 422, "validation_failed")
	_, err = e.s.PutLayout(ctx, a, DefaultID, LayoutInput{Version: ptr(0), Widgets: []Widget{{WidgetType: "my-tasks", W: 1, H: 1}}})
	requireCode(t, err, 422, "layout_invalid")

	d, err = e.s.PutLayout(ctx, a, DefaultID, LayoutInput{Version: ptr(0), Widgets: onlyMyTasks(0, 0)})
	require.NoError(t, err)
	require.False(t, d.Virtual)
	require.True(t, d.IsDefault)
	require.Equal(t, []string{"my-tasks"}, d.WidgetTypes)

	got, err := e.s.Get(ctx, a, DefaultID)
	require.NoError(t, err)
	require.Equal(t, d.ID, got.ID, "default now names the saved one")

	reset, err := e.s.Reset(ctx, a, d.ID)
	require.NoError(t, err)
	require.Equal(t, d.Version+1, reset.Version)
	require.Equal(t, len(e.s.virtual(ctx, a).Widgets), len(reset.Widgets))

	err = e.s.Delete(ctx, a, d.ID)
	requireCode(t, err, 409, "last_dashboard")
	err = e.s.Delete(ctx, e.bob, DefaultID)
	requireCode(t, err, 409, "last_dashboard")

	other, err := e.s.Create(ctx, a, CreateInput{Name: "Other", TemplateID: ptr("standard")})
	require.NoError(t, err)
	_, err = e.s.Reset(ctx, a, other.ID)
	requireCode(t, err, 422, "not_default_dashboard")
	require.NoError(t, e.s.Delete(ctx, a, d.ID), "no longer the last one")
	_, err = e.s.Get(ctx, a, DefaultID)
	requireCode(t, err, 404, "not_found")

	_, err = e.s.Get(ctx, a, "not-a-uuid")
	requireCode(t, err, 404, "not_found")
	_, err = e.s.Get(ctx, a, uuid.NewString())
	requireCode(t, err, 404, "not_found")
}

func TestServiceDB_CreateValidation(t *testing.T) {
	e := newSvcEnv(t, nil)
	ctx := context.Background()
	for name, tc := range map[string]struct {
		a    Actor
		in   CreateInput
		code int
	}{
		"empty name":     {e.alice, CreateInput{Name: "  "}, 422},
		"long name":      {e.alice, CreateInput{Name: strings.Repeat("я", MaxNameRunes+1)}, 422},
		"bad icon":       {e.alice, CreateInput{Name: "x", Icon: "icon:Bad!"}, 422},
		"bad placement":  {e.alice, CreateInput{Name: "x", Placement: "left"}, 422},
		"edit, personal": {e.alice, CreateInput{Name: "x", MembersCanEdit: true}, 422},
		"unknown tpl":    {e.alice, CreateInput{Name: "x", TemplateID: ptr("nope")}, 422},
		"viewer shares":  {e.viewer, CreateInput{Name: "x", Shared: ptr(true)}, 403},
	} {
		_, err := e.s.Create(ctx, tc.a, tc.in)
		requireCode(t, err, tc.code, "")
		_ = name
	}

	for i := 0; i < MaxDashboardsPerUser-1; i++ {
		_, err := e.s.Create(ctx, e.bob, CreateInput{Name: fmt.Sprintf("B%d", i)})
		require.NoError(t, err, i)
	}
	_, err := e.s.Create(ctx, e.bob, CreateInput{Name: "one too many"})
	requireCode(t, err, 422, "dashboard_limit")
	list, err := e.s.List(ctx, e.bob, false)
	require.NoError(t, err)
	require.Len(t, list, MaxDashboardsPerUser, "the saved default and the created ones")
	_, err = e.s.Duplicate(ctx, e.bob, list[0].ID, nil, CopySuffixEN)
	requireCode(t, err, 422, "dashboard_limit")
}

func TestServiceDB_PatchAndSharing(t *testing.T) {
	e := newSvcEnv(t, nil)
	ctx := context.Background()

	// Patching "default" saves it first.
	d, err := e.s.Patch(ctx, e.alice, DefaultID, PatchInput{Name: ptr("Home"), Icon: ptr("🚀")})
	require.NoError(t, err)
	require.Equal(t, "Home", d.Name)
	require.Equal(t, "🚀", d.Icon)
	require.Empty(t, e.pub.take(), "a personal change publishes nothing")

	for _, in := range []PatchInput{{Name: ptr("")}, {Icon: ptr("<svg>")}, {MembersCanEdit: ptr(true)}} {
		_, err = e.s.Patch(ctx, e.alice, d.ID, in)
		requireCode(t, err, 422, "validation_failed")
	}

	mine, err := e.s.Create(ctx, e.viewer, CreateInput{Name: "Viewer's"})
	require.NoError(t, err)
	_, err = e.s.Patch(ctx, e.viewer, mine.ID, PatchInput{Shared: ptr(true)})
	requireCode(t, err, 403, "forbidden")

	shared, err := e.s.Patch(ctx, e.alice, d.ID, PatchInput{Shared: ptr(true), MembersCanEdit: ptr(true)})
	require.NoError(t, err)
	require.True(t, shared.Shared)
	require.True(t, shared.MembersCanEdit)
	require.Equal(t, "Alice", *shared.AuthorName)
	ev := e.pub.take()
	require.Len(t, ev, 1)
	require.Equal(t, "ws:"+e.slug, ev[0].channel)
	require.Equal(t, EventUpdated, ev[0].event["event_type"])

	// §6.2: name and icon follow the edit right, the sharing the manage right.
	renamed, err := e.s.Patch(ctx, e.bob, shared.ID, PatchInput{Name: ptr("Ours")})
	require.NoError(t, err)
	require.Equal(t, "Ours", renamed.Name)
	require.Len(t, e.pub.take(), 1)
	_, err = e.s.Patch(ctx, e.bob, shared.ID, PatchInput{Name: ptr("Mine now"), Shared: ptr(false)})
	requireCode(t, err, 403, "forbidden")
	_, err = e.s.Sharing(ctx, e.bob, shared.ID, SharingInput{DefaultPlacement: ptr("top")})
	requireCode(t, err, 403, "forbidden")
	_, err = e.s.Patch(ctx, e.viewer, shared.ID, PatchInput{Name: ptr("Viewer's")})
	requireCode(t, err, 403, "forbidden")
	shared = renamed

	// A member who may edit the layout does so; the event follows.
	lay, err := e.s.PutLayout(ctx, e.bob, shared.ID, LayoutInput{Version: ptr(shared.Version), Widgets: onlyMyTasks(0, 0)})
	require.NoError(t, err)
	require.Len(t, e.pub.take(), 1)
	_, err = e.s.PutLayout(ctx, e.viewer, shared.ID, LayoutInput{Version: ptr(lay.Version), Widgets: onlyMyTasks(0, 0)})
	requireCode(t, err, 403, "forbidden")
	_, err = e.s.Reset(ctx, e.viewer, shared.ID)
	requireCode(t, err, 403, "forbidden")

	// Widget ids survive a layout write that keeps them.
	keep := lay.Widgets
	again, err := e.s.PutLayout(ctx, e.alice, shared.ID, LayoutInput{Version: ptr(lay.Version), Widgets: keep})
	require.NoError(t, err)
	require.Equal(t, keep[0].ID, again.Widgets[0].ID)
	_, err = e.s.Reset(ctx, e.alice, shared.ID)
	require.NoError(t, err, "a shared default is still resettable by whoever edits it")
	require.Len(t, e.pub.take(), 2)

	// An admin unsharing hands it back to its author: no longer the admin's to read.
	out, err := e.s.Patch(ctx, e.admin, shared.ID, PatchInput{Shared: ptr(false)})
	require.NoError(t, err)
	require.Nil(t, out)
	ev = e.pub.take()
	require.Len(t, ev, 1)
	back, err := e.s.Get(ctx, e.alice, shared.ID)
	require.NoError(t, err)
	require.Equal(t, shared.ID, ev[0].event["id"])
	require.Equal(t, back.Version, ev[0].event["version"], "the row's version, not 0")
	require.NotZero(t, back.Version)
	require.False(t, back.Shared)
	require.False(t, back.MembersCanEdit)

	// A failing publisher does not fail the change.
	e.pub.fail = true
	_, err = e.s.Patch(ctx, e.alice, shared.ID, PatchInput{Shared: ptr(true)})
	require.NoError(t, err)
	e.pub.fail = false
}

func TestServiceDB_DuplicateOrderHide(t *testing.T) {
	e := newSvcEnv(t, nil)
	ctx := context.Background()

	shared, err := e.s.Create(ctx, e.alice, CreateInput{Name: strings.Repeat("ш", MaxNameRunes), Icon: "icon:rocket", Shared: ptr(true), Placement: PlacementTop, TemplateID: ptr("standard")})
	require.NoError(t, err)

	// bob still has the virtual default; ordering it saves it.
	menu, err := e.s.List(ctx, e.bob, false)
	require.NoError(t, err)
	require.Len(t, menu, 2)
	_, err = e.s.Order(ctx, e.bob, OrderInput{Top: []string{shared.ID}})
	mm := requireCode(t, err, 422, "dashboard_order_mismatch")
	require.Equal(t, DefaultID, mm.Fields["missing"])
	_, err = e.s.Order(ctx, e.bob, OrderInput{Top: []string{shared.ID, shared.ID}, Bottom: []string{DefaultID}})
	requireCode(t, err, 422, "dashboard_order_mismatch")
	ordered, err := e.s.Order(ctx, e.bob, OrderInput{Top: []string{shared.ID}, Bottom: []string{DefaultID}})
	require.NoError(t, err)
	require.Len(t, ordered, 2)
	require.Equal(t, shared.ID, ordered[0].ID)
	require.NotEqual(t, DefaultID, ordered[1].ID, "the default was saved and got its id")
	require.Equal(t, PlacementBottom, ordered[1].Placement)

	cp, err := e.s.Duplicate(ctx, e.bob, shared.ID, nil, CopySuffixRU)
	require.NoError(t, err)
	require.Equal(t, MaxNameRunes, len([]rune(cp.Name)), "the suffix is cut to the limit")
	require.False(t, cp.Shared)
	require.Equal(t, "icon:rocket", cp.Icon)
	require.Equal(t, distinctTypes(shared.Widgets), cp.WidgetTypes)
	named, err := e.s.Duplicate(ctx, e.bob, cp.ID, ptr("Named"), CopySuffixEN)
	require.NoError(t, err)
	require.Equal(t, "Named", named.Name)
	_, err = e.s.Duplicate(ctx, e.bob, cp.ID, ptr(" "), CopySuffixEN)
	requireCode(t, err, 422, "validation_failed")
	viaDefault, err := e.s.Duplicate(ctx, e.viewer, DefaultID, nil, CopySuffixEN)
	require.NoError(t, err, "duplicating the virtual default saves it first")
	require.Equal(t, DefaultName+CopySuffixEN, viaDefault.Name)

	// Hide: only shared ones; hidden ones leave the menu until shown again.
	_, err = e.s.SetHidden(ctx, e.bob, cp.ID, true)
	requireCode(t, err, 422, "dashboard_not_hideable")
	_, err = e.s.SetHidden(ctx, e.alice, DefaultID, true)
	requireCode(t, err, 422, "dashboard_not_hideable")
	h, err := e.s.SetHidden(ctx, e.bob, shared.ID, true)
	require.NoError(t, err)
	require.True(t, h.Hidden)
	menu, err = e.s.List(ctx, e.bob, false)
	require.NoError(t, err)
	for _, m := range menu {
		require.NotEqual(t, shared.ID, m.ID)
	}
	all, err := e.s.List(ctx, e.bob, true)
	require.NoError(t, err)
	require.Len(t, all, len(menu)+1)
	h, err = e.s.SetHidden(ctx, e.bob, shared.ID, false)
	require.NoError(t, err)
	require.False(t, h.Hidden)

	// The viewer never placed the shared one: hiding it creates the placement.
	h, err = e.s.SetHidden(ctx, e.viewer, shared.ID, true)
	require.NoError(t, err)
	require.True(t, h.Hidden)
	h, err = e.s.SetHidden(ctx, e.admin, shared.ID, false)
	require.NoError(t, err)
	require.False(t, h.Hidden)

	// Deleting a shared dashboard publishes deleted=true.
	e.pub.take()
	require.NoError(t, e.s.Delete(ctx, e.admin, shared.ID))
	ev := e.pub.take()
	require.Len(t, ev, 1)
	require.Equal(t, true, ev[0].event["deleted"])
	err = e.s.Delete(ctx, e.alice, cp.ID)
	requireCode(t, err, 404, "not_found")
	err = e.s.Delete(ctx, e.alice, "nope")
	requireCode(t, err, 404, "not_found")
}

func TestServiceDB_ThemeSource(t *testing.T) {
	theme := Theme{ID: "board-studio", OwnWidgets: []string{"focus"}, Widgets: []string{"board-studio.focus", "triage"}}
	e := newSvcEnv(t, fakeThemes{theme: theme})
	ctx := context.Background()
	focus := ThemeWidgetType("board-studio", "focus")

	ids := map[string]bool{}
	for _, wt := range e.s.Catalog(ctx, e.alice) {
		ids[wt.ID] = true
	}
	require.True(t, ids[focus])
	d, err := e.s.Get(ctx, e.alice, DefaultID)
	require.NoError(t, err)
	require.Equal(t, []string{focus, "triage-queue"}, d.WidgetTypes)

	broken := NewService(e.db, fakeThemes{err: errors.New("theme store down")}, nil)
	d, err = broken.Get(ctx, e.alice, DefaultID)
	require.NoError(t, err, "a theme failure falls back to the standard composition")
	require.Equal(t, distinctTypes(DefaultWidgets(Theme{}, NewCatalog(Theme{}))), d.WidgetTypes)

	// Without a publisher, shared changes still go through.
	s, err := broken.Create(ctx, e.bob, CreateInput{Name: "Quiet", Shared: ptr(true)})
	require.NoError(t, err)
	require.True(t, s.Shared)
}

// A shared dashboard the caller authored is not a personal one: with no
// personal dashboard left, the virtual default is back in their menu (§5.3,
// §6.2), "default" resolves to it, and hiding the shared one never empties
// the menu.
func TestServiceDB_VirtualDefaultBesideAuthoredShared(t *testing.T) {
	e := newSvcEnv(t, nil)
	ctx := context.Background()

	team, err := e.s.Create(ctx, e.alice, CreateInput{Name: "Team", Shared: ptr(true)})
	require.NoError(t, err)
	menu, err := e.s.List(ctx, e.alice, false)
	require.NoError(t, err)
	require.Len(t, menu, 2, "the saved default and the shared one")
	saved := menu[0]
	if saved.ID == team.ID {
		saved = menu[1]
	}
	require.True(t, saved.IsDefault)
	require.NoError(t, e.s.Delete(ctx, e.alice, saved.ID), "the shared one is still hers, so this is not the last")

	menu, err = e.s.List(ctx, e.alice, false)
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, m := range menu {
		ids[m.ID] = m.Virtual
	}
	require.Len(t, ids, 2)
	require.Contains(t, ids, team.ID)
	require.True(t, ids[DefaultID], "no personal dashboard -> the virtual default is listed")

	d, err := e.s.Get(ctx, e.alice, DefaultID)
	require.NoError(t, err)
	require.True(t, d.Virtual)

	_, err = e.s.SetHidden(ctx, e.alice, team.ID, true)
	require.NoError(t, err)
	menu, err = e.s.List(ctx, e.alice, false)
	require.NoError(t, err)
	require.Len(t, menu, 1)
	require.Equal(t, DefaultID, menu[0].ID)

	// The first change saves the default again; the virtual one is gone.
	_, err = e.s.Patch(ctx, e.alice, DefaultID, PatchInput{Name: ptr("Home")})
	require.NoError(t, err)
	menu, err = e.s.List(ctx, e.alice, false)
	require.NoError(t, err)
	require.Len(t, menu, 1)
	require.False(t, menu[0].Virtual)
	require.Equal(t, "Home", menu[0].Name)
}

func TestServiceDB_AgentUsageSettings(t *testing.T) {
	e := newSvcEnv(t, nil)
	ctx := context.Background()
	settings := json.RawMessage(`{"m":"quota","g":"account","p":"2026-11-01..2026-11-18","w":"month","f.account":["pool/team"],"f.project":["p1"]}`)
	layout := []Widget{{WidgetType: "agent-usage", W: 12, H: 6, Settings: settings}}
	d, err := e.s.PutLayout(ctx, e.alice, DefaultID, LayoutInput{Version: ptr(0), Widgets: layout})
	require.NoError(t, err)
	got, err := e.s.Get(ctx, e.alice, DefaultID)
	require.NoError(t, err)
	require.Len(t, got.Widgets, 1)
	require.Equal(t, "agent-usage", got.Widgets[0].WidgetType)
	require.JSONEq(t, string(settings), string(got.Widgets[0].Settings))

	settings = json.RawMessage(`{"m":"tokens","g":"workspace","f.workspace":["ws-1"]}`)
	layout[0].Settings = settings
	d, err = e.s.PutLayout(ctx, e.alice, d.ID, LayoutInput{Version: ptr(d.Version), Widgets: layout})
	require.NoError(t, err)
	got, err = e.s.Get(ctx, e.alice, DefaultID)
	require.NoError(t, err)
	require.JSONEq(t, string(settings), string(got.Widgets[0].Settings))

	for _, bad := range []string{`{"m":"spend"}`, `{"w":"year"}`, `{"n":21}`, `{"p":"yesterday"}`} {
		layout[0].Settings = json.RawMessage(bad)
		_, err = e.s.PutLayout(ctx, e.alice, d.ID, LayoutInput{Version: ptr(d.Version), Widgets: layout})
		requireCode(t, err, 422, "layout_invalid")
		got, err = e.s.Get(ctx, e.alice, DefaultID)
		require.NoError(t, err)
		require.Equal(t, d.Version, got.Version, "a rejected edit leaves the saved slice intact")
		require.JSONEq(t, string(settings), string(got.Widgets[0].Settings))
	}
}

// WidgetInstance backs the signed context of an external widget: it answers
// only for an instance of a dashboard the user reads, else 404 (or 403 for a
// non-member), so nobody gets a token for an instance they cannot see.
func TestServiceDB_WidgetInstance(t *testing.T) {
	e := newSvcEnv(t, nil)
	ctx := context.Background()
	settings := json.RawMessage(`{"project_id":"p1"}`)
	layout := []Widget{{WidgetType: "reference-ext.status", X: 0, Y: 0, W: 6, H: 4, Settings: settings}}

	mine, err := e.s.Create(ctx, e.alice, CreateInput{Name: "Alice's"})
	require.NoError(t, err)
	mine, err = e.s.PutLayout(ctx, e.alice, mine.ID, LayoutInput{Version: ptr(mine.Version), Widgets: layout})
	require.NoError(t, err)
	require.Len(t, mine.Widgets, 1)
	dash, inst := uuid.MustParse(mine.ID), uuid.MustParse(mine.Widgets[0].ID)

	got, err := e.s.WidgetInstance(ctx, e.ws, e.alice.UserID, dash, inst)
	require.NoError(t, err, "positive control: the owner of the dashboard")
	require.Equal(t, "reference-ext.status", got.Type)
	require.JSONEq(t, string(settings), string(got.Settings))

	other, err := e.s.Create(ctx, e.alice, CreateInput{Name: "Other", TemplateID: ptr("standard")})
	require.NoError(t, err)
	otherInst := uuid.MustParse(other.Widgets[0].ID)

	for name, tc := range map[string]struct {
		user, dash, inst uuid.UUID
		ws               uuid.UUID
		code             int
	}{
		"someone else's personal dashboard": {e.bob.UserID, dash, inst, e.ws, 404},
		"instance of another dashboard":     {e.alice.UserID, dash, otherInst, e.ws, 404},
		"missing instance":                  {e.alice.UserID, dash, uuid.New(), e.ws, 404},
		"missing dashboard":                 {e.alice.UserID, uuid.New(), inst, e.ws, 404},
		"another workspace":                 {e.alice.UserID, dash, inst, uuid.New(), 403},
	} {
		t.Run(name, func(t *testing.T) {
			_, werr := e.s.WidgetInstance(ctx, tc.ws, tc.user, tc.dash, tc.inst)
			requireCode(t, werr, tc.code, "")
		})
	}

	// Shared, it reads for every member.
	_, err = e.s.Patch(ctx, e.alice, mine.ID, PatchInput{Shared: ptr(true)})
	require.NoError(t, err)
	got, err = e.s.WidgetInstance(ctx, e.ws, e.bob.UserID, dash, inst)
	require.NoError(t, err, "a shared dashboard reads for another member")
	require.Equal(t, "reference-ext.status", got.Type)
}
