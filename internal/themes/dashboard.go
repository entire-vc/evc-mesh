package themes

import (
	"context"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// DashboardWidgetSlotPrefix starts the slot of a widget view a theme ships
// ("dashboard.widget.<name>").
const DashboardWidgetSlotPrefix = "dashboard.widget."

// DashboardContribution is what the workspace's effective theme brings to the
// dashboards: the default composition (`widgets[]` setting) and the names of
// the widget views it declares as dashboard.widget.<name> slots.
type DashboardContribution struct {
	ThemeID     string
	Widgets     []string
	WidgetSlots []string
}

// Dashboard resolves the workspace theme and reads its dashboard parts. The
// mesh-default theme contributes nothing.
func (s *Service) Dashboard(ctx context.Context, workspaceID uuid.UUID) (DashboardContribution, error) {
	r, err := s.Resolve(ctx, &workspaceID)
	if err != nil || r == nil || r.Source == "default" {
		return DashboardContribution{}, err
	}
	out := DashboardContribution{ThemeID: r.ThemeID, Widgets: stringList(r.Settings["widgets"])}
	t, err := s.load(ctx, r.ThemeID)
	if err != nil || t == nil {
		return out, err
	}
	for slot := range t.spec.Slots {
		if name, ok := strings.CutPrefix(slot, DashboardWidgetSlotPrefix); ok && name != "" {
			out.WidgetSlots = append(out.WidgetSlots, name)
		}
	}
	sort.Strings(out.WidgetSlots)
	return out, nil
}

// stringList reads a JSON array of strings; anything else is empty.
func stringList(v any) []string {
	arr, _ := v.([]any)
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}
