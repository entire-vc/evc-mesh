package extensions

import (
	"bytes"
	"encoding/json"
	"log"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManifestDropsRetiredCapabilities(t *testing.T) {
	raw := []byte(`{"id":"keep","version":"1.0.0","runtime":"external","capabilities":{"api":["tasks:read","agent_keys:read","secrets:write"],"ui":["command","task.panel","workspace.settings"],"webhooks_in":["inbound"],"network":{"allowed_domains":[],"reasoning":"keep this untyped v1 field"}},"external":{"ui":{"command":{"path":"/cmd"},"task.panel":{"path":"/task"}}}}`)
	var logged bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(prev) })
	m, err := ParseManifest(raw)
	require.NoError(t, err)
	require.Equal(t, []string{"tasks:read"}, m.Capabilities.API)
	require.Equal(t, []string{"task.panel"}, m.Capabilities.UI)
	require.Empty(t, m.Capabilities.WebhooksIn)
	require.NotContains(t, m.External.UI, "command")
	require.Equal(t, string(raw), string(m.Raw))
	require.Len(t, m.Warnings, 6)
	require.Contains(t, logged.String(), `capabilities.api "secrets:write" is deprecated and ignored`)
	require.Contains(t, logged.String(), `capabilities.ui "command" is deprecated and ignored`)
	var effective Manifest
	require.NoError(t, json.Unmarshal(m.EffectiveJSON(), &effective))
	require.Equal(t, m.Capabilities, effective.Capabilities)
	require.NotContains(t, effective.External.UI, "command")
	require.Contains(t, string(m.EffectiveJSON()), `"reasoning":"keep this untyped v1 field"`)
}

func TestManifestSecretsMatchSettings(t *testing.T) {
	for name, caps := range map[string]string{"undeclared": "[]", "unknown": "[\"token\",\"ghost\"]"} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseManifest([]byte(`{"id":"secret-test","version":"1","runtime":"external","capabilities":{"secrets":` + caps + `},"settings":{"workspace":{"properties":{"token":{"x-secret":true}}}}}`))
			require.Error(t, err)
			if name == "unknown" {
				require.Contains(t, err.Error(), "ghost")
			} else {
				require.Contains(t, err.Error(), "token")
			}
		})
	}
}

func TestManifestSecretsBesideRefMatchSettings(t *testing.T) {
	for _, caps := range []string{`["base"]`, `["sibling"]`, `["base","sibling"]`} {
		t.Run(caps, func(t *testing.T) {
			m, err := ParseManifest([]byte(`{"id":"secret-ref","version":"1","runtime":"external",
			"capabilities":{"secrets":` + caps + `},
			"settings":{"workspace":{"$ref":"#/schemas/base","properties":{"sibling":{"x-secret":true}}}},
			"schemas":{"base":{"properties":{"base":{"x-secret":true}}}}}`))
			if caps == `["base","sibling"]` {
				require.NoError(t, err)
				require.Equal(t, []string{"base", "sibling"}, m.SecretKeys("workspace"))
				return
			}
			require.Error(t, err)
			if caps == `["base"]` {
				require.Contains(t, err.Error(), "sibling")
			} else {
				require.Contains(t, err.Error(), "base")
			}
		})
	}
}

func TestManifestRefSiblingCannotRemoveFieldFlags(t *testing.T) {
	m, err := ParseManifest([]byte(`{"id":"secret-ref","version":"1","runtime":"external",
	"capabilities":{"secrets":["token"]},
	"settings":{"workspace":{"$ref":"#/schemas/base","properties":{"token":{"type":"string"}}}},
	"schemas":{"base":{"properties":{"token":{"x-secret":true,"readOnly":true}}}}}`))
	require.NoError(t, err)
	require.Equal(t, SettingField{Secret: true, ReadOnly: true}, m.SettingFields("workspace")["token"])
}
