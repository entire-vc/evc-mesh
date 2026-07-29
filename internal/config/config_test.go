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

// An unset MESH_BASE_URL produces invite links pointing at the invitee's own
// machine. The API warns about it at startup, so the "is it the fallback?"
// question has to be answerable rather than guessed at by string-matching in
// main().
func TestLoad_BaseURLDefaultsToDevServerAndIsFlagged(t *testing.T) {
	cfg := Load()

	assert.Equal(t, DefaultBaseURL, cfg.Email.BaseURL)
	assert.True(t, cfg.Email.BaseURLIsDefault(), "an unset MESH_BASE_URL must be reported as the fallback")
}

func TestLoad_BaseURLOverrideIsNotFlagged(t *testing.T) {
	t.Setenv("MESH_BASE_URL", "https://mesh.example.com")

	cfg := Load()

	assert.Equal(t, "https://mesh.example.com", cfg.Email.BaseURL)
	assert.False(t, cfg.Email.BaseURLIsDefault())
}
