package dashboards

// Template is a starting composition offered by the create dialog (§5.9).
// Templates are code, not data: a new one is a change reviewed like any other.
type Template struct {
	ID      string   `json:"id"`
	Widgets []Widget `json:"widgets"`
}

// Template ids. "empty" creates a dashboard without widgets; "standard" is the
// default composition of §5.3 without a theme.
const (
	TemplateEmpty         = "empty"
	TemplateStandard      = "standard"
	TemplateInitiatives   = "initiatives"
	TemplateTriage        = "triage"
	TemplateActivity      = "activity"
	TemplateAgentSessions = "agent-sessions"
	TemplateAnalytics     = "analytics"
	TemplateEvents        = "events"
)

func metric(id string) Spec {
	return Spec{Type: "metric", W: 3, H: 2, Settings: `{"metric":"` + id + `"}`}
}

// standardSpecs are the four widgets of today's dashboard, 2x2 (§5.3, §5.9).
var standardSpecs = []Spec{
	{Type: "mentions", W: 6, H: 4},
	{Type: "triage-queue", W: 6, H: 4},
	{Type: "my-tasks", W: 6, H: 4},
	{Type: "agents-active", W: 6, H: 4},
}

// templateSpecs is the table of §5.9, row by row in its order.
var templateSpecs = []struct {
	id    string
	specs []Spec
}{
	{TemplateEmpty, nil},
	{TemplateStandard, standardSpecs},
	{TemplateInitiatives, []Spec{{Type: "initiatives", W: 12, H: 8}}},
	{TemplateTriage, []Spec{metric("triage_waiting"), {Type: "triage-queue", W: 12, H: 8}}},
	{TemplateActivity, []Spec{
		{Type: "mentions", W: 6, H: 8},
		{Type: "recent-comments", W: 6, H: 8},
		{Type: "my-comments", W: 12, H: 6},
	}},
	{TemplateAgentSessions, []Spec{
		{Type: "agents-summary", W: 4, H: 2},
		{Type: "cost-kpis", W: 12, H: 2},
		{Type: "agents-status", W: 12, H: 6},
		{Type: "cost-by-agent", W: 6, H: 4},
		{Type: "cost-top-tasks", W: 6, H: 4},
		{Type: "cost-by-day", W: 6, H: 4},
		{Type: "cost-by-project", W: 6, H: 4},
	}},
	{TemplateAnalytics, []Spec{
		metric("tasks_total"), metric("tasks_created"), metric("tasks_closed"), metric("events_total"),
		{Type: "tasks-breakdown", W: 6, H: 4, Settings: `{"dimension":"status"}`},
		{Type: "tasks-breakdown", W: 6, H: 4, Settings: `{"dimension":"priority"}`},
		{Type: "tasks-timeline", W: 12, H: 4},
		{Type: "agent-leaderboard", W: 6, H: 4},
		metric("agents_total"),
		{Type: "events-breakdown", W: 6, H: 4},
		{Type: "cost-by-project", W: 6, H: 4},
	}},
	{TemplateEvents, []Spec{{Type: "event-feed", W: 12, H: 10}}},
}

// Templates lists every template with its packed layout.
func Templates() []Template {
	out := make([]Template, 0, len(templateSpecs))
	for _, t := range templateSpecs {
		out = append(out, Template{ID: t.id, Widgets: Pack(t.specs)})
	}
	return out
}

// TemplateByID returns the template's layout; ok is false for an unknown id.
func TemplateByID(id string) (Template, bool) {
	for _, t := range templateSpecs {
		if t.id == id {
			return Template{ID: t.id, Widgets: Pack(t.specs)}, true
		}
	}
	return Template{}, false
}

// DefaultWidgets is the composition of the default dashboard (§5.3): the
// theme's `widgets[]` in its order when the theme sets it, else the four
// standard widgets. A core type takes its default size; a theme or unknown
// type takes the theme widget size. Repeated entries are dropped, and the list
// stops at the per-dashboard limit.
func DefaultWidgets(theme Theme, cat *Catalog) []Widget {
	if len(theme.Widgets) == 0 {
		return Pack(standardSpecs)
	}
	seen := map[string]bool{}
	var specs []Spec
	for _, raw := range theme.Widgets {
		id := normalizeThemeEntry(theme.ID, raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		sz := themeWidgetSize.Default
		if t, ok := cat.Lookup(id); ok {
			sz = t.Size.Default
		}
		specs = append(specs, Spec{Type: id, W: sz.W, H: sz.H})
		if len(specs) == MaxWidgetsPerBoard {
			break
		}
	}
	if len(specs) == 0 {
		return Pack(standardSpecs)
	}
	return Pack(specs)
}
