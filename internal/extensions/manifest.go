package extensions

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Manifest is the parsed mesh-extension.json (schema/manifest.v1.json). Only
// the fields the core reads are typed; the full document stays in Raw so it
// can be stored in installed_extensions verbatim.
type Manifest struct {
	Schema       int             `json:"schema"`
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Version      string          `json:"version"`
	Kind         string          `json:"kind"`
	Runtime      string          `json:"runtime"`
	MeshAPI      string          `json:"mesh_api"`
	Publisher    string          `json:"publisher"`
	Description  string          `json:"description"`
	Category     string          `json:"category"`
	Levels       Levels          `json:"levels"`
	Settings     json.RawMessage `json:"settings,omitempty"`
	Schemas      json.RawMessage `json:"schemas,omitempty"`
	Capabilities Capabilities    `json:"capabilities"`
	External     *External       `json:"external,omitempty"`
	// Requires lists the ids of extensions this one needs enabled in the same project.
	Requires []string `json:"requires,omitempty"`
	// ProjectView describes the project tab of the project.view slot.
	ProjectView *ProjectView `json:"project_view,omitempty"`
	// Theme is the block of a kind=theme extension, decoded by internal/themes.
	Theme json.RawMessage `json:"theme,omitempty"`

	Raw json.RawMessage `json:"-"`
}

// ProjectView is the manifest block of the project.view slot: a tab in the
// project, served under /w/:ws/p/:proj/<ext>/*.
type ProjectView struct {
	Icon  string `json:"icon"`
	Title string `json:"title"`
	// After is the id of the tab this one follows.
	After string `json:"after,omitempty"`
	// VisibleIf is the visibility condition, in the x-visible-if form.
	VisibleIf map[string]any `json:"visible_if,omitempty"`
}

// External is the manifest block of a runtime=external extension: where the
// service lives (a workspace setting holds its base URL) and its endpoints.
type External struct {
	BaseURLSetting string `json:"base_url_setting"`
	HealthPath     string `json:"health_path"`
	InstallPath    string `json:"install_path"`
	EventsPath     string `json:"events_path"`
	// TokenPath, when set, is where Mesh delivers the extension actor's token
	// itself (signed POST at install and on rotate-token), so the token never
	// passes through a person. Empty keeps the token in the rotate response.
	TokenPath string `json:"token_path,omitempty"`
	// SignatureVersion selects the signature of event deliveries to this
	// service: 0 or 1 is the documented "{timestamp}.{body}" HMAC, 2 also
	// authenticates delivery id and event type (internal/extensions/extsig).
	// Mesh tells the service at install; opt-in, v1 stays the default.
	SignatureVersion int `json:"signature_version,omitempty"`
	// UI maps a slot (sidebar.item, task.panel) to the screen the service serves
	// for it; the screen is shown in an iframe on the service's own origin.
	UI map[string]ExternalUI `json:"ui,omitempty"`
}

// ExternalUI is one iframe screen of an external extension.
type ExternalUI struct {
	Path  string `json:"path"`
	Title string `json:"title"`
	Icon  string `json:"icon,omitempty"`
}

// Levels says at which scopes the extension has settings.
type Levels struct {
	Instance  bool `json:"instance"`
	Workspace bool `json:"workspace"`
	Project   bool `json:"project"`
}

// Capabilities is the permission set the extension declares.
type Capabilities struct {
	API        []string `json:"api"`
	Events     []string `json:"events"`
	WebhooksIn []string `json:"webhooks_in"`
	UI         []string `json:"ui"`
	Secrets    []string `json:"secrets"`
	Network    *Network `json:"network,omitempty"`
}

// Network is the set of hosts the extension may talk to; iframe screens may
// only be served from these.
type Network struct {
	AllowedDomains []string `json:"allowed_domains"`
}

// ParseManifest decodes a manifest and checks the fields the registry relies
// on. Full structural validation is the JSON Schema's job (schema package).
func ParseManifest(raw []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	if m.ID == "" || m.Version == "" || m.Runtime == "" {
		return Manifest{}, fmt.Errorf("parse manifest: id, version and runtime are required")
	}
	m.Raw = append(json.RawMessage(nil), raw...)
	return m, nil
}

// DecodeSettings converts a settings map into a typed struct.
func DecodeSettings[T any](settings map[string]any) (T, error) {
	var out T
	b, err := json.Marshal(settings)
	if err != nil {
		return out, fmt.Errorf("encode settings: %w", err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, fmt.Errorf("decode settings: %w", err)
	}
	return out, nil
}

// InstanceSecretKeys returns the instance-level settings fields the manifest
// marks x-secret. The instance level may be inline or a "#/schemas/<name>" ref.
func (m Manifest) InstanceSecretKeys() []string { return m.SecretKeys("instance") }

// HasInstanceFields reports whether the instance level declares at least one
// setting, so an empty schema ({"properties":{}}) gets no form on the admin page.
func (m Manifest) HasInstanceFields() bool { return len(m.SettingFields("instance")) > 0 }

// SettingField is what the manifest says about one settings field.
type SettingField struct {
	Secret   bool
	ReadOnly bool
}

// SettingFields returns the fields of one settings level ("instance",
// "workspace" or "project") the manifest declares, with their x-secret and
// readOnly flags. The level may be inline or a "#/schemas/<name>" ref. nil when
// the level is not declared.
func (m Manifest) SettingFields(levelName string) map[string]SettingField {
	type field struct {
		Secret   bool `json:"x-secret"`
		ReadOnly bool `json:"readOnly"`
	}
	type level struct {
		Ref        string           `json:"$ref"`
		Properties map[string]field `json:"properties"`
	}
	var settings map[string]*level
	if len(m.Settings) == 0 || json.Unmarshal(m.Settings, &settings) != nil || settings[levelName] == nil {
		return nil
	}
	lv := *settings[levelName]
	if name, ok := strings.CutPrefix(lv.Ref, "#/schemas/"); ok {
		var schemas map[string]level
		if json.Unmarshal(m.Schemas, &schemas) != nil {
			return nil
		}
		lv = schemas[name]
	}
	out := make(map[string]SettingField, len(lv.Properties))
	for k, f := range lv.Properties {
		out[k] = SettingField(f)
	}
	return out
}

// SettingsSchema returns the JSON Schema the manifest declares for one settings
// level ("instance", "workspace" or "project"), nil when the level is not
// declared. The manifest's shared "schemas" block travels with it, so a
// "#/schemas/<name>" ref, at the level or nested inside, resolves when the
// result is compiled as a root document.
func (m Manifest) SettingsSchema(levelName string) json.RawMessage {
	var settings map[string]json.RawMessage
	if len(m.Settings) == 0 || json.Unmarshal(m.Settings, &settings) != nil {
		return nil
	}
	raw := settings[levelName]
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil {
		return nil
	}
	if len(m.Schemas) > 0 && string(m.Schemas) != "null" {
		root["schemas"] = m.Schemas
	}
	out, err := json.Marshal(root)
	if err != nil {
		return nil
	}
	return out
}

// SecretKeys returns the fields of one settings level the manifest marks
// x-secret, sorted.
func (m Manifest) SecretKeys(levelName string) []string {
	var keys []string
	for k, f := range m.SettingFields(levelName) {
		if f.Secret {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}
