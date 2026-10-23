package secretstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// Environment of the Keep client the API process uses to open keep:
// references. All three unset keeps the default builtin-only store; a partial
// set is a misconfiguration and refused.
const (
	EnvKeepURL   = "MESH_KEEP_URL"
	EnvKeepOrgID = "MESH_KEEP_ORG_ID"
	EnvKeepToken = "MESH_KEEP_TOKEN"
)

// NewKeepRouterFromEnv builds the process-wide store when Keep is configured.
// It returns (nil, nil) when none of the variables is set. New values keep
// going to builtin (Selected is nil) until the provider switch is made on
// purpose; the router only adds the ability to open keep: references.
func NewKeepRouterFromEnv(getenv func(string) string) (*Router, error) {
	url, org, tok := getenv(EnvKeepURL), getenv(EnvKeepOrgID), getenv(EnvKeepToken)
	if url == "" && org == "" && tok == "" {
		return nil, nil
	}
	if url == "" || org == "" || tok == "" {
		return nil, errors.New("keep secret store: " + EnvKeepURL + ", " + EnvKeepOrgID + " and " + EnvKeepToken + " must be set together")
	}
	orgID, err := uuid.Parse(org)
	if err != nil {
		return nil, fmt.Errorf("keep secret store: %s: %w", EnvKeepOrgID, err)
	}
	client := &HTTPKeepClient{
		BaseURL: url,
		OrgID:   orgID,
		Token:   func(context.Context) (string, error) { return tok, nil },
	}
	return &Router{Builtin: Builtin{}, Keep: Keep{Client: client}}, nil
}
