package config

import (
	"os"
	"path/filepath"
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

// The compose stack generates the metrics token into a file rather than
// requiring it in .env, because a required-with-no-default variable breaks
// `docker compose up` for every install that predates it (2026-07-29,
// mesh.prototypes.ventures). These cover the precedence that makes that
// possible.

func TestLoad_MetricsTokenFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics_token")
	// Trailing newline is what `echo > file` produces; it must not become
	// part of the token, or the bearer comparison never matches.
	require.NoError(t, os.WriteFile(path, []byte("tok-from-file\n"), 0o600))
	t.Setenv("MESH_METRICS_TOKEN_FILE", path)

	assert.Equal(t, "tok-from-file", Load().Server.MetricsToken)
}

func TestLoad_MetricsTokenEnvBeatsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics_token")
	require.NoError(t, os.WriteFile(path, []byte("tok-from-file"), 0o600))
	t.Setenv("MESH_METRICS_TOKEN_FILE", path)
	t.Setenv("MESH_METRICS_TOKEN", "tok-from-env")

	assert.Equal(t, "tok-from-env", Load().Server.MetricsToken)
}

func TestLoad_MetricsTokenMissingFileLeavesEndpointOpen(t *testing.T) {
	// An unreadable file must not be fatal: a monitoring knob is not worth
	// refusing to boot over, and empty means "open", which is the historical
	// behavior for deploys that gate /metrics at the network layer.
	t.Setenv("MESH_METRICS_TOKEN_FILE", filepath.Join(t.TempDir(), "absent"))

	assert.Empty(t, Load().Server.MetricsToken)
}

func TestLoad_MetricsTokenUnsetByDefault(t *testing.T) {
	assert.Empty(t, Load().Server.MetricsToken)
}
