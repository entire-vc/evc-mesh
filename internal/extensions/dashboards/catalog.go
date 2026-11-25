package dashboards

import (
	"encoding/json"
	"sort"
	"strings"
)

// GridColumns is the width of the dashboard grid (§5.5).
const GridColumns = 12

// MaxCellSpan is the largest w or h of one widget instance.
const MaxCellSpan = 12

// Cells is a widget size in grid cells.
type Cells struct {
	W int `json:"w"`
	H int `json:"h"`
}

// SizeRange is the default size of a widget type and the bounds a layout may
// resize it within.
type SizeRange struct {
	Default Cells `json:"default"`
	Min     Cells `json:"min"`
	Max     Cells `json:"max"`
}

// Widget origins (WidgetDescriptor v2 `origin`).
const (
	OriginCore      = "core"
	OriginExtension = "extension"
	OriginTheme     = "theme"
	OriginExternal  = "external"
)

// WidgetType is one catalog entry as GET /workspaces/:ws/dashboard-widgets
// returns it. The server knows ids, sizes and the settings schema it enforces;
// titles, descriptions and views are the client's (contract C, §5.8).
type WidgetType struct {
	ID       string    `json:"id"`
	Origin   string    `json:"origin"`
	Category string    `json:"category"`
	Size     SizeRange `json:"size"`
	// Settings is the JSON Schema an instance's settings are checked against;
	// absent means "any JSON object".
	Settings json.RawMessage `json:"settings,omitempty"`
	// ThemeID names the theme of an origin=theme entry.
	ThemeID string `json:"theme_id,omitempty"`
	// Extension carries an origin=extension entry's manifest strings, render
	// and allowed projects (card X1); its fields sit beside the common ones.
	*ExtensionWidget `json:",omitempty"`
}

func size(dw, dh, minW, minH, maxW, maxH int) SizeRange {
	return SizeRange{Default: Cells{dw, dh}, Min: Cells{minW, minH}, Max: Cells{maxW, maxH}}
}

// Metric ids a `metric` tile may show (§5.8, "Аналитика задач").
var metricIDs = []string{
	"tasks_total", "tasks_created", "tasks_closed", "tasks_overdue", "events_total",
	"triage_waiting", "agents_total", "agents_active", "mentions_unseen",
}

func metricSchema() json.RawMessage {
	enum, _ := json.Marshal(metricIDs)
	return json.RawMessage(`{"type":"object","properties":{"metric":{"enum":` + string(enum) + `}}}`)
}

// limitSchema bounds the `limit` of a list widget (§5.8: 5–50); other keys are
// the widget's own and pass.
const limitSchema = `{"type":"object","properties":{"limit":{"type":"integer","minimum":5,"maximum":50}}}`

// agentUsageSchema bounds the statistics widget's view keys (plan a9781091 §4,
// card 250bbe2e): metric m, token kind t, breakdowns g/s, period p (a preset or
// from..to days), quota window w, rows n. f.<key> filter arrays pass unchecked.
const agentUsageSchema = `{"type":"object","properties":{` +
	`"m":{"enum":["cost","tokens","quota","turns","duration","cards_done"]},` +
	`"t":{"enum":["total","in","out","cache_read","cache_write"]},` +
	`"g":{"enum":["model","provider","account","agent","project","workspace"]},` +
	`"s":{"enum":["model","provider","account","agent","project","workspace"]},` +
	`"p":{"type":"string","pattern":"^(1d|7d|30d|[0-9]{4}-[0-9]{2}-[0-9]{2}(\\.\\.[0-9]{4}-[0-9]{2}-[0-9]{2})?)$"},` +
	`"w":{"enum":["5h","week","month","budget"]},` +
	`"n":{"type":"integer","minimum":1,"maximum":20}}}`

// coreTypes is the static core catalog: ids and sizes of §5.8, in its order.
// Extensions add theirs through the same collector later (card X1).
var coreTypes = []WidgetType{
	// Задачи и триаж
	{ID: "my-tasks", Category: "tasks", Size: size(6, 4, 3, 2, 12, 8), Settings: json.RawMessage(limitSchema)},
	{ID: "triage-queue", Category: "tasks", Size: size(6, 4, 3, 2, 12, 10)},
	{ID: "task-list", Category: "tasks", Size: size(6, 4, 3, 2, 12, 10)},
	// Коммуникации
	{ID: "mentions", Category: "communication", Size: size(6, 4, 3, 2, 12, 10), Settings: json.RawMessage(limitSchema)},
	{ID: "my-comments", Category: "communication", Size: size(6, 4, 3, 2, 12, 10)},
	{ID: "recent-comments", Category: "communication", Size: size(6, 4, 3, 2, 12, 10)},
	// Агенты и затраты
	{ID: "agents-active", Category: "agents", Size: size(6, 4, 3, 2, 12, 8)},
	{ID: "agents-status", Category: "agents", Size: size(12, 6, 4, 3, 12, 12)},
	{ID: "agents-summary", Category: "agents", Size: size(4, 2, 3, 1, 6, 2)},
	{ID: "cost-kpis", Category: "agents", Size: size(12, 2, 6, 2, 12, 3)},
	{ID: "cost-by-agent", Category: "agents", Size: size(6, 4, 3, 3, 12, 8)},
	{ID: "cost-top-tasks", Category: "agents", Size: size(6, 4, 3, 3, 12, 8)},
	{ID: "cost-by-project", Category: "agents", Size: size(6, 4, 3, 3, 12, 8)},
	{ID: "cost-by-day", Category: "agents", Size: size(6, 4, 4, 3, 12, 8)},
	{ID: "agent-leaderboard", Category: "agents", Size: size(6, 4, 3, 3, 12, 8)},
	{ID: "agent-usage", Category: "agents", Size: size(12, 6, 6, 4, 12, 12), Settings: json.RawMessage(agentUsageSchema)},
	// Аналитика задач
	{ID: "metric", Category: "analytics", Size: size(3, 2, 2, 2, 6, 3), Settings: metricSchema()},
	{ID: "tasks-breakdown", Category: "analytics", Size: size(6, 4, 4, 3, 12, 8),
		Settings: json.RawMessage(`{"type":"object","properties":{"dimension":{"enum":["status","priority","assignee","project"]},"view":{"enum":["bars","donut"]}}}`)},
	{ID: "tasks-timeline", Category: "analytics", Size: size(12, 4, 6, 3, 12, 8)},
	{ID: "events-breakdown", Category: "analytics", Size: size(6, 4, 4, 3, 12, 8)},
	// Память Mesh (карточка a06aeea9): чтения/записи памяти, полезный recall и качество базы.
	// period и project приходят как у остальных аналитических виджетов и схемой не перечислены;
	// агенты/источники/«кто вызвал» — фильтры шапки виджета, в схеме только их тип.
	{ID: "memory-usage", Category: "analytics", Size: size(12, 10, 6, 4, 12, 12),
		Settings: json.RawMessage(`{"type":"object","properties":{` +
			`"op":{"enum":["all","read","write","recall","recall_with_graph","get_memory","get_project_knowledge","get_project_updates"]},` +
			`"agents":{"type":"array","items":{"type":"string"}},` +
			`"sources":{"type":"array","items":{"enum":["memory","project_knowledge","project_update","doc","unknown"]}},` +
			`"callers":{"type":"array","items":{"enum":["agent_tool","runner","script","ui","unknown"]}},` +
			`"group_by":{"enum":["agent","project","op","source","caller_kind","kind","scope","day"]},` +
			`"group_by2":{"enum":["agent","project","op","source","caller_kind","kind","scope","day"]},` +
			`"view":{"enum":["table","top","chart"]}}}`)},
	// События
	{ID: "event-feed", Category: "events", Size: size(12, 6, 6, 4, 12, 12)},
	// Инициативы
	{ID: "initiatives", Category: "initiatives", Size: size(12, 4, 4, 3, 12, 10)},
	{ID: "initiative", Category: "initiatives", Size: size(6, 3, 4, 2, 12, 6)},
}

var coreByID = func() map[string]WidgetType {
	m := make(map[string]WidgetType, len(coreTypes))
	for _, t := range coreTypes {
		t.Origin = OriginCore
		m[t.ID] = t
	}
	return m
}()

// CoreTypes returns the core catalog in §5.8 order.
func CoreTypes() []WidgetType {
	out := make([]WidgetType, len(coreTypes))
	for i, t := range coreTypes {
		out[i] = coreByID[t.ID]
	}
	return out
}

// IsCoreType reports whether id is a core widget type.
func IsCoreType(id string) bool {
	_, ok := coreByID[id]
	return ok
}

// legacyAliases maps the ids of the pre-dashboards widgets, which a theme's
// `widgets[]` setting may still name, to the core types that replace them.
var legacyAliases = map[string]string{
	"triage":   "triage-queue",
	"sessions": "agents-active",
}

// ThemeWidgetPrefix starts the type id of a widget a theme brings (§6.1:
// "theme.board-studio.focus").
const ThemeWidgetPrefix = "theme."

// ThemeWidgetType is the catalog id of a theme's own widget: the theme's
// bundle names it "<theme-id>.<name>", the catalog stores it as
// "theme.<theme-id>.<name>" so origins never collide.
func ThemeWidgetType(themeID, name string) string {
	return ThemeWidgetPrefix + themeID + "." + name
}

// themeWidgetSize is what the server can say about a theme widget's size: the
// bundle carries the real bounds, the server only keeps the grid.
var themeWidgetSize = size(3, 2, 1, 1, MaxCellSpan, MaxCellSpan)

// Theme is what a workspace's effective theme contributes to dashboards.
type Theme struct {
	ID string
	// Widgets is the theme's `widgets[]` setting: the default composition (§5.3,
	// §5.10). Empty means the four standard widgets.
	Widgets []string
	// OwnWidgets are the names of the theme's own widgets (its
	// dashboard.widget.<name> slots that are not core types).
	OwnWidgets []string
}

// Catalog is the set of widget types one workspace offers.
type Catalog struct {
	types []WidgetType
	byID  map[string]WidgetType
}

// NewCatalog builds the catalog: core types, then the theme's own widgets.
func NewCatalog(theme Theme) *Catalog {
	c := &Catalog{byID: map[string]WidgetType{}}
	for _, t := range CoreTypes() {
		c.add(t)
	}
	names := append([]string(nil), theme.OwnWidgets...)
	sort.Strings(names)
	for _, n := range names {
		if theme.ID == "" || n == "" || IsCoreType(n) || strings.ContainsAny(n, " /") {
			continue
		}
		c.add(WidgetType{ID: ThemeWidgetType(theme.ID, n), Origin: OriginTheme, Category: "theme", Size: themeWidgetSize, ThemeID: theme.ID})
	}
	return c
}

func (c *Catalog) add(t WidgetType) {
	if _, dup := c.byID[t.ID]; dup {
		return
	}
	c.byID[t.ID] = t
	c.types = append(c.types, t)
}

// Types lists the catalog in order.
func (c *Catalog) Types() []WidgetType { return append([]WidgetType(nil), c.types...) }

// Lookup returns the type with this id.
func (c *Catalog) Lookup(id string) (WidgetType, bool) {
	t, ok := c.byID[id]
	return t, ok
}

// normalizeThemeEntry turns one entry of a theme's `widgets[]` setting into a
// catalog id: legacy core ids are renamed, a theme's "<theme-id>.<name>" gets
// the theme prefix, anything else is kept as it is.
func normalizeThemeEntry(themeID, id string) string {
	id = strings.TrimSpace(id)
	if alias, ok := legacyAliases[id]; ok {
		return alias
	}
	if themeID != "" && strings.HasPrefix(id, themeID+".") {
		return ThemeWidgetPrefix + id
	}
	return id
}
