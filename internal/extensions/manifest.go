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

	Raw json.RawMessage `json:"-"`
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
func (m Manifest) InstanceSecretKeys() []string {
	type field struct {
		Secret bool `json:"x-secret"`
	}
	type level struct {
		Ref        string           `json:"$ref"`
		Properties map[string]field `json:"properties"`
	}
	var settings struct {
		Instance *level `json:"instance"`
	}
	if len(m.Settings) == 0 || json.Unmarshal(m.Settings, &settings) != nil || settings.Instance == nil {
		return nil
	}
	lv := *settings.Instance
	if name, ok := strings.CutPrefix(lv.Ref, "#/schemas/"); ok {
		var schemas map[string]level
		if json.Unmarshal(m.Schemas, &schemas) != nil {
			return nil
		}
		lv = schemas[name]
	}
	var keys []string
	for k, f := range lv.Properties {
		if f.Secret {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}
