package agentchat_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/agentchat"
)

func validate(t *testing.T, raw []byte) error {
	t.Helper()
	sch, err := os.ReadFile("../schema/manifest.v1.json")
	require.NoError(t, err)
	sd, err := jsonschema.UnmarshalJSON(bytes.NewReader(sch))
	require.NoError(t, err)
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	require.NoError(t, c.AddResource("manifest.v1.json", sd))
	compiled, err := c.Compile("manifest.v1.json")
	require.NoError(t, err)
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	require.NoError(t, err)
	return compiled.Validate(inst)
}

func TestAgentChatManifest_ValidAgainstSchemaV1_BrokenOneFails(t *testing.T) {
	raw, err := os.ReadFile("mesh-extension.json")
	require.NoError(t, err)
	require.NoError(t, validate(t, raw))
	broken := bytes.Replace(raw, []byte(`"kind": "plugin"`), []byte(`"kind": "gizmo"`), 1)
	require.NotEqual(t, raw, broken)
	require.Error(t, validate(t, broken))

	m := agentchat.New(extensions.Deps{}).Manifest()
	require.Equal(t, "agent-chat", m.ID)
	require.Equal(t, []string{"runner"}, m.Requires)
	require.False(t, m.Levels.Instance)
	require.True(t, m.Levels.Workspace && m.Levels.Project)
}

func TestAgentChatValidate_WorkspaceSettings(t *testing.T) {
	p := agentchat.New(extensions.Deps{})
	ws := extensions.LevelWorkspace

	require.NoError(t, p.Validate(ws, map[string]any{}), "empty row means defaults")
	require.NoError(t, p.Validate(ws, map[string]any{"history_retention_days": 30, "max_attachment_mb": 10, "session_idle_minutes": 5}))
	require.Error(t, p.Validate(ws, map[string]any{"max_attachment_mb": 26}), "the Docs limit is a ceiling")
	require.Error(t, p.Validate(ws, map[string]any{"max_attachment_mb": 0}))
	require.Error(t, p.Validate(ws, map[string]any{"session_idle_minutes": 241}))
	require.Error(t, p.Validate(ws, map[string]any{"history_retention_days": -1}))
	require.Error(t, p.Validate(ws, map[string]any{"offline_expiry_hours": 0}))
	require.NoError(t, p.Validate(ws, map[string]any{"offline_expiry_hours": 48}))
	require.Error(t, p.Validate(extensions.LevelInstance, map[string]any{}))
	require.Error(t, p.Validate(ws, map[string]any{"session_idle_minute": 5}), "unknown key violates additionalProperties:false")
	require.Error(t, p.Validate(extensions.LevelProject, map[string]any{"agent": []string{}}), "unknown project key")

	s, err := agentchat.ParseWorkspaceSettings(map[string]any{"history_retention_days": 7})
	require.NoError(t, err)
	require.Equal(t, agentchat.WorkspaceSettings{HistoryRetentionDays: 7, MaxAttachmentMB: 25, SessionIdleMinutes: 10, OfflineExpiryHours: 24}, s)
}

func TestAgentChatValidate_ProjectSettings(t *testing.T) {
	p := agentchat.New(extensions.Deps{})
	pr := extensions.LevelProject
	a, b := uuid.NewString(), uuid.NewString()

	require.NoError(t, p.Validate(pr, map[string]any{"agents": []any{a, b}, "allowed_roles": []any{"admin", "viewer"}}))
	require.Error(t, p.Validate(pr, map[string]any{"agents": []any{a, a}}), "an agent twice")
	require.Error(t, p.Validate(pr, map[string]any{"agents": []any{"not-a-uuid"}}))
	require.Error(t, p.Validate(pr, map[string]any{"agents": []any{a}, "allowed_roles": []any{"owner"}}))

	s, err := agentchat.ParseProjectSettings(map[string]any{"agents": []any{a}})
	require.NoError(t, err)
	require.Equal(t, agentchat.DefaultAllowedRoles(), s.AllowedRoles)
	require.True(t, s.Allows("member"))
	require.False(t, s.Allows("viewer"))
	require.True(t, s.HasAgent(uuid.MustParse(a)))
	require.False(t, s.HasAgent(uuid.MustParse(b)))
}
