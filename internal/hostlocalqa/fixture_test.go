package hostlocalqa

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func loadPolicy(t *testing.T) *Policy {
	t.Helper()
	p, err := LoadPolicy()
	require.NoError(t, err)
	return p
}

const (
	fxRunner   = "11111111-2222-4333-8444-555555555555"
	fxReceiver = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	fxRequest  = "99999999-8888-4777-8666-555555555555"
)

// sendMap is a valid sender envelope as a generic map, for mutation.
func sendMap(p *Policy) map[string]any {
	return map[string]any{
		"artifact_id": p.ArtifactID, "candidate": p.Candidate, "capability": Capability,
		"expected_admission_revision": 1, "expires": 4102444800,
		"operation_manifest_sha256": p.OperationManifestSHA, "policy_sha256": p.SHA256,
		"producer":          producerMap(),
		"qa_package_sha256": p.QAPackageSHA, "receiver_id": fxReceiver, "request_id": fxRequest,
		"schema_version": 1, "source_task_id": p.SourceTaskID.String(),
		"target": targetMap(p), "text": p.Text, "text_sha256": p.TextSHA256,
	}
}

func targetMap(p *Policy) map[string]any {
	return map[string]any{"agent_id": p.AgentID.String(), "host_id": "host-1", "hostname": p.Hostname,
		"runner_id": fxRunner, "session_id": "native-1", "workspace_id": p.WorkspaceID.String()}
}

func producerMap() map[string]any {
	return map[string]any{"producer_pid": 4242, "producer_started": "2026-11-03T00:00:00Z",
		"terminal_session_id": "w0t0p0:ABC", "transport": "iterm", "tty": "/dev/ttys004"}
}

func regMap(p *Policy) map[string]any {
	return map[string]any{"schema_version": 1, "capability": Capability, "policy_sha256": p.SHA256,
		"target": targetMap(p), "producer": producerMap(), "expires": 4102444800,
		"allowed_sender_agent_ids": []string{p.AgentID.String()}}
}

func checkMap(p *Policy) map[string]any {
	return map[string]any{"receiver_id": fxReceiver, "admission_revision": 1, "delivery_id": fxRequest,
		"request_id": fxRequest, "capability": Capability, "policy_sha256": p.SHA256, "text_sha256": p.TextSHA256,
		"target": targetMap(p), "producer": producerMap(), "expires": 4102444800}
}

func ackMap(p *Policy, state string) map[string]any {
	m := checkMap(p)
	m["schema_version"], m["state"], m["observed_at"] = 1, state, "2026-11-03T00:00:00Z"
	return m
}

func enc(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return b
}
