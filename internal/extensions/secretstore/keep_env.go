package secretstore

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// Environment of the Keep client the API process uses to open keep:
// references: the Keep base URL, the organisation, and this installation's
// workspace id with the events secret Keep issued at install (the HMAC key).
// All unset keeps the default builtin-only store; a partial set is a
// misconfiguration and refused.
const (
	EnvKeepURL          = "MESH_KEEP_URL"
	EnvKeepOrgID        = "MESH_KEEP_ORG_ID"
	EnvKeepWorkspaceID  = "MESH_KEEP_WORKSPACE_ID"
	EnvKeepEventsSecret = "MESH_KEEP_EVENTS_SECRET"
)

// NewKeepRouterFromEnv builds the process-wide store when Keep is configured.
// It returns (nil, nil) when none of the variables is set. Selected is left
// for the caller to wire to the keep extension's secret_provider setting;
// until then new values go to builtin.
func NewKeepRouterFromEnv(getenv func(string) string) (*Router, error) {
	url, org, ws, secret := getenv(EnvKeepURL), getenv(EnvKeepOrgID), getenv(EnvKeepWorkspaceID), getenv(EnvKeepEventsSecret)
	if url == "" && org == "" && ws == "" && secret == "" {
		return nil, nil
	}
	if url == "" || org == "" || ws == "" || secret == "" {
		return nil, errors.New("keep secret store: " + EnvKeepURL + ", " + EnvKeepOrgID + ", " + EnvKeepWorkspaceID + " and " + EnvKeepEventsSecret + " must be set together")
	}
	orgID, err := uuid.Parse(org)
	if err != nil {
		return nil, fmt.Errorf("keep secret store: %s: %w", EnvKeepOrgID, err)
	}
	wsID, err := uuid.Parse(ws)
	if err != nil {
		return nil, fmt.Errorf("keep secret store: %s: %w", EnvKeepWorkspaceID, err)
	}
	client := &HTTPKeepClient{BaseURL: url, OrgID: orgID, WorkspaceID: wsID, EventsSecret: []byte(secret)}
	return &Router{Builtin: Builtin{}, Keep: Keep{Client: client}}, nil
}
