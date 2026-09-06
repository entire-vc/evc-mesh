package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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

// The NATS stream-limit defaults must match the values that
// eventbus.DefaultConfig() previously hardcoded (10 GB / 30 days / 256 KB /
// 1 replica) — leaving the new env vars unset must change nothing for an
// existing deployment.
func TestLoad_NATSStreamLimitDefaults(t *testing.T) {
	cfg := Load()

	// cfg.NATS.URL is deliberately not asserted here: `make ci` runs the
	// whole suite with NATS_URL pointed at its per-checkout compose port
	// (see ci-test in the Makefile), same reason TestLoad_Defaults above
	// never checks it either.
	assert.Equal(t, "http://localhost:8223", cfg.NATS.MonitorURL)
	assert.Equal(t, int64(10*1024*1024*1024), cfg.NATS.StreamMaxBytes)
	assert.Equal(t, 30*24*time.Hour, cfg.NATS.StreamMaxAge)
	assert.Equal(t, int32(256*1024), cfg.NATS.MaxMsgSize)
	assert.Equal(t, 1, cfg.NATS.Replicas)
}

func TestLoad_NATSStreamLimitOverrides(t *testing.T) {
	t.Setenv("NATS_MONITOR_URL", "http://nats:8222")
	t.Setenv("NATS_STREAM_MAX_BYTES", "1048576")
	t.Setenv("NATS_STREAM_MAX_AGE", "24h")
	t.Setenv("NATS_MAX_MSG_SIZE", "1024")
	t.Setenv("NATS_REPLICAS", "3")

	cfg := Load()

	assert.Equal(t, "http://nats:8222", cfg.NATS.MonitorURL)
	assert.Equal(t, int64(1048576), cfg.NATS.StreamMaxBytes)
	assert.Equal(t, 24*time.Hour, cfg.NATS.StreamMaxAge)
	assert.Equal(t, int32(1024), cfg.NATS.MaxMsgSize)
	assert.Equal(t, 3, cfg.NATS.Replicas)
}

// A malformed NATS_STREAM_MAX_BYTES (getEnvInt64's parse-failure path) must
// fall back to the default rather than zeroing out the stream's storage
// limit.
func TestLoad_NATSStreamMaxBytesInvalidFallsBackToDefault(t *testing.T) {
	t.Setenv("NATS_STREAM_MAX_BYTES", "not-a-number")

	cfg := Load()

	assert.Equal(t, int64(10*1024*1024*1024), cfg.NATS.StreamMaxBytes)
}

// TestLoad_HumanGateDefaultTimeoutHours_Defaults pins the value itself, not just that
// SOME default exists: task #060ccaae / Pavel decision 2026-09-06 is specifically 24,
// not the original 72h spec — a silently-reverted default would ship the wrong policy
// with every other test in this file staying green.
func TestLoad_HumanGateDefaultTimeoutHours_Defaults(t *testing.T) {
	cfg := Load()

	assert.Equal(t, 24, cfg.HumanGate.DefaultTimeoutHours)
}

func TestLoad_HumanGateDefaultTimeoutHours_Override(t *testing.T) {
	t.Setenv("HUMAN_GATE_DEFAULT_TIMEOUT_H", "48")

	cfg := Load()

	assert.Equal(t, 48, cfg.HumanGate.DefaultTimeoutHours)
}

// A malformed HUMAN_GATE_DEFAULT_TIMEOUT_H must fall back to the default rather than
// zeroing out the window — getEnvInt's own parse-failure path, pinned here because a
// zeroed window would (absent TaskRepo's own defaultGateTimeoutHoursOrDefault floor)
// mean every soft gate's auto-deadline computes as "right now".
func TestLoad_HumanGateDefaultTimeoutHours_InvalidFallsBackToDefault(t *testing.T) {
	t.Setenv("HUMAN_GATE_DEFAULT_TIMEOUT_H", "not-a-number")

	cfg := Load()

	assert.Equal(t, 24, cfg.HumanGate.DefaultTimeoutHours)
}

// A NATS_MAX_MSG_SIZE outside int32's range must fall back to the default,
// not wrap silently. int32(getEnvInt(...)) truncated a value like this into
// an unrelated, possibly negative, number instead — caught by CodeQL on
// PR #720 before this test existed.
func TestLoad_NATSMaxMsgSizeOutOfInt32RangeFallsBackToDefault(t *testing.T) {
	t.Setenv("NATS_MAX_MSG_SIZE", "5000000000") // > math.MaxInt32

	cfg := Load()

	assert.Equal(t, int32(256*1024), cfg.NATS.MaxMsgSize)
}
