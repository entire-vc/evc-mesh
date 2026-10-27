// Package agentchat is the Chat with Agent extension: manifest, typed workspace
// and project settings and their validation. Threads, messages and the runner
// protocol live in the repository and handler layers; this package owns the
// question "what are the chat settings of this workspace or project".
package agentchat

import (
	"errors"
	"fmt"
	"slices"

	_ "embed"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/extensions"
)

//go:embed mesh-extension.json
var manifestJSON []byte

// ID is the extension id (manifest id, integration_configs.provider,
// project_integrations.extension_id).
const ID = "agent-chat"

// Defaults and bounds of the workspace settings (agent-chat-openapi
// WorkspaceChatSettings). The attachment ceiling is the Docs limit.
const (
	DefaultMaxAttachmentMB    = 25
	MaxAttachmentMB           = 25
	DefaultSessionIdleMinutes = 10
	MaxSessionIdleMinutes     = 240
	DefaultOfflineExpiryHours = 24
	MaxOfflineExpiryHours     = 720
)

// Project member roles a project may admit to the chat.
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
	RoleViewer = "viewer"
)

// WorkspaceSettings is the typed form of the integration_configs row.
type WorkspaceSettings struct {
	HistoryRetentionDays int `json:"history_retention_days"`
	MaxAttachmentMB      int `json:"max_attachment_mb"`
	SessionIdleMinutes   int `json:"session_idle_minutes"`
	// OfflineExpiryHours is how long a turn may wait in queued for a runner
	// before it expires (ADR decision 24 h).
	OfflineExpiryHours int `json:"offline_expiry_hours"`
}

// DefaultWorkspaceSettings is what a workspace without a row gets.
func DefaultWorkspaceSettings() WorkspaceSettings {
	return WorkspaceSettings{MaxAttachmentMB: DefaultMaxAttachmentMB, SessionIdleMinutes: DefaultSessionIdleMinutes, OfflineExpiryHours: DefaultOfflineExpiryHours}
}

// ProjectSettings is the typed form of the project_integrations settings.
type ProjectSettings struct {
	Agents       []uuid.UUID `json:"agents"`
	AllowedRoles []string    `json:"allowed_roles"`
}

// DefaultAllowedRoles applies while a project has not chosen its roles.
func DefaultAllowedRoles() []string { return []string{RoleAdmin, RoleMember} }

// Allows reports whether a project member with this role may use the chat.
func (s ProjectSettings) Allows(role string) bool {
	for _, r := range s.AllowedRoles {
		if r == role {
			return true
		}
	}
	return false
}

// HasAgent reports whether the agent is one of the project's chat agents.
func (s ProjectSettings) HasAgent(id uuid.UUID) bool {
	for _, a := range s.Agents {
		if a == id {
			return true
		}
	}
	return false
}

// ParseWorkspaceSettings decodes a stored row, filling defaults for what it omits.
func ParseWorkspaceSettings(raw map[string]any) (WorkspaceSettings, error) {
	s := DefaultWorkspaceSettings()
	if len(raw) == 0 {
		return s, nil
	}
	got, err := extensions.DecodeSettings[WorkspaceSettings](raw)
	if err != nil {
		return s, err
	}
	if _, ok := raw["max_attachment_mb"]; ok {
		s.MaxAttachmentMB = got.MaxAttachmentMB
	}
	if _, ok := raw["session_idle_minutes"]; ok {
		s.SessionIdleMinutes = got.SessionIdleMinutes
	}
	if _, ok := raw["offline_expiry_hours"]; ok {
		s.OfflineExpiryHours = got.OfflineExpiryHours
	}
	s.HistoryRetentionDays = got.HistoryRetentionDays
	return s, nil
}

// ParseProjectSettings decodes a stored row; missing allowed_roles means the default.
func ParseProjectSettings(raw map[string]any) (ProjectSettings, error) {
	s := ProjectSettings{}
	if len(raw) > 0 {
		got, err := extensions.DecodeSettings[ProjectSettings](raw)
		if err != nil {
			return s, err
		}
		s = got
	}
	if s.AllowedRoles == nil {
		s.AllowedRoles = DefaultAllowedRoles()
	}
	return s, nil
}

// ValidateWorkspaceSettings checks the bounds of the workspace settings.
func ValidateWorkspaceSettings(s WorkspaceSettings) error {
	switch {
	case s.HistoryRetentionDays < 0:
		return errors.New("history_retention_days must be >= 0")
	case s.MaxAttachmentMB < 1 || s.MaxAttachmentMB > MaxAttachmentMB:
		return fmt.Errorf("max_attachment_mb must be between 1 and %d", MaxAttachmentMB)
	case s.SessionIdleMinutes < 1 || s.SessionIdleMinutes > MaxSessionIdleMinutes:
		return fmt.Errorf("session_idle_minutes must be between 1 and %d", MaxSessionIdleMinutes)
	case s.OfflineExpiryHours < 1 || s.OfflineExpiryHours > MaxOfflineExpiryHours:
		return fmt.Errorf("offline_expiry_hours must be between 1 and %d", MaxOfflineExpiryHours)
	}
	return nil
}

// ValidateProjectSettings checks the shape of the project settings. Whether the
// agents are served by this workspace's runners is checked where the database is.
func ValidateProjectSettings(s ProjectSettings) error {
	seen := map[uuid.UUID]bool{}
	for _, a := range s.Agents {
		if a == uuid.Nil {
			return errors.New("agents must be agent ids")
		}
		if seen[a] {
			return errors.New("agents must not repeat")
		}
		seen[a] = true
	}
	for _, r := range s.AllowedRoles {
		switch r {
		case RoleAdmin, RoleMember, RoleViewer:
		default:
			return fmt.Errorf("allowed_roles: unknown role %q", r)
		}
	}
	return nil
}

// Provider implements extensions.Provider for Chat with Agent.
type Provider struct {
	extensions.BaseProvider
}

// New builds the provider over the shared stores.
func New(d extensions.Deps) *Provider {
	m, err := extensions.ParseManifest(manifestJSON)
	if err != nil {
		panic(fmt.Sprintf("agentchat: embedded manifest: %v", err))
	}
	return &Provider{BaseProvider: extensions.BaseProvider{Man: m, Conns: d.Conns, Inst: d.Inst, Allow: d.Allow}}
}

var (
	workspaceKeys = []string{"history_retention_days", "max_attachment_mb", "session_idle_minutes", "offline_expiry_hours"}
	projectKeys   = []string{"agents", "allowed_roles"}
)

// rejectUnknownKeys enforces the manifest's additionalProperties:false, which
// json decoding into a struct would silently ignore.
func rejectUnknownKeys(raw map[string]any, known []string) error {
	for k := range raw {
		if !slices.Contains(known, k) {
			return fmt.Errorf("unknown setting %q", k)
		}
	}
	return nil
}

// CheckWorkspaceKeys rejects keys the workspace settings schema does not define.
func CheckWorkspaceKeys(raw map[string]any) error { return rejectUnknownKeys(raw, workspaceKeys) }

// Validate: no instance level; workspace and project settings are typed.
func (p *Provider) Validate(level extensions.Level, settings map[string]any) error {
	switch level {
	case extensions.LevelWorkspace:
		if err := rejectUnknownKeys(settings, workspaceKeys); err != nil {
			return err
		}
		s, err := ParseWorkspaceSettings(settings)
		if err != nil {
			return err
		}
		return ValidateWorkspaceSettings(s)
	case extensions.LevelProject:
		if err := rejectUnknownKeys(settings, projectKeys); err != nil {
			return err
		}
		s, err := ParseProjectSettings(settings)
		if err != nil {
			return err
		}
		return ValidateProjectSettings(s)
	}
	return fmt.Errorf("agent-chat has no %s-level settings", level)
}
