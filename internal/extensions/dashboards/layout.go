package dashboards

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Limits of §5.2.
const (
	MaxDashboardsPerUser = 30
	MaxWidgetsPerBoard   = 24
	MaxSettingsBytes     = 16 * 1024
	MaxNameRunes         = 60
	// MaxRow bounds y so a layout cannot push a widget out of any reasonable
	// scroll range; 24 widgets of height 12 stacked fit with room to spare.
	MaxRow = 24 * MaxCellSpan
)

// Widget is one widget instance on a dashboard.
type Widget struct {
	// ID is empty for a new instance and for the widgets of the virtual
	// default dashboard, which has no rows yet.
	ID         string          `json:"id,omitempty"`
	WidgetType string          `json:"widget_type"`
	X          int             `json:"x"`
	Y          int             `json:"y"`
	W          int             `json:"w"`
	H          int             `json:"h"`
	Settings   json.RawMessage `json:"settings"`
}

// LayoutError is a refused layout: one message per offending field, keyed
// "widgets[i].<field>" or "widgets".
type LayoutError struct {
	Fields map[string]string
}

func (e *LayoutError) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for k, v := range e.Fields {
		parts = append(parts, k+": "+v)
	}
	return "invalid layout: " + strings.Join(parts, "; ")
}

func (e *LayoutError) add(key, msg string) {
	if _, ok := e.Fields[key]; !ok {
		e.Fields[key] = msg
	}
}

// NormalizeSettings returns the settings as a compact JSON object ("{}" for
// absent or null) or an error when they are not an object.
func NormalizeSettings(raw json.RawMessage) (json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return json.RawMessage(`{}`), nil
	}
	if raw[0] != '{' || !json.Valid(raw) {
		return nil, fmt.Errorf("must be a JSON object")
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return nil, fmt.Errorf("must be a JSON object")
	}
	return buf.Bytes(), nil
}

// ValidateLayout checks a whole layout against the catalog and §5.2:
//   - at most MaxWidgetsPerBoard widgets;
//   - every widget inside the 12-column grid, a known type within its
//     size.min..size.max; an unknown type is kept (it may belong to an
//     extension that is off right now) and only has to fit the grid;
//   - settings a JSON object of at most MaxSettingsBytes that passes the
//     type's schema, where the type has one;
//   - no two widgets overlap, no instance id twice.
//
// It normalizes Settings in place.
func ValidateLayout(cat *Catalog, widgets []Widget) error {
	e := &LayoutError{Fields: map[string]string{}}
	if len(widgets) > MaxWidgetsPerBoard {
		e.add("widgets", fmt.Sprintf("at most %d widgets per dashboard", MaxWidgetsPerBoard))
		return e
	}
	ids := map[string]int{}
	for i := range widgets {
		w := &widgets[i]
		key := fmt.Sprintf("widgets[%d]", i)
		w.WidgetType = strings.TrimSpace(w.WidgetType)
		if w.WidgetType == "" || len(w.WidgetType) > 128 {
			e.add(key+".widget_type", "required, at most 128 characters")
		}
		if w.ID != "" {
			if j, dup := ids[w.ID]; dup {
				e.add(key+".id", fmt.Sprintf("same id as widgets[%d]", j))
			}
			ids[w.ID] = i
		}
		switch {
		case w.W < 1 || w.W > MaxCellSpan || w.H < 1 || w.H > MaxCellSpan:
			e.add(key+".size", fmt.Sprintf("w and h must be 1..%d", MaxCellSpan))
		case w.X < 0 || w.Y < 0 || w.Y > MaxRow:
			e.add(key+".position", fmt.Sprintf("x and y must be >= 0, y at most %d", MaxRow))
		case w.X > GridColumns-w.W: // not x+w: a huge x would wrap the sum
			e.add(key+".position", fmt.Sprintf("x + w must be at most %d", GridColumns))
		}
		t, known := cat.Lookup(w.WidgetType)
		if known {
			r := t.Size
			if w.W < r.Min.W || w.W > r.Max.W || w.H < r.Min.H || w.H > r.Max.H {
				e.add(key+".size", fmt.Sprintf("%s takes %dx%d..%dx%d", w.WidgetType, r.Min.W, r.Min.H, r.Max.W, r.Max.H))
			}
		}
		settings, err := NormalizeSettings(w.Settings)
		if err != nil {
			e.add(key+".settings", err.Error())
			continue
		}
		w.Settings = settings
		if len(settings) > MaxSettingsBytes {
			e.add(key+".settings", fmt.Sprintf("at most %d KB", MaxSettingsBytes/1024))
			continue
		}
		if known && len(t.Settings) > 0 {
			if err := checkSchema(t.ID, t.Settings, settings); err != nil {
				e.add(key+".settings", err.Error())
			}
		}
	}
	for i := 0; i < len(widgets); i++ {
		for j := i + 1; j < len(widgets); j++ {
			if overlaps(widgets[i], widgets[j]) {
				e.add(fmt.Sprintf("widgets[%d].position", j), fmt.Sprintf("overlaps widgets[%d]", i))
			}
		}
	}
	if len(e.Fields) > 0 {
		return e
	}
	return nil
}

func overlaps(a, b Widget) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

var schemaCache sync.Map // widget type id -> *jsonschema.Schema

func compiled(id string, schema json.RawMessage) (*jsonschema.Schema, error) {
	if s, ok := schemaCache.Load(id); ok {
		return s.(*jsonschema.Schema), nil
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	err = c.AddResource(id+".json", doc)
	if err != nil {
		return nil, err
	}
	s, err := c.Compile(id + ".json")
	if err != nil {
		return nil, err
	}
	schemaCache.Store(id, s)
	return s, nil
}

func checkSchema(id string, schema, settings json.RawMessage) error {
	s, err := compiled(id, schema)
	if err != nil {
		return fmt.Errorf("settings schema of %s: %w", id, err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(settings))
	if err != nil {
		return fmt.Errorf("must be a JSON object")
	}
	if verr := s.Validate(inst); verr != nil {
		msg := verr.Error()
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = strings.TrimSpace(msg[i+1:])
		}
		return fmt.Errorf("does not match the %s settings: %s", id, msg)
	}
	return nil
}

// Spec is a widget to place: a type, a size and settings, no position yet.
type Spec struct {
	Type     string
	W, H     int
	Settings string
}

// Pack places specs row by row on the 12-column grid in the given order: left
// to right, a new row when the next one does not fit. Rows are as tall as
// their tallest widget, so nothing overlaps.
func Pack(specs []Spec) []Widget {
	out := make([]Widget, 0, len(specs))
	x, y, rowH := 0, 0, 0
	for _, s := range specs {
		if x+s.W > GridColumns {
			x, y, rowH = 0, y+rowH, 0
		}
		settings := s.Settings
		if settings == "" {
			settings = "{}"
		}
		out = append(out, Widget{WidgetType: s.Type, X: x, Y: y, W: s.W, H: s.H, Settings: json.RawMessage(settings)})
		x += s.W
		if s.H > rowH {
			rowH = s.H
		}
	}
	return out
}
