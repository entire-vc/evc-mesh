package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Load had no test coverage at all before this — any change to it (however
// small) pulls the whole function into a PR's diff-coverage measurement, so
// a one-line addition like AllowRegistration needs a real test alongside it.

func TestLoad_Defaults(t *testing.T) {
	cfg := Load()
	require.NotNil(t, cfg)

	assert.Equal(t, "0.0.0.0", cfg.Server.Host)
	assert.Equal(t, 8005, cfg.Server.Port)
	assert.Equal(t, "mesh", cfg.Database.Name)
	assert.True(t, cfg.Auth.AllowRegistration, "self-host registration must default to open so existing installs keep working")
}

func TestLoad_AllowRegistrationOverride(t *testing.T) {
	t.Setenv("MESH_ALLOW_REGISTRATION", "false")

	cfg := Load()

	assert.False(t, cfg.Auth.AllowRegistration)
}
