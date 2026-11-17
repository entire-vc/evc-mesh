package extensions

import (
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"sort"
)

var retiredSlots = []string{"workspace.settings", "project.settings", "command", "doc.panel"}
var retiredAPI = []string{"workspaces:write", "members:write", "agents:write", "agent_keys:read", "agent_keys:write", "secrets:read", "secrets:write"}

// IsDeprecatedCapability reports v1 declarations that are accepted but grant
// nothing. Catalog release metadata uses the same rule before consent diffs.
func IsDeprecatedCapability(group, value string) bool {
	switch group {
	case "api":
		return slices.Contains(retiredAPI, value)
	case "ui":
		return slices.Contains(retiredSlots, value)
	default:
		return false
	}
}

// normalize keeps v1 inputs readable while removing declarations that have no
// runtime consumer. Raw remains the exact document saved by the installer.
func (m *Manifest) normalize() error {
	filter := func(group string, values []string) []string {
		out := make([]string, 0, len(values))
		for _, value := range values {
			if IsDeprecatedCapability(group, value) {
				m.warn(fmt.Sprintf("capabilities.%s %q is deprecated and ignored", group, value))
			} else {
				out = append(out, value)
			}
		}
		return out
	}
	m.Capabilities.API = filter("api", m.Capabilities.API)
	m.Capabilities.UI = filter("ui", m.Capabilities.UI)
	if m.External != nil {
		for _, slot := range retiredSlots {
			if _, ok := m.External.UI[slot]; ok {
				delete(m.External.UI, slot)
				m.warn(fmt.Sprintf("external.ui %q is deprecated and ignored", slot))
			}
		}
	}
	if m.Runtime != "builtin" || m.Kind == "theme" {
		for _, name := range m.Capabilities.WebhooksIn {
			m.warn(fmt.Sprintf("capabilities.webhooks_in %q is builtin-only and ignored", name))
		}
		m.Capabilities.WebhooksIn = []string{}
	}
	fields := map[string]bool{}
	for _, level := range []string{"instance", "workspace", "project"} {
		for _, key := range m.SecretKeys(level) {
			fields[key] = true
		}
	}
	for _, key := range m.Capabilities.Secrets {
		if !fields[key] {
			return fmt.Errorf("capabilities.secrets field %q has no x-secret setting", key)
		}
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !slices.Contains(m.Capabilities.Secrets, key) {
			return fmt.Errorf("x-secret setting %q is missing from capabilities.secrets", key)
		}
	}
	return nil
}

func (m *Manifest) warn(message string) {
	m.Warnings = append(m.Warnings, message)
	log.Printf("[extensions] manifest %q: %s", m.ID, message)
}

// EffectiveJSON is the manifest exposed to consumers (catalog and install
// preview), preserving untyped v1 fields. It never changes the stored Raw.
func (m Manifest) EffectiveJSON() json.RawMessage {
	var doc map[string]json.RawMessage
	if json.Unmarshal(m.Raw, &doc) != nil {
		return nil
	}
	var caps map[string]json.RawMessage
	_ = json.Unmarshal(doc["capabilities"], &caps)
	if caps != nil {
		for group, values := range map[string][]string{"api": m.Capabilities.API, "ui": m.Capabilities.UI, "webhooks_in": m.Capabilities.WebhooksIn} {
			if _, ok := caps[group]; ok {
				caps[group], _ = json.Marshal(values)
			}
		}
		doc["capabilities"], _ = json.Marshal(caps)
	}
	if m.External != nil {
		var ext map[string]json.RawMessage
		_ = json.Unmarshal(doc["external"], &ext)
		if ext != nil {
			if _, ok := ext["ui"]; ok {
				ext["ui"], _ = json.Marshal(m.External.UI)
			}
			doc["external"], _ = json.Marshal(ext)
		}
	}
	raw, _ := json.Marshal(doc)
	return raw
}
