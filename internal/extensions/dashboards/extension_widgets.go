package dashboards

import (
	"context"
	"log"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/extensions"
)

// WidgetSource names who offers a catalog entry: the extension (or theme) and
// its display name. Core entries leave it out.
type WidgetSource struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// ExtensionWidget is the part of a catalog entry only an extension widget has:
// the client has no strings or views of its own for it, so the server passes
// the manifest's.
type ExtensionWidget struct {
	ExtensionID     string                  `json:"extension_id"`
	Title           string                  `json:"title"`
	TitleI18n       map[string]string       `json:"title_i18n,omitempty"`
	Description     string                  `json:"description,omitempty"`
	DescriptionI18n map[string]string       `json:"description_i18n,omitempty"`
	Source          WidgetSource            `json:"source"`
	Requires        []string                `json:"requires,omitempty"`
	Scope           string                  `json:"scope"`
	Render          extensions.WidgetRender `json:"render"`
	// ProjectIDs, for scope "project", are the projects the viewer may set it to.
	ProjectIDs []uuid.UUID `json:"project_ids,omitempty"`
}

// extensionWidgetSize fills the bounds a manifest left out: from one cell up to
// the whole grid, never below the default.
func extensionWidgetSize(s extensions.WidgetSize) SizeRange {
	r := SizeRange{Default: Cells{s.Default.W, s.Default.H}, Min: Cells{1, 1}, Max: Cells{MaxCellSpan, MaxCellSpan}}
	if s.Min != nil {
		r.Min = Cells{s.Min.W, s.Min.H}
	}
	if s.Max != nil {
		r.Max = Cells{s.Max.W, s.Max.H}
	}
	return r
}

// AddExtensionWidgets appends the widget types of extensions (card X1) after
// the core and theme ones. An id that is already taken, or that is not
// "<extension>.<widget>", is skipped: a core or theme type never changes meaning.
func (c *Catalog) AddExtensionWidgets(ws []extensions.DashboardWidget) {
	for i := range ws {
		w := &ws[i]
		if w.ExtensionID == "" || !strings.HasPrefix(w.ID, w.ExtensionID+".") || strings.HasPrefix(w.ID, ThemeWidgetPrefix) {
			continue
		}
		c.add(WidgetType{
			ID:       w.ID,
			Origin:   OriginExtension,
			Category: w.Category,
			Size:     extensionWidgetSize(w.Size),
			Settings: w.Settings,
			ExtensionWidget: &ExtensionWidget{
				ExtensionID: w.ExtensionID, Title: w.Title, TitleI18n: w.TitleI18n,
				Description: w.Description, DescriptionI18n: w.DescriptionI18n,
				Source:   WidgetSource{Kind: OriginExtension, Name: w.ExtensionName},
				Requires: w.Requires, Scope: w.Scope, Render: w.Render, ProjectIDs: w.ProjectIDs,
			},
		})
	}
}

// WithExtensionWidgets makes the catalog offer the widgets of the extensions
// enabled for the workspace (extensions.NewDashboardWidgets).
func (s *Service) WithExtensionWidgets(src extensions.DashboardWidgetSource) *Service {
	s.extWidgets = src
	return s
}

// catalog is the workspace's catalog for this caller. Extension widgets that
// cannot be read right now are left out with a log line: the home screen still
// opens, and a saved instance of one stays (an unknown widget_type is kept).
func (s *Service) catalog(ctx context.Context, a Actor) *Catalog {
	cat := NewCatalog(s.theme(ctx, a.WorkspaceID))
	if s.extWidgets == nil {
		return cat
	}
	ws, err := s.extWidgets.DashboardWidgets(ctx, a.WorkspaceID, a.UserID)
	if err != nil {
		log.Printf("[dashboards] extension widgets of workspace %s: %v (left out of the catalog)", a.WorkspaceID, err)
		return cat
	}
	cat.AddExtensionWidgets(ws)
	return cat
}
