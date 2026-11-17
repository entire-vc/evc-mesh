package dashboards

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every template is a valid layout of the core catalog, and the ids are the
// ones the create dialog offers (§5.9).
func TestTemplates_AreValidLayouts(t *testing.T) {
	cat := NewCatalog(Theme{})
	ids := []string{}
	for _, tpl := range Templates() {
		ids = append(ids, tpl.ID)
		require.NoError(t, ValidateLayout(cat, tpl.Widgets), tpl.ID)
		for _, w := range tpl.Widgets {
			_, known := cat.Lookup(w.WidgetType)
			require.True(t, known, "%s uses %s, not in the catalog", tpl.ID, w.WidgetType)
		}
	}
	require.Equal(t, []string{"empty", "standard", "initiatives", "triage", "activity", "agent-sessions", "analytics", "events"}, ids)
	empty, ok := TemplateByID(TemplateEmpty)
	require.True(t, ok)
	require.Empty(t, empty.Widgets)
	_, ok = TemplateByID("nope")
	require.False(t, ok)
}

// Each template is the §5.9 table row: widget types in order with their sizes.
// The old pages redirect to these templates, so a missing row is a lost function.
func TestTemplates_MatchPRDTable(t *testing.T) {
	want := map[string][]string{
		TemplateEmpty:       {},
		TemplateStandard:    {"mentions 6x4", "triage-queue 6x4", "my-tasks 6x4", "agents-active 6x4"},
		TemplateInitiatives: {"initiatives 12x8"},
		TemplateTriage:      {"metric 3x2", "triage-queue 12x8"},
		TemplateActivity:    {"mentions 6x8", "recent-comments 6x8", "my-comments 12x6"},
		TemplateAgentSessions: {
			"agents-summary 4x2", "cost-kpis 12x2", "agents-status 12x6",
			"cost-by-agent 6x4", "cost-top-tasks 6x4", "cost-by-day 6x4", "cost-by-project 6x4",
		},
		TemplateAnalytics: {
			"metric 3x2", "metric 3x2", "metric 3x2", "metric 3x2",
			"tasks-breakdown 6x4", "tasks-breakdown 6x4", "tasks-timeline 12x4",
			"agent-leaderboard 6x4", "metric 3x2", "events-breakdown 6x4", "cost-by-project 6x4",
		},
		TemplateEvents: {"event-feed 12x10"},
	}
	for _, tpl := range Templates() {
		got := []string{}
		for _, w := range tpl.Widgets {
			got = append(got, fmt.Sprintf("%s %dx%d", w.WidgetType, w.W, w.H))
		}
		require.Equal(t, want[tpl.ID], got, tpl.ID)
	}
}

func TestValidateLayout_Refusals(t *testing.T) {
	cat := NewCatalog(Theme{})
	w := func(typ string, x, y, wd, h int, settings string) Widget {
		return Widget{WidgetType: typ, X: x, Y: y, W: wd, H: h, Settings: json.RawMessage(settings)}
	}
	cases := map[string]struct {
		widgets []Widget
		field   string
	}{
		"overlap":         {[]Widget{w("mentions", 0, 0, 6, 4, ""), w("my-tasks", 5, 2, 6, 4, "")}, "widgets[1].position"},
		"past the grid":   {[]Widget{w("mentions", 8, 0, 6, 4, "")}, "widgets[0].position"},
		"below min size":  {[]Widget{w("mentions", 0, 0, 2, 2, "")}, "widgets[0].size"},
		"above max size":  {[]Widget{w("agents-summary", 0, 0, 4, 3, "")}, "widgets[0].size"},
		"settings array":  {[]Widget{w("mentions", 0, 0, 6, 4, `[1]`)}, "widgets[0].settings"},
		"schema: limit":   {[]Widget{w("my-tasks", 0, 0, 6, 4, `{"limit":500}`)}, "widgets[0].settings"},
		"schema: metric":  {[]Widget{w("metric", 0, 0, 3, 2, `{"metric":"nope"}`)}, "widgets[0].settings"},
		"no type":         {[]Widget{w(" ", 0, 0, 6, 4, "")}, "widgets[0].widget_type"},
		"duplicate id":    {[]Widget{{ID: "a", WidgetType: "mentions", W: 6, H: 4}, {ID: "a", WidgetType: "mentions", X: 6, W: 6, H: 4}}, "widgets[1].id"},
		"too many":        {make([]Widget, MaxWidgetsPerBoard+1), "widgets"},
		"negative coords": {[]Widget{w("mentions", -1, 0, 6, 4, "")}, "widgets[0].position"},
	}
	for name, tc := range cases {
		err := ValidateLayout(cat, tc.widgets)
		require.Error(t, err, name)
		var le *LayoutError
		require.ErrorAs(t, err, &le, name)
		require.Contains(t, le.Fields, tc.field, "%s: %v", name, le.Fields)
	}
}

// An unknown type is kept: it may be a widget of an extension that is off now
// (§7.2); it only has to fit the grid.
func TestValidateLayout_UnknownTypeKept(t *testing.T) {
	ws := []Widget{{WidgetType: "ext.other.chart", W: 1, H: 1, Settings: json.RawMessage(` { "a" : 1 } `)}}
	require.NoError(t, ValidateLayout(NewCatalog(Theme{}), ws))
	require.JSONEq(t, `{"a":1}`, string(ws[0].Settings))
	require.Equal(t, `{"a":1}`, string(ws[0].Settings), "settings are stored compact")
}

func TestDefaultWidgets(t *testing.T) {
	std := DefaultWidgets(Theme{}, NewCatalog(Theme{}))
	types := []string{}
	for _, w := range std {
		types = append(types, w.WidgetType)
	}
	require.Equal(t, []string{"mentions", "triage-queue", "my-tasks", "agents-active"}, types)
	require.NoError(t, ValidateLayout(NewCatalog(Theme{}), std))

	theme := Theme{ID: "board-studio", Widgets: []string{"board-studio.focus", "triage", "sessions", "triage", "metric"}, OwnWidgets: []string{"focus"}}
	cat := NewCatalog(theme)
	got := DefaultWidgets(theme, cat)
	types = types[:0]
	for _, w := range got {
		types = append(types, w.WidgetType)
	}
	require.Equal(t, []string{"theme.board-studio.focus", "triage-queue", "agents-active", "metric"}, types,
		"theme order, legacy ids renamed, repeats dropped")
	require.NoError(t, ValidateLayout(cat, got))

	ft, ok := cat.Lookup("theme.board-studio.focus")
	require.True(t, ok)
	require.Equal(t, OriginTheme, ft.Origin)
	require.Equal(t, "board-studio", ft.ThemeID)
}

func TestCatalog_ThemeCannotShadowCore(t *testing.T) {
	cat := NewCatalog(Theme{ID: "t", OwnWidgets: []string{"mentions", "bad name", "own"}})
	n := 0
	for _, wt := range cat.Types() {
		if wt.Origin == OriginTheme {
			n++
			require.Equal(t, "theme.t.own", wt.ID)
		}
	}
	require.Equal(t, 1, n)
	require.Len(t, CoreTypes(), len(coreTypes))
}

// The icon is the shared §5.11 value: one emoji grapheme, icon:<name> or
// empty. Anything else is a 422 on the icon field.
func TestCleanIcon_SharedFormat(t *testing.T) {
	for _, ok := range []string{"", "🚀", "icon:rocket", "icon:folder-kanban", "👨‍👩‍👧‍👦", " 🚦 "} {
		got, verr := cleanIcon(ok)
		require.Nil(t, verr, "%q", ok)
		require.Equal(t, strings.TrimSpace(ok), got)
	}
	for _, bad := range []string{"icon:Rocket!", "icon:", "<svg>", "🚀🚀", "ab", "icon:" + strings.Repeat("a", 41)} {
		_, verr := cleanIcon(bad)
		require.NotNil(t, verr, "%q must be rejected", bad)
		require.Equal(t, 422, verr.Status)
		require.Contains(t, verr.Fields, "icon")
	}
}

// widget_types is the sorted set of types, never null.
func TestDistinctTypes(t *testing.T) {
	require.Equal(t, []string{}, distinctTypes(nil))
	require.Equal(t, []string{"mentions", "my-tasks"}, distinctTypes([]Widget{
		{WidgetType: "my-tasks"}, {WidgetType: "mentions"}, {WidgetType: "my-tasks"}}))
	v := (&Service{}).virtual(context.Background(), Actor{})
	require.Equal(t, distinctTypes(v.Widgets), v.WidgetTypes)
	require.NotEmpty(t, v.WidgetTypes)
}
