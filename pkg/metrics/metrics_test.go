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
