package secretstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestNewKeepRouterFromEnv_UnsetKeepsBuiltinOnlyDefault(t *testing.T) {
	r, err := NewKeepRouterFromEnv(envOf(nil))
	require.NoError(t, err)
	assert.Nil(t, r)
}

func TestNewKeepRouterFromEnv_PartialIsRefused(t *testing.T) {
	_, err := NewKeepRouterFromEnv(envOf(map[string]string{EnvKeepURL: "https://keep.example"}))
	assert.ErrorContains(t, err, "must be set together")
}

func TestNewKeepRouterFromEnv_BadOrgRefused(t *testing.T) {
	_, err := NewKeepRouterFromEnv(envOf(map[string]string{EnvKeepURL: "u", EnvKeepOrgID: "nope", EnvKeepToken: "t"}))
	assert.Error(t, err)
}

func TestNewKeepRouterFromEnv_ConfiguredOpensKeepRefsAndKeepsNewValuesBuiltin(t *testing.T) {
	r, err := NewKeepRouterFromEnv(envOf(map[string]string{
		EnvKeepURL: "http://127.0.0.1:1", EnvKeepOrgID: "6f1c1f0e-3a55-4d0c-9d5e-1b3f6c0a7a11", EnvKeepToken: "t"}))
	require.NoError(t, err)
	require.NotNil(t, r)
	assert.Nil(t, r.Selected, "new values stay builtin until the switch is made on purpose")
	// a keep: ref is routed to the Keep client (unreachable here -> degraded with a reason, not empty)
	res := r.Resolve(context.Background(), "keep:6f1c1f0e-3a55-4d0c-9d5e-1b3f6c0a7a22")
	assert.True(t, res.Degraded)
	assert.Contains(t, res.Reason, "keep unavailable")
}
