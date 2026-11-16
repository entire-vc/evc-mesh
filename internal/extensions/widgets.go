package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"

	"github.com/google/uuid"
)

// WidgetSlot is the UI slot an extension declares to offer dashboard widgets.
const WidgetSlot = "dashboard.widget"

// widgetCapability is the slot in the flat "kind:value" form consent is
// recorded in (see DeclaredCapabilities).
const widgetCapability = "ui:" + WidgetSlot

// Widget scopes (manifest widgets[].scope).
const (
	WidgetScopeWorkspace = "workspace"
	WidgetScopeProject   = "project"
	WidgetScopeUser      = "user"
)

// Widget is one entry of the manifest's widgets[]: a widget type the extension
// offers dashboards (PRD Dashboards §7.2).
type Widget struct {
	ID              string            `json:"id"`
	Title           string            `json:"title"`
	TitleI18n       map[string]string `json:"title_i18n,omitempty"`
	Description     string            `json:"description,omitempty"`
	DescriptionI18n map[string]string `json:"description_i18n,omitempty"`
	Category        string            `json:"category"`
	Size            WidgetSize        `json:"size"`
	// Settings is the JSON Schema of an instance's settings (drawn by SchemaForm).
	Settings json.RawMessage `json:"settings,omitempty"`
	Requires []string        `json:"requires,omitempty"`
	Scope    string          `json:"scope"`
	Render   WidgetRender    `json:"render"`
}

// WidgetSize is a widget's size in cells of the 12-column dashboard grid.
type WidgetSize struct {
	Default WidgetCells  `json:"default"`
	Min     *WidgetCells `json:"min,omitempty"`
	Max     *WidgetCells `json:"max,omitempty"`
}

// WidgetCells is a width and height in grid cells.
type WidgetCells struct {
	W int `json:"w"`
	H int `json:"h"`
}

// WidgetRender says what draws the widget: a component of the Mesh bundle
// (builtin extensions) or an iframe screen of the service (external ones).
type WidgetRender struct {
	Builtin string       `json:"builtin,omitempty"`
	Frame   *WidgetFrame `json:"frame,omitempty"`
}

// WidgetFrame is the iframe screen of an external extension's widget.
type WidgetFrame struct {
	Path        string `json:"path"`
	MinRefreshS int    `json:"min_refresh_s,omitempty"`
}

// WidgetType is the full id of a widget type in the dashboard catalog:
// "<ext_id>.<widget_id>". Core widget ids have no dot, so they never collide.
func WidgetType(extensionID, widgetID string) string { return extensionID + "." + widgetID }

// checkWidgets enforces what the JSON Schema cannot say: widget ids are unique
// within the manifest, and no settings field of a widget, at any depth, is
// x-secret (an instance's settings belong to whoever placed it on a dashboard
// and are readable by everyone who sees that dashboard).
func checkWidgets(ws []Widget) error {
	seen := map[string]bool{}
	for _, w := range ws {
		if seen[w.ID] {
			return fmt.Errorf("widgets: duplicate id %q", w.ID)
		}
		seen[w.ID] = true
		if len(w.Settings) == 0 {
			continue
		}
		var doc any
		if err := json.Unmarshal(w.Settings, &doc); err != nil {
			return fmt.Errorf("widgets[%s].settings: %w", w.ID, err)
		}
		if hasKey(doc, "x-secret") {
			return fmt.Errorf("widgets[%s].settings: x-secret is not allowed in widget settings", w.ID)
		}
	}
	return nil
}

func hasKey(v any, key string) bool {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if k == key || hasKey(child, key) {
				return true
			}
		}
	case []any:
		for _, child := range t {
			if hasKey(child, key) {
				return true
			}
		}
	}
	return false
}

// DashboardWidget is one extension widget type in the catalog of
// GET /workspaces/:ws/dashboard-widgets, in the shape of the SDK's
// WidgetDescriptor v2 plus the extension it comes from.
type DashboardWidget struct {
	ID              string            `json:"id"`
	Origin          string            `json:"origin"`
	ExtensionID     string            `json:"extension_id"`
	ExtensionName   string            `json:"extension_name"`
	Title           string            `json:"title"`
	TitleI18n       map[string]string `json:"title_i18n,omitempty"`
	Description     string            `json:"description,omitempty"`
	DescriptionI18n map[string]string `json:"description_i18n,omitempty"`
	Category        string            `json:"category"`
	Size            WidgetSize        `json:"size"`
	Settings        json.RawMessage   `json:"settings,omitempty"`
	Requires        []string          `json:"requires"`
	Scope           string            `json:"scope"`
	Render          WidgetRender      `json:"render"`
	// ProjectIDs, for scope "project" only, are the projects the widget may be
	// set to: the extension is bound to them and the viewer may open them.
	ProjectIDs []uuid.UUID `json:"project_ids,omitempty"`
}

// DashboardWidgetSource is what the dashboard catalog asks for the widget
// types of extensions available to one viewer in one workspace.
type DashboardWidgetSource interface {
	DashboardWidgets(ctx context.Context, workspaceID, userID uuid.UUID) ([]DashboardWidget, error)
}

// WidgetProjects lists, for a scope "project" widget, the projects of a
// workspace that are bound to the extension (an enabled project_integrations
// row) and that the user may open.
type WidgetProjects interface {
	WidgetProjects(ctx context.Context, workspaceID uuid.UUID, extensionID string, userID uuid.UUID) ([]uuid.UUID, error)
}

// DashboardWidgets is the DashboardWidgetSource over the extension registry.
type DashboardWidgets struct {
	reg      *Registry
	projects WidgetProjects
	log      *slog.Logger
}

// NewDashboardWidgets returns the catalog source. projects may be nil only
// where no extension declares a scope "project" widget: such widgets are then
// left out (nobody can be shown a project they may not open).
func NewDashboardWidgets(reg *Registry, projects WidgetProjects) *DashboardWidgets {
	return &DashboardWidgets{reg: reg, projects: projects, log: slog.Default()}
}

// DashboardWidgets returns the widget types of the extensions that are enabled
// in the workspace, declare the dashboard.widget slot and have it consented
// to, ordered by extension and manifest order. An extension whose state cannot
// be read right now is left out with a log line instead of failing the whole
// catalog; its widgets on dashboards show the stub until it answers again.
func (d *DashboardWidgets) DashboardWidgets(ctx context.Context, workspaceID, userID uuid.UUID) ([]DashboardWidget, error) {
	out := []DashboardWidget{}
	for _, p := range d.reg.List() {
		m := p.Manifest()
		if len(m.Widgets) == 0 || !slices.Contains(m.Capabilities.UI, WidgetSlot) {
			continue
		}
		ok, err := d.reg.WidgetsAvailable(ctx, workspaceID, m.ID)
		if err != nil {
			d.log.WarnContext(ctx, "dashboard widgets: extension state unavailable", "extension", m.ID, "workspace", workspaceID, "error", err)
			continue
		}
		if !ok {
			continue
		}
		var projects []uuid.UUID
		projectsRead := false
		for _, w := range m.Widgets {
			item := DashboardWidget{
				ID: WidgetType(m.ID, w.ID), Origin: "extension", ExtensionID: m.ID, ExtensionName: m.Name,
				Title: w.Title, TitleI18n: w.TitleI18n, Description: w.Description, DescriptionI18n: w.DescriptionI18n,
				Category: w.Category, Size: w.Size, Settings: w.Settings, Requires: append([]string{}, w.Requires...),
				Scope: w.Scope, Render: w.Render,
			}
			if w.Scope == WidgetScopeProject {
				if d.projects == nil {
					continue
				}
				if !projectsRead {
					projects, err = d.projects.WidgetProjects(ctx, workspaceID, m.ID, userID)
					if err != nil {
						return nil, fmt.Errorf("dashboard widgets %q: projects: %w", m.ID, err)
					}
					projectsRead = true
				}
				if len(projects) == 0 {
					continue
				}
				item.ProjectIDs = projects
			}
			out = append(out, item)
		}
	}
	return out, nil
}

// WidgetsAvailable reports whether the extension's dashboard widgets may be
// offered in the workspace: it is enabled there (the provider's Resolve, the
// same answer ResolveStatus gives, without a health check) and its
// dashboard.widget slot is consented to. A workspace connection must carry the
// slot in its recorded consent, so a release that adds the slot waits for the
// administrator's re-consent; an instance-level connection of a builtin is
// governed by the instance administrator, who has no per-workspace consent.
func (r *Registry) WidgetsAvailable(ctx context.Context, workspaceID uuid.UUID, extensionID string) (bool, error) {
	p, ok := r.Get(extensionID)
	if !ok {
		return false, nil
	}
	m := p.Manifest()
	if !slices.Contains(m.Capabilities.UI, WidgetSlot) {
		return false, nil
	}
	res, err := p.Resolve(ctx, workspaceID)
	if err != nil {
		return false, fmt.Errorf("resolve %q: %w", extensionID, err)
	}
	if !res.Enabled {
		return false, nil
	}
	r.mu.RLock()
	consents := r.consents
	r.mu.RUnlock()
	if consents == nil || res.Source != SourceWorkspace {
		return true, nil
	}
	caps, err := consents.Consented(ctx, workspaceID, extensionID)
	if err != nil {
		return false, fmt.Errorf("consents %q: %w", extensionID, err)
	}
	return slices.Contains(caps, widgetCapability), nil
}
