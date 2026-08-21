package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

// mesh_integration_encryption_active is the only signal that says whether
// integration credentials are actually protected — writes succeed either way,
// so a wrong value here is indistinguishable from a healthy instance.
func TestSetIntegrationEncryptionState(t *testing.T) {
	for _, tc := range []struct {
		name     string
		state    string
		required bool
		want     float64
	}{
		{"configured", "ok", false, 1},
		{"configured and required", "ok", true, 1},
		{"key absent", "absent", false, 0},
		{"key malformed", "invalid", false, 0},
		{"required but absent", "absent", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetIntegrationEncryptionState(tc.state, tc.required)
			assert.Equal(t, tc.want, testutil.ToFloat64(IntegrationEncryptionActive))
		})
	}
}

// mesh_client_ip_trusted is the loud-degradation signal for #5d759aad: 0
// means /auth/login's per-IP limiter has no real per-client granularity.
// A wrong value here is exactly as invisible as the integration-encryption
// case above — the API keeps answering requests either way.
func TestSetClientIPTrusted(t *testing.T) {
	SetClientIPTrusted(true)
	assert.Equal(t, float64(1), testutil.ToFloat64(ClientIPTrusted))

	SetClientIPTrusted(false)
	assert.Equal(t, float64(0), testutil.ToFloat64(ClientIPTrusted))
}

// mesh_event_bus_enabled is the only signal that says whether mesh-api is
// actually running with a working NATS/Redis event bus — the API keeps
// serving requests identically either way, so a wrong value here is exactly
// as invisible as the two cases above.
func TestSetEventBusEnabled(t *testing.T) {
	SetEventBusEnabled(true)
	assert.Equal(t, float64(1), testutil.ToFloat64(EventBusEnabled))

	SetEventBusEnabled(false)
	assert.Equal(t, float64(0), testutil.ToFloat64(EventBusEnabled))
}
