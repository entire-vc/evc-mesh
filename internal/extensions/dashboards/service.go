package dashboards

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
)

// DefaultID addresses the caller's default dashboard: the virtual one while
// they have no dashboards of their own, then the one it was saved as.
const DefaultID = "default"

// DefaultName is the name of the virtual default dashboard (§5.3).
const DefaultName = "Dashboard"

// Menu groups (§5.4).
const (
	PlacementTop    = "top"
	PlacementBottom = "bottom"
)

// EventUpdated is published on ws:<slug> whenever a shared dashboard changes.
const EventUpdated = "dashboard.updated"

// Workspace roles, as stored in workspace_members.role.
const (
	roleOwner  = "owner"
	roleAdmin  = "admin"
	roleViewer = "viewer"
)

// Dashboard is a dashboard as the caller sees it in their menu.
type Dashboard struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Icon      string `json:"icon"`
	Placement string `json:"placement"`
	Position  int    `json:"position"`
	IsDefault bool   `json:"is_default"`
	Virtual   bool   `json:"virtual"`
	Shared    bool   `json:"shared"`
	// Scope is §6.2's name of the same fact: "personal" or "workspace".
	Scope string `json:"scope"`
	// DefaultPlacement is where the dashboard lands for members who never
	// placed it; Placement is where it sits in the caller's own menu.
	DefaultPlacement string     `json:"default_placement"`
	MembersCanEdit   bool       `json:"members_can_edit"`
	AuthorUserID     *uuid.UUID `json:"author_user_id"`
	// AuthorName is the author's display name; null when the author left.
	AuthorName *string `json:"author_name"`
	// WidgetTypes are the distinct widget types on the dashboard, sorted, so
	// the menu can tell which dashboard hosts e.g. the mentions widget.
	WidgetTypes []string `json:"widget_types"`
	Hidden      bool     `json:"hidden"`
	CanEdit     bool     `json:"can_edit"`
	CanManage   bool     `json:"can_manage"`
	TemplateID  *string  `json:"template_id"`
	Version     int      `json:"version"`
}

// Detail is a dashboard with its widget instances.
type Detail struct {
	Dashboard
	Widgets []Widget `json:"widgets"`
}

// Error is a refusal the HTTP layer turns into a response as it is.
type Error struct {
	Status  int
	Code    string
	Message string
	Fields  map[string]string
	// Current is the dashboard as it is now, sent with stale_layout.
	Current *Detail
}

func (e *Error) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message) }

func errNotFound() *Error {
	return &Error{Status: http.StatusNotFound, Code: "not_found", Message: "Dashboard not found"}
}

func errForbidden(msg string) *Error {
	return &Error{Status: http.StatusForbidden, Code: "forbidden", Message: msg}
}

func errInvalid(field, msg string) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: "validation_failed", Message: "Validation failed", Fields: map[string]string{field: msg}}
}

// Publisher sends a JSON event to a Redis channel the WebSocket hub fans out
// (service.WSPublisher).
type Publisher interface {
	Publish(ctx context.Context, channel string, event any) error
}

// ThemeSource tells what the workspace's effective theme brings to dashboards.
type ThemeSource interface {
	DashboardTheme(ctx context.Context, workspaceID uuid.UUID) (Theme, error)
}

// Service is the dashboards model over the ext_dashboards_* tables.
type Service struct {
	db     *sqlx.DB
	themes ThemeSource
	pub    Publisher
	now    func() time.Time
	// extWidgets, when set, adds the widgets of enabled extensions to the catalog.
	extWidgets extensions.DashboardWidgetSource
}

// NewService builds the service; themes and pub may be nil (no theme, no events).
func NewService(db *sqlx.DB, themes ThemeSource, pub Publisher) *Service {
	return &Service{db: db, themes: themes, pub: pub, now: time.Now}
}

// Actor is the human the request acts for, in one workspace.
type Actor struct {
	UserID      uuid.UUID
	WorkspaceID uuid.UUID
	Role        string
}

func (a Actor) isAdmin() bool { return a.Role == roleOwner || a.Role == roleAdmin }

// ResolveActor reads the caller's workspace role: their membership row, or
// "owner" for the workspace owner (the same fallback the workspace guard has).
// Someone who is neither gets 403.
func (s *Service) ResolveActor(ctx context.Context, workspaceID, userID uuid.UUID) (Actor, error) {
	var role string
	err := s.db.GetContext(ctx, &role, `
		SELECT role FROM workspace_members WHERE workspace_id = $1 AND user_id = $2
		UNION ALL
		SELECT 'owner' FROM workspaces WHERE id = $1 AND owner_id = $2
		LIMIT 1`, workspaceID, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return Actor{}, errForbidden("not a member of this workspace")
	}
	if err != nil {
		return Actor{}, err
	}
	return Actor{UserID: userID, WorkspaceID: workspaceID, Role: role}, nil
}

// theme returns the workspace theme's contribution; a failure to read it falls
// back to "no theme" so the home screen still opens.
func (s *Service) theme(ctx context.Context, ws uuid.UUID) Theme {
	if s.themes == nil {
		return Theme{}
	}
	t, err := s.themes.DashboardTheme(ctx, ws)
	if err != nil {
		log.Printf("[dashboards] theme of workspace %s: %v (using the standard composition)", ws, err)
		return Theme{}
	}
	return t
}

// Catalog returns the widget types the workspace offers.
func (s *Service) Catalog(ctx context.Context, a Actor) []WidgetType {
	return s.catalog(ctx, a).Types()
}

// row is one dashboard joined with the caller's placement.
type row struct {
	ID             uuid.UUID      `db:"id"`
	WorkspaceID    uuid.UUID      `db:"workspace_id"`
	OwnerUserID    *uuid.UUID     `db:"owner_user_id"`
	AuthorUserID   *uuid.UUID     `db:"author_user_id"`
	AuthorName     *string        `db:"author_name"`
	WidgetTypes    pq.StringArray `db:"widget_types"`
	Shared         bool           `db:"shared"`
	MembersCanEdit bool           `db:"members_can_edit"`
	Name           string         `db:"name"`
	Icon           string         `db:"icon"`
	Placement      string         `db:"placement"`
	IsDefault      bool           `db:"is_default"`
	TemplateID     *string        `db:"template_id"`
	Version        int            `db:"version"`
	CreatedAt      time.Time      `db:"created_at"`
	PPlacement     *string        `db:"p_placement"`
	PPosition      *int           `db:"p_position"`
	PHidden        *bool          `db:"p_hidden"`
}

const rowColumns = `d.id, d.workspace_id, d.owner_user_id, d.author_user_id, d.scope = 'workspace' AS shared, d.members_can_edit,
	d.name, d.icon, d.default_placement AS placement, d.is_default, d.template_id, d.version, d.created_at,
	p.placement AS p_placement, p.position AS p_position, p.hidden AS p_hidden,
	NULLIF(COALESCE(NULLIF(u.display_name, ''), u.email), '') AS author_name,
	COALESCE((SELECT array_agg(DISTINCT w.widget_type COLLATE "C" ORDER BY w.widget_type COLLATE "C")
		FROM ext_dashboards_widgets w WHERE w.dashboard_id = d.id), '{}') AS widget_types`

// rowJoins completes rowColumns; $2 is the caller.
const rowJoins = `FROM ext_dashboards_dashboards d
		LEFT JOIN ext_dashboards_placements p ON p.dashboard_id = d.id AND p.user_id = $2
		LEFT JOIN users u ON u.id = d.author_user_id`

// unplacedPosition sorts a shared dashboard the caller never placed after the
// ones they did.
const unplacedPosition = 1 << 30

func (r row) isMine(a Actor) bool {
	return r.OwnerUserID != nil && *r.OwnerUserID == a.UserID
}

func (r row) isAuthor(a Actor) bool {
	return r.AuthorUserID != nil && *r.AuthorUserID == a.UserID
}

// canRead: a personal dashboard is its owner's alone; a shared one is every
// member's.
func (r row) canRead(a Actor) bool { return r.Shared || r.isMine(a) }

// canManage: rename, icon, sharing, delete. Personal: the owner. Shared: the
// author and workspace owners and admins.
func (r row) canManage(a Actor) bool {
	if !r.Shared {
		return r.isMine(a)
	}
	return r.isAuthor(a) || a.isAdmin()
}

// canEdit: the layout and "reset". Shared: whoever manages it, and every
// non-viewer member when the author let members edit.
func (r row) canEdit(a Actor) bool {
	if !r.Shared {
		return r.isMine(a)
	}
	return r.canManage(a) || (r.MembersCanEdit && a.Role != roleViewer)
}

func (r row) view(a Actor) Dashboard {
	d := Dashboard{
		ID: r.ID.String(), Name: r.Name, Icon: r.Icon, Placement: r.Placement, Position: unplacedPosition,
		IsDefault: r.IsDefault, Shared: r.Shared, Scope: scopeOf(r.Shared), DefaultPlacement: r.Placement,
		MembersCanEdit: r.MembersCanEdit, AuthorUserID: r.AuthorUserID,
		AuthorName: r.AuthorName, WidgetTypes: widgetTypesOf(r.WidgetTypes),
		CanEdit: r.canEdit(a), CanManage: r.canManage(a), TemplateID: r.TemplateID, Version: r.Version,
	}
	if r.PPlacement != nil {
		d.Placement = *r.PPlacement
	}
	if r.PPosition != nil {
		d.Position = *r.PPosition
	}
	if r.PHidden != nil {
		d.Hidden = *r.PHidden
	}
	return d
}

type queryer interface {
	sqlx.QueryerContext
	sqlx.ExecerContext
	GetContext(ctx context.Context, dest any, query string, args ...any) error
	SelectContext(ctx context.Context, dest any, query string, args ...any) error
}

// visibleRows lists the dashboards the caller sees: their personal ones and
// every shared one, with their own placement where they have one.
func visibleRows(ctx context.Context, q queryer, a Actor) ([]row, error) {
	var rows []row
	err := q.SelectContext(ctx, &rows, `SELECT `+rowColumns+`
		`+rowJoins+`
		WHERE d.workspace_id = $1 AND (d.owner_user_id = $2 OR d.scope = 'workspace')`, a.WorkspaceID, a.UserID)
	return rows, err
}

// getRow loads one dashboard of the workspace; another tenant's id or a
// personal dashboard of someone else is "not found".
func getRow(ctx context.Context, q queryer, a Actor, id uuid.UUID, forUpdate bool) (row, error) {
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE OF d"
	}
	var r row
	err := q.GetContext(ctx, &r, `SELECT `+rowColumns+`
		`+rowJoins+`
		WHERE d.workspace_id = $1 AND d.id = $3`+lock, a.WorkspaceID, a.UserID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return row{}, errNotFound()
	}
	if err != nil {
		return row{}, err
	}
	if !r.canRead(a) {
		return row{}, errNotFound()
	}
	return r, nil
}

// personalCount is how many personal dashboards the caller has. With none,
// the virtual default is in their menu (§5.3, §6.2), whatever shared
// dashboards they authored.
func personalCount(ctx context.Context, q queryer, a Actor) (int, error) {
	var n int
	err := q.GetContext(ctx, &n, `SELECT count(*) FROM ext_dashboards_dashboards
		WHERE workspace_id = $1 AND owner_user_id = $2`, a.WorkspaceID, a.UserID)
	return n, err
}

// ownCount is how many dashboards the caller has: personal ones and shared
// ones they made. The §5.2 limit and "last dashboard" count these.
func ownCount(ctx context.Context, q queryer, a Actor) (int, error) {
	var n int
	err := q.GetContext(ctx, &n, `SELECT count(*) FROM ext_dashboards_dashboards
		WHERE workspace_id = $1 AND (owner_user_id = $2 OR author_user_id = $2)`, a.WorkspaceID, a.UserID)
	return n, err
}

func (s *Service) widgets(ctx context.Context, q queryer, dashboardID uuid.UUID) ([]Widget, error) {
	var rows []struct {
		ID         uuid.UUID       `db:"id"`
		WidgetType string          `db:"widget_type"`
		X          int             `db:"x"`
		Y          int             `db:"y"`
		W          int             `db:"w"`
		H          int             `db:"h"`
		Settings   json.RawMessage `db:"settings"`
	}
	if err := q.SelectContext(ctx, &rows, `SELECT id, widget_type, x, y, w, h, settings
		FROM ext_dashboards_widgets WHERE dashboard_id = $1 ORDER BY y, x, created_at, id`, dashboardID); err != nil {
		return nil, err
	}
	out := make([]Widget, 0, len(rows))
	for _, r := range rows {
		out = append(out, Widget{ID: r.ID.String(), WidgetType: r.WidgetType, X: r.X, Y: r.Y, W: r.W, H: r.H, Settings: r.Settings})
	}
	return out, nil
}

func (s *Service) detail(ctx context.Context, q queryer, a Actor, r row) (*Detail, error) {
	ws, err := s.widgets(ctx, q, r.ID)
	if err != nil {
		return nil, err
	}
	d := r.view(a)
	d.WidgetTypes = distinctTypes(ws)
	return &Detail{Dashboard: d, Widgets: ws}, nil
}

// widgetTypesOf never returns nil, so the field is [] rather than null.
func widgetTypesOf(types []string) []string {
	if types == nil {
		return []string{}
	}
	return types
}

// distinctTypes is the sorted set of widget types of ws.
func distinctTypes(ws []Widget) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, w := range ws {
		if !seen[w.WidgetType] {
			seen[w.WidgetType] = true
			out = append(out, w.WidgetType)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Service) virtual(ctx context.Context, a Actor) *Detail {
	theme := s.theme(ctx, a.WorkspaceID)
	ws := DefaultWidgets(theme, NewCatalog(theme))
	return &Detail{
		// Scope and DefaultPlacement are what materialize will store.
		Dashboard: Dashboard{ID: DefaultID, Name: DefaultName, Placement: PlacementTop, Position: 0,
			Scope: ScopePersonal, DefaultPlacement: PlacementTop,
			IsDefault: true, Virtual: true, CanEdit: true, CanManage: true, Version: 0,
			WidgetTypes: distinctTypes(ws)},
		Widgets: ws,
	}
}

// sortMenu orders the dashboards as the menu shows them: top group, then
// bottom, each by position, unplaced shared ones last by creation.
func sortMenu(list []Dashboard, created map[string]time.Time) {
	group := func(p string) int {
		if p == PlacementBottom {
			return 1
		}
		return 0
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if ga, gb := group(a.Placement), group(b.Placement); ga != gb {
			return ga < gb
		}
		if a.Virtual != b.Virtual {
			return a.Virtual
		}
		if a.Position != b.Position {
			return a.Position < b.Position
		}
		if ca, cb := created[a.ID], created[b.ID]; !ca.Equal(cb) {
			return ca.Before(cb)
		}
		return a.ID < b.ID
	})
}

// List returns the caller's menu: personal and shared dashboards in order,
// hidden shared ones only with includeHidden. A caller with no dashboards of
// their own gets the virtual default one in front of the top group.
func (s *Service) List(ctx context.Context, a Actor, includeHidden bool) ([]Dashboard, error) {
	return s.list(ctx, s.db, a, includeHidden)
}

func (s *Service) list(ctx context.Context, q queryer, a Actor, includeHidden bool) ([]Dashboard, error) {
	rows, err := visibleRows(ctx, q, a)
	if err != nil {
		return nil, err
	}
	out := make([]Dashboard, 0, len(rows)+1)
	created := map[string]time.Time{}
	personal := false
	for _, r := range rows {
		if r.isMine(a) {
			personal = true
		}
		v := r.view(a)
		if v.Hidden && !includeHidden {
			continue
		}
		created[v.ID] = r.CreatedAt
		out = append(out, v)
	}
	if !personal {
		out = append(out, s.virtual(ctx, a).Dashboard)
	}
	sortMenu(out, created)
	return out, nil
}

// lockUser serializes one person's structural changes in one workspace
// (materializing the default, creating, deleting, reordering), so two tabs
// cannot both save the virtual default or both delete "the last" dashboard.
func lockUser(ctx context.Context, tx *sqlx.Tx, a Actor) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('ext-dashboards:' || $1::text || ':' || $2::text))`,
		a.WorkspaceID, a.UserID)
	return err
}

func (s *Service) inTx(ctx context.Context, a Actor, fn func(tx *sqlx.Tx) error) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockUser(ctx, tx, a); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// nextPosition is the position after the last one in the caller's group.
func nextPosition(ctx context.Context, q queryer, a Actor, placement string) (int, error) {
	var n int
	err := q.GetContext(ctx, &n, `SELECT COALESCE(MAX(position) + 1, 0) FROM ext_dashboards_placements
		WHERE workspace_id = $1 AND user_id = $2 AND placement = $3`, a.WorkspaceID, a.UserID, placement)
	return n, err
}

func upsertPlacement(ctx context.Context, q queryer, a Actor, id uuid.UUID, placement string, position int, hidden bool) error {
	_, err := q.ExecContext(ctx, `INSERT INTO ext_dashboards_placements (dashboard_id, workspace_id, user_id, placement, position, hidden, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (dashboard_id, user_id) DO UPDATE
		SET placement = EXCLUDED.placement, position = EXCLUDED.position, hidden = EXCLUDED.hidden, updated_at = now()`,
		id, a.WorkspaceID, a.UserID, placement, position, hidden)
	return mapDBError(err)
}

func replaceWidgets(ctx context.Context, q queryer, a Actor, dashboardID uuid.UUID, widgets []Widget, keep map[uuid.UUID]bool) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM ext_dashboards_widgets WHERE dashboard_id = $1`, dashboardID); err != nil {
		return err
	}
	for _, w := range widgets {
		id := uuid.New()
		if w.ID != "" {
			if prev, err := uuid.Parse(w.ID); err == nil && keep[prev] {
				id = prev
			}
		}
		settings, err := NormalizeSettings(w.Settings)
		if err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO ext_dashboards_widgets (id, dashboard_id, workspace_id, widget_type, x, y, w, h, settings)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, id, dashboardID, a.WorkspaceID, w.WidgetType, w.X, w.Y, w.W, w.H, []byte(settings)); err != nil {
			return err
		}
	}
	return nil
}

type newDashboard struct {
	name, icon, placement  string
	shared, membersCanEdit bool
	isDefault              bool
	templateID             *string
	widgets                []Widget
}

// insert creates a dashboard, its widgets and the caller's placement at the
// end of the chosen group (front of the top group for the default one).
func (s *Service) insert(ctx context.Context, tx *sqlx.Tx, a Actor, n newDashboard) (row, error) {
	id := uuid.New()
	var owner *uuid.UUID
	if !n.shared {
		owner = &a.UserID
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ext_dashboards_dashboards
		(id, workspace_id, owner_user_id, author_user_id, scope, members_can_edit, name, icon, default_placement, is_default, template_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		id, a.WorkspaceID, owner, a.UserID, scopeOf(n.shared), n.membersCanEdit, n.name, n.icon, n.placement, n.isDefault, n.templateID); err != nil {
		return row{}, mapDBError(err)
	}
	if err := replaceWidgets(ctx, tx, a, id, n.widgets, nil); err != nil {
		return row{}, err
	}
	pos, err := nextPosition(ctx, tx, a, n.placement)
	if err != nil {
		return row{}, err
	}
	if n.isDefault {
		if err := tx.GetContext(ctx, &pos, `SELECT COALESCE(MIN(position) - 1, 0) FROM ext_dashboards_placements
			WHERE workspace_id = $1 AND user_id = $2 AND placement = $3`, a.WorkspaceID, a.UserID, n.placement); err != nil {
			return row{}, err
		}
	}
	if err := upsertPlacement(ctx, tx, a, id, n.placement, pos, false); err != nil {
		return row{}, err
	}
	return getRow(ctx, tx, a, id, true)
}

// materialize saves the virtual default dashboard (§5.3) when the caller has
// no personal dashboard; widgets nil means the default composition. It
// returns ok=false when there is nothing virtual to save.
func (s *Service) materialize(ctx context.Context, tx *sqlx.Tx, a Actor, widgets []Widget) (row, bool, error) {
	n, err := personalCount(ctx, tx, a)
	if err != nil || n > 0 {
		return row{}, false, err
	}
	if widgets == nil {
		widgets = s.virtual(ctx, a).Widgets
	}
	r, err := s.insert(ctx, tx, a, newDashboard{name: DefaultName, placement: PlacementTop, isDefault: true, widgets: widgets})
	return r, err == nil, err
}

// resolve finds the dashboard an id names for the caller inside tx. "default"
// is their saved default dashboard; while there is none and they have no
// personal dashboard, it is virtual (virtual=true, zero row).
func (s *Service) resolve(ctx context.Context, tx *sqlx.Tx, a Actor, id string) (r row, virtual bool, err error) {
	if id == DefaultID {
		var did uuid.UUID
		err = tx.GetContext(ctx, &did, `SELECT id FROM ext_dashboards_dashboards
			WHERE workspace_id = $1 AND owner_user_id = $2 AND is_default ORDER BY created_at LIMIT 1`, a.WorkspaceID, a.UserID)
		if errors.Is(err, sql.ErrNoRows) {
			n, cerr := personalCount(ctx, tx, a)
			if cerr != nil {
				return row{}, false, cerr
			}
			if n == 0 {
				return row{}, true, nil
			}
			return row{}, false, errNotFound()
		}
		if err != nil {
			return row{}, false, err
		}
		r, err = getRow(ctx, tx, a, did, true)
		return r, false, err
	}
	uid, perr := uuid.Parse(id)
	if perr != nil {
		return row{}, false, errNotFound()
	}
	r, err = getRow(ctx, tx, a, uid, true)
	return r, false, err
}

// resolveOrMaterialize is resolve that saves the virtual default first.
func (s *Service) resolveOrMaterialize(ctx context.Context, tx *sqlx.Tx, a Actor, id string) (row, error) {
	r, virtual, err := s.resolve(ctx, tx, a, id)
	if err != nil || !virtual {
		return r, err
	}
	r, _, err = s.materialize(ctx, tx, a, nil)
	return r, err
}

// Get returns one dashboard with its widgets.
func (s *Service) Get(ctx context.Context, a Actor, id string) (*Detail, error) {
	var out *Detail
	err := s.inTx(ctx, a, func(tx *sqlx.Tx) error {
		r, virtual, err := s.resolve(ctx, tx, a, id)
		if err != nil {
			return err
		}
		if virtual {
			out = s.virtual(ctx, a)
			return nil
		}
		out, err = s.detail(ctx, tx, a, r)
		return err
	})
	return out, err
}

func cleanName(name string) (string, *Error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxNameRunes {
		return "", errInvalid("name", fmt.Sprintf("1 to %d characters", MaxNameRunes))
	}
	return name, nil
}

// cleanIcon accepts the shared icon format (§5.11): one emoji grapheme,
// icon:<name> with name [a-z0-9-]{1,40}, or empty.
func cleanIcon(icon string) (string, *Error) {
	icon = strings.TrimSpace(icon)
	if err := domain.ValidateIconValue(icon); err != nil {
		return "", errInvalid("icon", "one emoji, icon:<name> or empty")
	}
	return icon, nil
}

func cleanPlacement(p string) (string, *Error) {
	switch p {
	case PlacementTop, PlacementBottom:
		return p, nil
	case "":
		return PlacementBottom, nil
	}
	return "", errInvalid("placement", "top or bottom")
}

// CreateInput is POST /workspaces/:ws/dashboards.
type CreateInput struct {
	Name       string  `json:"name"`
	Icon       string  `json:"icon"`
	Placement  string  `json:"placement"`
	TemplateID *string `json:"template_id"`
	// Shared is a pointer so an explicit false is told from an absent one:
	// {"scope":"workspace","shared":false} is a contradiction, not a share.
	Shared *bool `json:"shared"`
	// Scope is the §6.2 form of Shared: "workspace" shares, "personal" does not.
	Scope          string `json:"scope"`
	MembersCanEdit bool   `json:"members_can_edit"`
}

// sharedFromScope folds §6.2's scope into the shared flag. An empty scope
// leaves shared as sent; a scope that contradicts an explicit shared is 422.
func sharedFromScope(shared *bool, scope *string) (*bool, *Error) {
	if scope == nil || *scope == "" {
		return shared, nil
	}
	var want bool
	switch strings.TrimSpace(*scope) {
	case ScopeWorkspace:
		want = true
	case ScopePersonal:
		want = false
	default:
		return nil, errInvalid("scope", "personal or workspace")
	}
	if shared != nil && *shared != want {
		return nil, errInvalid("scope", "contradicts shared")
	}
	return &want, nil
}

// Create makes a dashboard from a template. A viewer cannot share; the first
// dashboard of someone still on the virtual default saves the default first,
// so it does not vanish from their menu.
func (s *Service) Create(ctx context.Context, a Actor, in CreateInput) (*Detail, error) {
	name, verr := cleanName(in.Name)
	if verr != nil {
		return nil, verr
	}
	icon, verr := cleanIcon(in.Icon)
	if verr != nil {
		return nil, verr
	}
	placement, verr := cleanPlacement(in.Placement)
	if verr != nil {
		return nil, verr
	}
	sharedPtr, verr := sharedFromScope(in.Shared, &in.Scope)
	if verr != nil {
		return nil, verr
	}
	shared := sharedPtr != nil && *sharedPtr
	if shared && a.Role == roleViewer {
		return nil, errForbidden("a viewer cannot share dashboards with the workspace")
	}
	if in.MembersCanEdit && !shared {
		return nil, errInvalid("members_can_edit", "only a shared dashboard can let members edit")
	}
	tpl := TemplateEmpty
	if in.TemplateID != nil && *in.TemplateID != "" {
		tpl = *in.TemplateID
	}
	t, ok := TemplateByID(tpl)
	if !ok {
		return nil, errInvalid("template_id", "unknown template")
	}
	var templateID *string
	if tpl != TemplateEmpty {
		templateID = &tpl
	}
	var out *Detail
	err := s.inTx(ctx, a, func(tx *sqlx.Tx) error {
		if _, _, err := s.materialize(ctx, tx, a, nil); err != nil {
			return err
		}
		if err := s.checkLimit(ctx, tx, a); err != nil {
			return err
		}
		r, err := s.insert(ctx, tx, a, newDashboard{name: name, icon: icon, placement: placement, shared: shared,
			membersCanEdit: in.MembersCanEdit, templateID: templateID, widgets: t.Widgets})
		if err != nil {
			return err
		}
		out, err = s.detail(ctx, tx, a, r)
		return err
	})
	if err != nil {
		return nil, err
	}
	if out.Shared {
		s.publish(ctx, a.WorkspaceID, out.ID, out.Version, false)
	}
	return out, nil
}

func (s *Service) checkLimit(ctx context.Context, tx *sqlx.Tx, a Actor) error {
	n, err := ownCount(ctx, tx, a)
	if err != nil {
		return err
	}
	if n >= MaxDashboardsPerUser {
		return &Error{Status: http.StatusUnprocessableEntity, Code: "dashboard_limit",
			Message: fmt.Sprintf("You can have at most %d dashboards in a workspace", MaxDashboardsPerUser)}
	}
	return nil
}

// PatchInput is PATCH /dashboards/:id; absent fields stay as they are.
type PatchInput struct {
	Name           *string `json:"name"`
	Icon           *string `json:"icon"`
	Shared         *bool   `json:"shared"`
	Scope          *string `json:"scope"`
	MembersCanEdit *bool   `json:"members_can_edit"`
	// DefaultPlacement: "top" or "bottom", where members who never placed
	// the dashboard see it.
	DefaultPlacement *string `json:"default_placement"`
}

// SharingInput is PATCH /dashboards/:id/sharing (§6.2): only the sharing
// fields, all of them under the manage right.
type SharingInput struct {
	Scope            *string `json:"scope"`
	Shared           *bool   `json:"shared"`
	MembersCanEdit   *bool   `json:"members_can_edit"`
	DefaultPlacement *string `json:"default_placement"`
}

// Sharing changes scope, members_can_edit and default_placement.
func (s *Service) Sharing(ctx context.Context, a Actor, id string, in SharingInput) (*Detail, error) {
	return s.Patch(ctx, a, id, PatchInput{Scope: in.Scope, Shared: in.Shared,
		MembersCanEdit: in.MembersCanEdit, DefaultPlacement: in.DefaultPlacement})
}

// touchesSharing reports whether a patch changes anything under the manage
// right; name and icon alone need only the edit right (§6.2).
func (in PatchInput) touchesSharing() bool {
	return in.Shared != nil || (in.Scope != nil && *in.Scope != "") || in.MembersCanEdit != nil || in.DefaultPlacement != nil
}

// Patch renames, changes the icon or the sharing. "default" is saved first.
// Name and icon take the edit right, the sharing fields the manage right.
// Sharing back to personal hands the dashboard to its author and drops the
// other members' placements; a dashboard whose author left cannot go back.
func (s *Service) Patch(ctx context.Context, a Actor, id string, in PatchInput) (*Detail, error) {
	var out *Detail
	var wasShared bool
	// The row as written, for the event when the caller can no longer read it.
	var goneID string
	var goneVersion int
	err := s.inTx(ctx, a, func(tx *sqlx.Tx) error {
		r, err := s.resolveOrMaterialize(ctx, tx, a, id)
		if err != nil {
			return err
		}
		wasShared = r.Shared
		if in.touchesSharing() && !r.canManage(a) {
			return errForbidden("only the author or a workspace admin can change the sharing of this dashboard")
		}
		if !r.canEdit(a) {
			return errForbidden("you cannot edit this dashboard")
		}
		sharedIn, verr := sharedFromScope(in.Shared, in.Scope)
		if verr != nil {
			return verr
		}
		defaultPlacement := r.Placement
		if in.DefaultPlacement != nil {
			pl, perr := cleanPlacement(*in.DefaultPlacement)
			if perr != nil {
				return errInvalid("default_placement", "top or bottom")
			}
			defaultPlacement = pl
		}
		name, icon := r.Name, r.Icon
		if in.Name != nil {
			n, verr := cleanName(*in.Name)
			if verr != nil {
				return verr
			}
			name = n
		}
		if in.Icon != nil {
			ic, verr := cleanIcon(*in.Icon)
			if verr != nil {
				return verr
			}
			icon = ic
		}
		shared, membersCanEdit := r.Shared, r.MembersCanEdit
		if sharedIn != nil {
			shared = *sharedIn
		}
		if in.MembersCanEdit != nil {
			membersCanEdit = *in.MembersCanEdit
		}
		if !shared {
			if in.MembersCanEdit != nil && *in.MembersCanEdit {
				return errInvalid("members_can_edit", "only a shared dashboard can let members edit")
			}
			membersCanEdit = false
		}
		if (shared != r.Shared || membersCanEdit != r.MembersCanEdit) && a.Role == roleViewer {
			return errForbidden("a viewer cannot share dashboards with the workspace")
		}
		owner := r.OwnerUserID
		switch {
		case shared && !r.Shared:
			owner = nil
		case !shared && r.Shared:
			if r.AuthorUserID == nil {
				return errInvalid("shared", "the author has left the workspace; this dashboard can only stay shared")
			}
			owner = r.AuthorUserID
			if _, err = tx.ExecContext(ctx, `DELETE FROM ext_dashboards_placements WHERE dashboard_id = $1 AND user_id <> $2`, r.ID, *owner); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE ext_dashboards_dashboards
			SET name = $2, icon = $3, scope = $4, members_can_edit = $5, owner_user_id = $6,
				default_placement = $7, updated_at = now()
			WHERE id = $1`, r.ID, name, icon, scopeOf(shared), membersCanEdit, owner, defaultPlacement); err != nil {
			return mapDBError(err)
		}
		// Patch leaves the layout version as it was.
		rowID, rowVersion := r.ID, r.Version
		if r, err = getRow(ctx, tx, a, rowID, false); err != nil {
			// Unsharing by an admin hands the dashboard to its author: it is
			// no longer the caller's to read.
			var e *Error
			if errors.As(err, &e) && e.Status == http.StatusNotFound {
				out = nil
				goneID, goneVersion = rowID.String(), rowVersion
				return nil
			}
			return err
		}
		out, err = s.detail(ctx, tx, a, r)
		return err
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		s.publishID(ctx, a.WorkspaceID, goneID, goneVersion, false)
		return nil, nil
	}
	if wasShared || out.Shared {
		s.publish(ctx, a.WorkspaceID, out.ID, out.Version, false)
	}
	return out, nil
}

// Delete removes a dashboard. The caller's last own dashboard (and the
// virtual one) cannot go: 409 last_dashboard.
func (s *Service) Delete(ctx context.Context, a Actor, id string) error {
	var deleted row
	err := s.inTx(ctx, a, func(tx *sqlx.Tx) error {
		r, virtual, err := s.resolve(ctx, tx, a, id)
		if err != nil {
			return err
		}
		if virtual {
			return errLast()
		}
		if !r.canManage(a) {
			return errForbidden("only the author or a workspace admin can delete this dashboard")
		}
		if r.isMine(a) || r.isAuthor(a) {
			n, cerr := ownCount(ctx, tx, a)
			if cerr != nil {
				return cerr
			}
			if n <= 1 {
				return errLast()
			}
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM ext_dashboards_dashboards WHERE id = $1`, r.ID); err != nil {
			return err
		}
		deleted = r
		return nil
	})
	if err != nil {
		return err
	}
	if deleted.Shared {
		s.publish(ctx, a.WorkspaceID, deleted.ID.String(), deleted.Version, true)
	}
	return nil
}

// scopeOf is the §6.1 scope column value of a dashboard.
func scopeOf(shared bool) string {
	if shared {
		return ScopeWorkspace
	}
	return ScopePersonal
}

// Scopes of a dashboard (§6.1 scope column).
const (
	ScopePersonal  = "personal"
	ScopeWorkspace = "workspace"
)

func errLast() *Error {
	return &Error{Status: http.StatusConflict, Code: "last_dashboard", Message: "The last dashboard cannot be deleted"}
}

// Name suffixes of a copy (§6.2): Russian for a Russian-speaking caller,
// English otherwise.
const (
	CopySuffixRU = " (копия)"
	CopySuffixEN = " (copy)"
)

// Duplicate copies a dashboard the caller can read into a new personal one in
// the same group of their menu.
func (s *Service) Duplicate(ctx context.Context, a Actor, id string, name *string, suffix string) (*Detail, error) {
	return s.Copy(ctx, a, id, name, "", suffix)
}

// Copy is Duplicate into a chosen group ("top" or "bottom"; "" keeps the
// source's group): POST /dashboards/:id/copy of §6.2.
func (s *Service) Copy(ctx context.Context, a Actor, id string, name *string, placement, suffix string) (*Detail, error) {
	if placement != "" {
		pl, verr := cleanPlacement(placement)
		if verr != nil {
			return nil, verr
		}
		placement = pl
	}
	var out *Detail
	err := s.inTx(ctx, a, func(tx *sqlx.Tx) error {
		src, err := s.resolveOrMaterialize(ctx, tx, a, id)
		if err != nil {
			return err
		}
		if _, _, err = s.materialize(ctx, tx, a, nil); err != nil {
			return err
		}
		err = s.checkLimit(ctx, tx, a)
		if err != nil {
			return err
		}
		newName := src.Name + suffix
		if r := []rune(newName); len(r) > MaxNameRunes {
			newName = string(r[:MaxNameRunes])
		}
		if name != nil {
			n, verr := cleanName(*name)
			if verr != nil {
				return verr
			}
			newName = n
		}
		widgets, err := s.widgets(ctx, tx, src.ID)
		if err != nil {
			return err
		}
		for i := range widgets {
			widgets[i].ID = ""
		}
		group := placement
		if group == "" {
			group = src.view(a).Placement
		}
		r, err := s.insert(ctx, tx, a, newDashboard{name: newName, icon: src.Icon, placement: group,
			templateID: src.TemplateID, widgets: widgets})
		if err != nil {
			return err
		}
		out, err = s.detail(ctx, tx, a, r)
		return err
	})
	return out, err
}

// Reset gives a default dashboard back its default composition (§5.3).
func (s *Service) Reset(ctx context.Context, a Actor, id string) (*Detail, error) {
	var out *Detail
	err := s.inTx(ctx, a, func(tx *sqlx.Tx) error {
		r, virtual, err := s.resolve(ctx, tx, a, id)
		if err != nil {
			return err
		}
		if virtual {
			out = s.virtual(ctx, a)
			return nil
		}
		if !r.IsDefault {
			return &Error{Status: http.StatusUnprocessableEntity, Code: "not_default_dashboard", Message: "Only the default dashboard can be reset"}
		}
		if !r.canEdit(a) {
			return errForbidden("you cannot edit this dashboard")
		}
		err = replaceWidgets(ctx, tx, a, r.ID, s.virtual(ctx, a).Widgets, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE ext_dashboards_dashboards SET version = version + 1, updated_at = now() WHERE id = $1`, r.ID); err != nil {
			return err
		}
		if r, err = getRow(ctx, tx, a, r.ID, false); err != nil {
			return err
		}
		out, err = s.detail(ctx, tx, a, r)
		return err
	})
	if err != nil {
		return nil, err
	}
	if out.Shared {
		s.publish(ctx, a.WorkspaceID, out.ID, out.Version, false)
	}
	return out, nil
}

// LayoutInput is PUT /dashboards/:id/layout: the whole grid and the version it
// was edited from.
type LayoutInput struct {
	Version *int     `json:"version"`
	Widgets []Widget `json:"widgets"`
}

// PutLayout replaces the grid. A version other than the stored one is 409
// stale_layout with the dashboard as it is now. The virtual default has
// version 0: saving it creates the dashboard with this grid.
func (s *Service) PutLayout(ctx context.Context, a Actor, id string, in LayoutInput) (*Detail, error) {
	if in.Version == nil {
		return nil, errInvalid("version", "required")
	}
	if in.Widgets == nil {
		in.Widgets = []Widget{}
	}
	cat := s.catalog(ctx, a)
	if err := ValidateLayout(cat, in.Widgets); err != nil {
		var le *LayoutError
		if errors.As(err, &le) {
			return nil, &Error{Status: http.StatusUnprocessableEntity, Code: "layout_invalid", Message: "The layout is not valid", Fields: le.Fields}
		}
		return nil, err
	}
	var out *Detail
	err := s.inTx(ctx, a, func(tx *sqlx.Tx) error {
		r, virtual, err := s.resolve(ctx, tx, a, id)
		if err != nil {
			return err
		}
		if virtual {
			if *in.Version != 0 {
				return stale(s.virtual(ctx, a))
			}
			for i := range in.Widgets {
				in.Widgets[i].ID = ""
			}
			if r, _, err = s.materialize(ctx, tx, a, in.Widgets); err != nil {
				return err
			}
			out, err = s.detail(ctx, tx, a, r)
			return err
		}
		if !r.canEdit(a) {
			return errForbidden("you cannot edit this dashboard")
		}
		if r.Version != *in.Version {
			cur, derr := s.detail(ctx, tx, a, r)
			if derr != nil {
				return derr
			}
			return stale(cur)
		}
		existing, err := s.widgets(ctx, tx, r.ID)
		if err != nil {
			return err
		}
		keep := map[uuid.UUID]bool{}
		for _, w := range existing {
			keep[uuid.MustParse(w.ID)] = true
		}
		err = replaceWidgets(ctx, tx, a, r.ID, in.Widgets, keep)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE ext_dashboards_dashboards SET version = version + 1, updated_at = now() WHERE id = $1`, r.ID); err != nil {
			return err
		}
		if r, err = getRow(ctx, tx, a, r.ID, false); err != nil {
			return err
		}
		out, err = s.detail(ctx, tx, a, r)
		return err
	})
	if err != nil {
		return nil, err
	}
	if out.Shared {
		s.publish(ctx, a.WorkspaceID, out.ID, out.Version, false)
	}
	return out, nil
}

func stale(cur *Detail) *Error {
	return &Error{Status: http.StatusConflict, Code: "stale_layout", Message: "The dashboard was changed elsewhere", Current: cur}
}

// OrderInput is PUT /workspaces/:ws/dashboards/order.
type OrderInput struct {
	Top    []string `json:"top"`
	Bottom []string `json:"bottom"`
	// Hidden, when sent, makes the call cover the whole menu (§6.2): hidden
	// shared dashboards are listed here, and only shared ones may be.
	Hidden []string `json:"hidden"`
}

// Order sets the whole menu order in one call. The ids must be exactly the
// dashboards the caller's menu shows (hidden ones excluded); anything else —
// someone's personal dashboard, another tenant's id, a hidden one, a repeat
// or a missing one — is 422. "default" stands for the virtual default and
// saves it.
func (s *Service) Order(ctx context.Context, a Actor, in OrderInput) ([]Dashboard, error) {
	var out []Dashboard
	err := s.inTx(ctx, a, func(tx *sqlx.Tx) error {
		withHidden := in.Hidden != nil
		menu, err := s.list(ctx, tx, a, withHidden)
		if err != nil {
			return err
		}
		want := map[string]bool{}
		byID := map[string]Dashboard{}
		for _, d := range menu {
			want[d.ID] = true
			byID[d.ID] = d
		}
		for _, id := range in.Hidden {
			if d, ok := byID[id]; ok && !d.Shared {
				return &Error{Status: http.StatusUnprocessableEntity, Code: "dashboard_not_hideable",
					Message: "Only a shared dashboard can be hidden; delete a personal one instead"}
			}
		}
		seen := map[string]bool{}
		var unknown []string
		for _, id := range append(append(append([]string{}, in.Top...), in.Bottom...), in.Hidden...) {
			if !want[id] || seen[id] {
				unknown = append(unknown, id)
			}
			seen[id] = true
		}
		var missing []string
		for id := range want {
			if !seen[id] {
				missing = append(missing, id)
			}
		}
		if len(unknown) > 0 || len(missing) > 0 {
			sort.Strings(missing)
			fields := map[string]string{}
			if len(unknown) > 0 {
				fields["unknown"] = strings.Join(unknown, ",")
			}
			if len(missing) > 0 {
				fields["missing"] = strings.Join(missing, ",")
			}
			return &Error{Status: http.StatusUnprocessableEntity, Code: "dashboard_order_mismatch",
				Message: "The order must list exactly the dashboards of your menu", Fields: fields}
		}
		if seen[DefaultID] {
			r, ok, merr := s.materialize(ctx, tx, a, nil)
			if merr != nil {
				return merr
			}
			if ok {
				swap := func(ids []string) {
					for i, id := range ids {
						if id == DefaultID {
							ids[i] = r.ID.String()
						}
					}
				}
				swap(in.Top)
				swap(in.Bottom)
			}
		}
		for group, ids := range map[string][]string{PlacementTop: in.Top, PlacementBottom: in.Bottom} {
			for i, id := range ids {
				err = upsertPlacement(ctx, tx, a, uuid.MustParse(id), group, i, false)
				if err != nil {
					return err
				}
			}
		}
		// Hidden ones keep their group and go after the visible ones.
		for i, id := range in.Hidden {
			group := byID[id].Placement
			pos := len(in.Bottom) + i
			if group == PlacementTop {
				pos = len(in.Top) + i
			}
			err = upsertPlacement(ctx, tx, a, uuid.MustParse(id), group, pos, true)
			if err != nil {
				return err
			}
		}
		out, err = s.list(ctx, tx, a, false)
		return err
	})
	return out, err
}

// SetHidden hides a shared dashboard from the caller's menu or brings it back
// (at the end of its group). A personal dashboard cannot be hidden: 422.
func (s *Service) SetHidden(ctx context.Context, a Actor, id string, hidden bool) (*Dashboard, error) {
	var out *Dashboard
	err := s.inTx(ctx, a, func(tx *sqlx.Tx) error {
		r, virtual, err := s.resolve(ctx, tx, a, id)
		if err != nil {
			return err
		}
		if virtual || !r.Shared {
			return &Error{Status: http.StatusUnprocessableEntity, Code: "dashboard_not_hideable",
				Message: "Only a shared dashboard can be hidden; delete a personal one instead"}
		}
		v := r.view(a)
		pos := v.Position
		if !hidden && (v.Hidden || r.PPosition == nil) {
			if pos, err = nextPosition(ctx, tx, a, v.Placement); err != nil {
				return err
			}
		}
		if r.PPosition == nil && hidden {
			if pos, err = nextPosition(ctx, tx, a, v.Placement); err != nil {
				return err
			}
		}
		err = upsertPlacement(ctx, tx, a, r.ID, v.Placement, pos, hidden)
		if err != nil {
			return err
		}
		if r, err = getRow(ctx, tx, a, r.ID, false); err != nil {
			return err
		}
		v = r.view(a)
		out = &v
		return nil
	})
	return out, err
}

// PrepareMemberRemoval implements service.MemberRemovalHook. Removing a
// member keeps their shared dashboards and clears author_user_id through the
// foreign key, which changes what every member's menu shows, so each of those
// dashboards gets dashboard.updated {id, version} once the member is gone.
// The layout version stays as it was, as with Patch. Their personal
// dashboards and placements go with them and concern nobody else.
func (s *Service) PrepareMemberRemoval(ctx context.Context, workspaceID, userID uuid.UUID) func(context.Context) {
	var authored []struct {
		ID      uuid.UUID `db:"id"`
		Version int       `db:"version"`
	}
	if err := s.db.SelectContext(ctx, &authored, `SELECT id, version FROM ext_dashboards_dashboards
		WHERE workspace_id = $1 AND author_user_id = $2 AND scope = 'workspace'
		ORDER BY created_at, id`, workspaceID, userID); err != nil {
		log.Printf("[dashboards] member removal %s: shared dashboards: %v", userID, err)
		return nil
	}
	if len(authored) == 0 {
		return nil
	}
	return func(ctx context.Context) {
		for _, d := range authored {
			s.publishID(ctx, workspaceID, d.ID.String(), d.Version, false)
		}
	}
}

func (s *Service) publish(ctx context.Context, ws uuid.UUID, id string, version int, deleted bool) {
	s.publishID(ctx, ws, id, version, deleted)
}

// publishID sends dashboard.updated {id, version} on the workspace channel.
// Best effort: the change is committed, a lost event only delays other tabs
// until their next load.
func (s *Service) publishID(ctx context.Context, ws uuid.UUID, id string, version int, deleted bool) {
	if s.pub == nil {
		return
	}
	var slug string
	if err := s.db.GetContext(ctx, &slug, `SELECT slug FROM workspaces WHERE id = $1`, ws); err != nil {
		log.Printf("[dashboards] event for %s: workspace slug: %v", id, err)
		return
	}
	ev := map[string]any{"event_type": EventUpdated, "workspace_id": ws.String(), "id": id, "version": version}
	if deleted {
		ev["deleted"] = true
	}
	if err := s.pub.Publish(ctx, "ws:"+slug, ev); err != nil {
		log.Printf("[dashboards] event for %s: %v", id, err)
	}
}

// mapDBError turns a foreign-key failure on a membership row into a readable
// refusal: it happens for a workspace owner whose membership row is missing,
// or for someone removed from the workspace mid-request.
func mapDBError(err error) error {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23503" {
		return &Error{Status: http.StatusConflict, Code: "conflict",
			Message: "Your workspace membership record is missing; ask an administrator to re-add you"}
	}
	return err
}

// WidgetInstance is one saved widget of a dashboard, as an external widget's
// signed context needs it: its type ("<ext_id>.<widget_id>") and its settings.
type WidgetInstance struct {
	Type     string
	Settings json.RawMessage
}

// WidgetInstance returns instance instanceID of dashboard dashboardID when the
// user reads that dashboard in workspace workspaceID (PRD §5.5: their own or a
// shared one). A dashboard of another workspace, someone else's personal one,
// a missing one, or an instance that is not on that dashboard is "not found"
// (404), the same answer, so the refusal confirms nothing.
func (s *Service) WidgetInstance(ctx context.Context, workspaceID, userID, dashboardID, instanceID uuid.UUID) (*WidgetInstance, error) {
	a, err := s.ResolveActor(ctx, workspaceID, userID)
	if err != nil {
		return nil, err
	}
	if _, err = getRow(ctx, s.db, a, dashboardID, false); err != nil {
		return nil, err
	}
	var w struct {
		Type     string          `db:"widget_type"`
		Settings json.RawMessage `db:"settings"`
	}
	err = s.db.GetContext(ctx, &w, `SELECT widget_type, settings FROM ext_dashboards_widgets
		WHERE dashboard_id = $1 AND id = $2`, dashboardID, instanceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound()
	}
	if err != nil {
		return nil, err
	}
	return &WidgetInstance{Type: w.Type, Settings: w.Settings}, nil
}
