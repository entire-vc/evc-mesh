package themes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/entire-vc/evc-mesh/internal/extensions"
)

// Settings levels a theme can declare (manifest settings.<level>).
const (
	LevelInstance  = "instance"
	LevelWorkspace = "workspace"
)

// levelSchema returns the JSON Schema of one settings level, following a
// "#/schemas/<name>" reference. ok is false when the theme declares none.
func levelSchema(m extensions.Manifest, level string) (map[string]any, bool) {
	var settings map[string]json.RawMessage
	if len(m.Settings) == 0 || json.Unmarshal(m.Settings, &settings) != nil {
		return nil, false
	}
	raw, ok := settings[level]
	if !ok {
		return nil, false
	}
	var lv map[string]any
	if json.Unmarshal(raw, &lv) != nil {
		return nil, false
	}
	if ref, _ := lv["$ref"].(string); ref != "" {
		name, found := strings.CutPrefix(ref, "#/schemas/")
		if !found {
			return nil, false
		}
		var schemas map[string]map[string]any
		if json.Unmarshal(m.Schemas, &schemas) != nil || schemas[name] == nil {
			return nil, false
		}
		lv = withSiblings(schemas[name], lv)
	}
	return lv, true
}

// withSiblings applies the keywords written next to a "$ref" together with the
// referenced schema, as Draft 2020-12 does: every keyword constrains, none
// replaces another. "properties" are united (a field named by both takes the
// sibling's definition), "required" lists are united, "additionalProperties"
// is false when either side says false, and any other keyword both sides
// carry is kept as a separate allOf branch so neither is lost.
func withSiblings(ref, level map[string]any) map[string]any {
	out := make(map[string]any, len(ref)+len(level))
	for k, v := range ref {
		out[k] = v
	}
	for k, v := range level {
		prev, both := out[k]
		switch {
		case k == "$ref":
		case !both:
			out[k] = v
		case k == "properties":
			base, _ := prev.(map[string]any)
			over, _ := v.(map[string]any)
			merged := make(map[string]any, len(base)+len(over))
			for n, f := range base {
				merged[n] = f
			}
			for n, f := range over {
				merged[n] = f
			}
			out[k] = merged
		case k == "required":
			seen := map[any]bool{}
			var names []any
			for _, list := range []any{prev, v} {
				items, _ := list.([]any)
				for _, n := range items {
					if !seen[n] {
						seen[n] = true
						names = append(names, n)
					}
				}
			}
			out[k] = names
		case k == "additionalProperties":
			if prev == false || v == false {
				out[k] = false
			} else {
				out[k] = v
			}
		default:
			branches, _ := out["allOf"].([]any)
			out["allOf"] = append(branches, map[string]any{k: v})
		}
	}
	return out
}

// LevelSchema is levelSchema for callers that want the document itself (the
// form of GET /themes); nil when the level is not declared.
func LevelSchema(m extensions.Manifest, level string) map[string]any {
	lv, _ := levelSchema(m, level)
	return lv
}

func properties(lv map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	props, _ := lv["properties"].(map[string]any)
	for k, v := range props {
		if f, ok := v.(map[string]any); ok {
			out[k] = f
		}
	}
	return out
}

// bindings maps a settings field to the token it drives ("x-token"). Fields
// of both levels are read; a field name keeps one binding.
func bindings(m extensions.Manifest) (bound, bad map[string]string) {
	bound, bad = map[string]string{}, map[string]string{}
	for _, level := range []string{LevelInstance, LevelWorkspace} {
		lv, ok := levelSchema(m, level)
		if !ok {
			continue
		}
		for name, f := range properties(lv) {
			tok, has := f["x-token"].(string)
			if !has {
				continue
			}
			key := "settings." + level + "." + name + ".x-token"
			switch {
			case !IsToken(tok):
				bad[key] = fmt.Sprintf("%q is not a token of contract v1", tok)
			case f["type"] != "string":
				bad[key] = "a field bound to a token must be a string"
			default:
				// Effective settings are flattened by field name across the two
				// levels, so one name can drive only one token.
				if prev, dup := bound[name]; dup && prev != tok {
					bad[key] = fmt.Sprintf("field %q is bound to %q at another level; one name drives one token", name, prev)
					continue
				}
				bound[name] = tok
			}
		}
	}
	return bound, bad
}

// ValidateSettings checks values against the level's schema; a field the
// schema does not name is an error. nil when the values are acceptable. A
// theme that declares no such level accepts only an empty object.
func ValidateSettings(m extensions.Manifest, level string, values map[string]any) map[string]string {
	lv, ok := levelSchema(m, level)
	if !ok {
		if len(values) == 0 {
			return nil
		}
		fields := map[string]string{}
		for k := range values {
			fields[k] = "theme declares no " + level + " settings"
		}
		return fields
	}
	doc := map[string]any{}
	for k, v := range lv {
		doc[k] = v
	}
	doc["type"] = "object"
	if _, set := doc["additionalProperties"]; !set {
		doc["additionalProperties"] = false
	}
	raw, _ := json.Marshal(doc)
	parsed, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return map[string]string{"settings": "theme settings schema is not readable"}
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err = c.AddResource("settings.json", parsed); err != nil {
		return map[string]string{"settings": "theme settings schema is not usable"}
	}
	sch, err := c.Compile("settings.json")
	if err != nil {
		return map[string]string{"settings": "theme settings schema is not usable"}
	}
	instRaw, _ := json.Marshal(values)
	inst, _ := jsonschema.UnmarshalJSON(bytes.NewReader(instRaw))
	verr, isVal := sch.Validate(inst).(*jsonschema.ValidationError)
	if !isVal {
		return nil
	}
	fields := map[string]string{}
	var walk func(u *jsonschema.OutputUnit)
	walk = func(u *jsonschema.OutputUnit) {
		if len(u.Errors) == 0 && u.Error != nil {
			loc := strings.TrimPrefix(u.InstanceLocation, "/")
			if loc == "" {
				loc = "settings"
			}
			if _, dup := fields[loc]; !dup {
				fields[loc] = u.Error.String()
			}
		}
		for i := range u.Errors {
			walk(&u.Errors[i])
		}
	}
	out := verr.DetailedOutput()
	walk(out)
	if len(fields) == 0 {
		fields["settings"] = "do not match the theme's schema"
	}
	return fields
}

// defaults collects the "default" of every field of a level.
func defaults(m extensions.Manifest, level string) map[string]any {
	out := map[string]any{}
	if lv, ok := levelSchema(m, level); ok {
		for name, f := range properties(lv) {
			if d, has := f["default"]; has {
				out[name] = d
			}
		}
	}
	return out
}

// effectiveSettings is defaults ← instance values (when they belong to this
// theme) ← workspace values (ADR D3).
func effectiveSettings(m extensions.Manifest, instance, workspace map[string]any) map[string]any {
	out := defaults(m, LevelInstance)
	for k, v := range defaults(m, LevelWorkspace) {
		out[k] = v
	}
	for _, layer := range []map[string]any{instance, workspace} {
		for k, v := range layer {
			out[k] = v
		}
	}
	return out
}

// tokenOverrides turns the effective values of token-bound fields into
// token → value, skipping values the token would not accept.
func tokenOverrides(m extensions.Manifest, settings map[string]any) map[string]string {
	bound, _ := bindings(m)
	out := map[string]string{}
	names := make([]string, 0, len(bound))
	for field := range bound {
		names = append(names, field)
	}
	sort.Strings(names)
	for _, field := range names {
		if v, ok := settings[field].(string); ok && checkTokenValue(bound[field], v) {
			out[bound[field]] = v
		}
	}
	return out
}
