package extensions

import (
	"context"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// Level is the scope a settings object belongs to (manifest `levels`).
type Level string

const (
	LevelInstance  Level = "instance"
	LevelWorkspace Level = "workspace"
	LevelProject   Level = "project"
)

// Source says which layer decided the effective settings.
type Source string

const (
	// SourceWorkspace: the workspace's own connection row governs (active or
	// explicitly switched off).
	SourceWorkspace Source = "workspace"
	// SourceInstance: no workspace row; the instance-level setting governs.
	SourceInstance Source = "instance"
	// SourceNone: nothing configured, or instance level switched off.
	SourceNone Source = "none"
)

// Resolved is the effective configuration of one extension for one workspace.
type Resolved struct {
	ExtensionID string
	// WorkspaceID is set by the registry before a health check, so an external
	// provider can tell which workspace's connection it is probing.
	WorkspaceID uuid.UUID
	Enabled     bool
	Source      Source
	Settings    map[string]any
	// NotAllowed: the instance allow-list refuses the extension, so it is off
	// whatever its connection says.
	NotAllowed bool
	// InstanceDisabled: the instance-level setting of the extension exists and
	// is switched off. Informational: it does not change Enabled by itself.
	InstanceDisabled bool
}

// HealthReport is the result of a live connection check.
type HealthReport struct {
	OK      bool
	Message string
}

// Route is one HTTP route an extension contributes.
type Route struct {
	Method      string
	Path        string
	Handler     echo.HandlerFunc
	Middlewares []echo.MiddlewareFunc
}

// Hooks are the inbound routes an extension adds to the core, including
// public webhooks.
type Hooks struct {
	Routes []Route
}

// Provider is the contract every extension implements.
type Provider interface {
	// Manifest returns the manifest embedded in the extension package.
	Manifest() Manifest
	// Validate checks settings for a level against the manifest schema plus
	// the provider's own rules.
	Validate(level Level, settings map[string]any) error
	// Resolve returns the effective settings and their source for a workspace.
	Resolve(ctx context.Context, workspaceID uuid.UUID) (Resolved, error)
	// Health runs a live check of the resolved connection.
	Health(ctx context.Context, r Resolved) HealthReport
	// Hooks returns inbound routes.
	Hooks() Hooks
}
