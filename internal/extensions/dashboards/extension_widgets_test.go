package dashboards

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/extensions"
)

type fakeWidgetSource struct {
	widgets []extensions.DashboardWidget
	err     error
	gotWS   uuid.UUID
	gotUser uuid.UUID
}

func (f *fakeWidgetSource) DashboardWidgets(_ context.Context, ws, user uuid.UUID) ([]extensions.DashboardWidget, error) {
	f.gotWS, f.gotUser = ws, user
	return f.widgets, f.err
}

func failedPipelinesWidget(projects ...uuid.UUID) extensions.DashboardWidget {
	return extensions.DashboardWidget{
		ID: "gitlab.failed-pipelines", Origin: "extension", ExtensionID: "gitlab", ExtensionName: "GitLab",
		Title: "Failed pipelines of my MRs", TitleI18n: map[string]string{"ru": "Упавшие пайплайны моих MR"},
		Category: "integrations",
		Size:     extensions.WidgetSize{Default: extensions.WidgetCells{W: 6, H: 4}, Min: &extensions.WidgetCells{W: 4, H: 3}},
		Settings: json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer","minimum":1,"maximum":20}}}`),
		Scope:    "workspace", Render: extensions.WidgetRender{Builtin: "gitlab.failed-pipelines"},
		ProjectIDs: projects,
	}
}

func TestAddExtensionWidgets_AppendsAfterCoreWithManifestFields(t *testing.T) {
	p := uuid.New()
	c := NewCatalog(Theme{})
	c.AddExtensionWidgets([]extensions.DashboardWidget{failedPipelinesWidget(p)})

	types := c.Types()
	last := types[len(types)-1]
	if len(types) != len(CoreTypes())+1 || last.ID != "gitlab.failed-pipelines" {
		t.Fatalf("extension widget not appended after core: %d types, last %q", len(types), last.ID)
	}
	if last.Origin != OriginExtension || last.ExtensionWidget == nil || last.ExtensionID != "gitlab" {
		t.Fatalf("origin/extension part lost: %+v", last)
	}
	if last.Size != (SizeRange{Default: Cells{6, 4}, Min: Cells{4, 3}, Max: Cells{MaxCellSpan, MaxCellSpan}}) {
		t.Fatalf("size: %+v", last.Size)
	}

	raw, err := json.Marshal(last)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	for _, k := range []string{"id", "origin", "category", "size", "settings", "extension_id", "title", "title_i18n", "source", "scope", "render", "project_ids"} {
		if _, ok := got[k]; !ok {
			t.Errorf("JSON of an extension entry lacks %q: %s", k, raw)
		}
	}
	if src, _ := got["source"].(map[string]any); src["kind"] != "extension" || src["name"] != "GitLab" {
		t.Errorf("source = %v", got["source"])
	}

	core, _ := json.Marshal(types[0])
	if strings.Contains(string(core), "extension_id") || strings.Contains(string(core), `"source"`) {
		t.Errorf("core entry grew extension fields: %s", core)
	}
}

func TestAddExtensionWidgets_NeverShadowsCoreOrThemeTypes(t *testing.T) {
	c := NewCatalog(Theme{ID: "studio", OwnWidgets: []string{"focus"}})
	before := len(c.Types())
	c.AddExtensionWidgets([]extensions.DashboardWidget{
		{ID: "my-tasks", ExtensionID: "gitlab"},                  // core id
		{ID: "theme.studio.focus", ExtensionID: "theme"},         // theme id
		{ID: "other.widget", ExtensionID: "gitlab"},              // not its own prefix
		{ID: "gitlab.x", ExtensionID: ""},                        // no extension
		{ID: "gitlab.ok", ExtensionID: "gitlab", Title: "ok"},    // kept
		{ID: "gitlab.ok", ExtensionID: "gitlab", Title: "twice"}, // duplicate, first wins
	})
	types := c.Types()
	if len(types) != before+1 {
		t.Fatalf("want exactly one extension entry added, got %d", len(types)-before)
	}
	if mt, _ := c.Lookup("my-tasks"); mt.Origin != OriginCore {
		t.Fatalf("core my-tasks changed origin to %q", mt.Origin)
	}
	if ok, _ := c.Lookup("gitlab.ok"); ok.Title != "ok" {
		t.Fatalf("duplicate replaced the first entry: %q", ok.Title)
	}
}

func TestServiceCatalog_IncludesExtensionWidgetsForTheCaller(t *testing.T) {
	src := &fakeWidgetSource{widgets: []extensions.DashboardWidget{failedPipelinesWidget()}}
	s := NewService(nil, nil, nil).WithExtensionWidgets(src)
	a := Actor{UserID: uuid.New(), WorkspaceID: uuid.New()}

	types := s.Catalog(context.Background(), a)
	if types[len(types)-1].ID != "gitlab.failed-pipelines" {
		t.Fatalf("catalog has no extension widget: last %q", types[len(types)-1].ID)
	}
	if src.gotWS != a.WorkspaceID || src.gotUser != a.UserID {
		t.Fatalf("source asked for ws %s user %s, want %s %s", src.gotWS, src.gotUser, a.WorkspaceID, a.UserID)
	}
}

func TestServiceCatalog_SourceErrorLeavesCoreCatalog(t *testing.T) {
	s := NewService(nil, nil, nil).WithExtensionWidgets(&fakeWidgetSource{err: errors.New("registry down")})
	types := s.Catalog(context.Background(), Actor{UserID: uuid.New(), WorkspaceID: uuid.New()})
	if len(types) != len(CoreTypes()) {
		t.Fatalf("want the core catalog only, got %d types", len(types))
	}
}

func TestServiceCatalog_WithoutSourceIsCoreOnly(t *testing.T) {
	types := NewService(nil, nil, nil).Catalog(context.Background(), Actor{UserID: uuid.New(), WorkspaceID: uuid.New()})
	if len(types) != len(CoreTypes()) {
		t.Fatalf("want %d core types, got %d", len(CoreTypes()), len(types))
	}
}

func TestValidateLayout_EnforcesExtensionWidgetBounds(t *testing.T) {
	c := NewCatalog(Theme{})
	c.AddExtensionWidgets([]extensions.DashboardWidget{failedPipelinesWidget()})

	small := []Widget{{WidgetType: "gitlab.failed-pipelines", W: 2, H: 2}}
	if err := ValidateLayout(c, small); err == nil || !strings.Contains(err.Error(), "takes 4x3") {
		t.Fatalf("a widget below the manifest min must be refused, got %v", err)
	}
	badSettings := []Widget{{WidgetType: "gitlab.failed-pipelines", W: 6, H: 4, Settings: json.RawMessage(`{"limit":100}`)}}
	if err := ValidateLayout(c, badSettings); err == nil {
		t.Fatal("settings outside the manifest schema must be refused")
	}
	ok := []Widget{{WidgetType: "gitlab.failed-pipelines", W: 6, H: 4, Settings: json.RawMessage(`{"limit":5}`)}}
	if err := ValidateLayout(c, ok); err != nil {
		t.Fatalf("a fitting instance was refused: %v", err)
	}
	// Extension off: the type leaves the catalog, the saved instance stays.
	if err := ValidateLayout(NewCatalog(Theme{}), small); err != nil {
		t.Fatalf("an instance of an absent extension widget must be kept, got %v", err)
	}
}
