package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

var executionNow = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func str(v string) *string { return &v }

func validAttempt() ExecutionAttemptReport {
	observedAt := executionNow.Add(-10 * time.Second)
	return ExecutionAttemptReport{
		WorkspaceID:   uuid.New(),
		ProjectID:     uuid.New(),
		AgentID:       uuid.New(),
		BindingRef:    "lane-a",
		ControllerRef: "controller-a",
		InstanceID:    "boot-1",
		Generation:    3,
		AttemptID:     "attempt-1",
		TurnID:        str("turn-1"),
		State:         ExecutionStateRunning,
		ResolvedRoute: ExecutionResolvedRoute{Source: "runner_resolver", Revision: 17, ProfileRef: str("sol-high"), Harness: str("codex"), Model: str("gpt-6.1-sol")},
		Observed:      ExecutionObserved{Harness: str("codex"), Model: str("gpt-6.1-sol"), Source: "adapter_handshake", ObservedAt: observedAt},
	}
}

func validReport() ExecutionReport {
	return ExecutionReport{
		SchemaVersion: ExecutionSchemaVersion,
		InstanceID:    "boot-1",
		Generation:    3,
		Sequence:      1,
		ReportedAt:    executionNow.Add(-5 * time.Second),
		Attempts:      []ExecutionAttemptReport{validAttempt()},
	}
}

func TestExecutionReportValidateAccepts(t *testing.T) {
	require.NoError(t, validReport().Validate(executionNow, "controller-a"))
}

func TestExecutionReportValidateRejects(t *testing.T) {
	cases := map[string]func(r *ExecutionReport){
		"schema":               func(r *ExecutionReport) { r.SchemaVersion = 2 },
		"empty attempts":       func(r *ExecutionReport) { r.Attempts = nil },
		"too many attempts":    func(r *ExecutionReport) { r.Attempts = make([]ExecutionAttemptReport, ExecutionMaxAttempts+1) },
		"zero sequence":        func(r *ExecutionReport) { r.Sequence = 0 },
		"zero generation":      func(r *ExecutionReport) { r.Generation = 0 },
		"future reported_at":   func(r *ExecutionReport) { r.ReportedAt = executionNow.Add(time.Minute) },
		"future observed_at":   func(r *ExecutionReport) { r.Attempts[0].Observed.ObservedAt = executionNow.Add(time.Minute) },
		"instance mismatch":    func(r *ExecutionReport) { r.Attempts[0].InstanceID = "boot-2" },
		"generation mismatch":  func(r *ExecutionReport) { r.Attempts[0].Generation = 2 },
		"controller mismatch":  func(r *ExecutionReport) { r.Attempts[0].ControllerRef = "controller-b" },
		"bad state":            func(r *ExecutionReport) { r.Attempts[0].State = "paused" },
		"nil project":          func(r *ExecutionReport) { r.Attempts[0].ProjectID = uuid.Nil },
		"duplicate attempt":    func(r *ExecutionReport) { r.Attempts = append(r.Attempts, r.Attempts[0]) },
		"observed without src": func(r *ExecutionReport) { r.Attempts[0].Observed.Source = "" },
		"path in model":        func(r *ExecutionReport) { r.Attempts[0].Observed.Model = str("/Users/x/secret") },
		"space in effort":      func(r *ExecutionReport) { r.Attempts[0].Observed.Effort = str("very high") },
		"provenance no time": func(r *ExecutionReport) {
			r.Attempts[0].Provenance = &ExecutionProvenance{Model: &ExecutionFieldSource{Source: "adapter_handshake"}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := validReport()
			mutate(&r)
			require.Error(t, r.Validate(executionNow, "controller-a"))
		})
	}
}

func TestExecutionReportDecodeRejectsUnknownAndSecretFields(t *testing.T) {
	for name, body := range map[string]string{
		"top level extra": `{"schema_version":1,"instance_id":"b","generation":1,"sequence":1,"reported_at":"2026-10-30T11:59:00Z","attempts":[],"env":{"K":"v"}}`,
		"nested extra":    `{"schema_version":1,"instance_id":"b","generation":1,"sequence":1,"reported_at":"2026-10-30T11:59:00Z","attempts":[{"credential_ref":"x"}]}`,
		"duplicate key":   `{"schema_version":1,"schema_version":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			var r ExecutionReport
			require.ErrorIs(t, DecodeExecutionJSON([]byte(body), &r), ErrRuntimeInput)
		})
	}
}

// Positive control for the rejections above: the same decoder accepts a
// well-formed body, and null (unknown model, no task) is a legal value.
func TestExecutionReportDecodeAcceptsNullsAndWellFormed(t *testing.T) {
	body := `{"schema_version":1,"instance_id":"boot-1","generation":3,"sequence":1,"reported_at":"2026-10-30T11:59:55Z","attempts":[{"workspace_id":"11111111-1111-4111-8111-111111111111","project_id":"22222222-2222-4222-8222-222222222222","agent_id":"33333333-3333-4333-8333-333333333333","binding_ref":"lane-a","task_id":null,"controller_ref":"controller-a","instance_id":"boot-1","generation":3,"attempt_id":"a1","turn_id":null,"state":"running","resolved_route":{"source":"runner_resolver","revision":1},"observed":{"harness":null,"model":null,"effort":null,"source":"adapter_handshake","observed_at":"2026-10-30T11:59:50Z"}}]}`
	var r ExecutionReport
	require.NoError(t, DecodeExecutionJSON([]byte(body), &r))
	require.NoError(t, r.Validate(executionNow, "controller-a"))
	require.Nil(t, r.Attempts[0].Observed.Model)
	require.ErrorIs(t, DecodeRuntimeJSON([]byte(body), &r), ErrRuntimeInput, "the catalog decoder still refuses null")
}

func TestExecutionReportBodyHashIgnoresNothing(t *testing.T) {
	a, b := validReport(), validReport()
	b.Attempts[0].AttemptID = a.Attempts[0].AttemptID
	b.Attempts[0].WorkspaceID, b.Attempts[0].ProjectID, b.Attempts[0].AgentID = a.Attempts[0].WorkspaceID, a.Attempts[0].ProjectID, a.Attempts[0].AgentID
	require.Equal(t, a.BodyHash(), b.BodyHash())
	b.Attempts[0].Observed.Model = str("other")
	require.NotEqual(t, a.BodyHash(), b.BodyHash())
}

func TestExecutionFreshness(t *testing.T) {
	window := time.Duration(ExecutionFreshnessSeconds) * time.Second
	fresh := executionNow.Add(-window / 2)
	old := executionNow.Add(-window - time.Second)
	cases := []struct {
		name string
		in   ExecutionFreshnessInput
		want string
	}{
		{"current", ExecutionFreshnessInput{State: ExecutionStateRunning, ModelKnown: true, ObservedAt: fresh, HeartbeatAt: fresh, ReportedAt: fresh, ReceivedAt: fresh, GenerationCurrent: true, ControllerLive: true}, FreshnessCurrent},
		{"late delivery of old data is stale", ExecutionFreshnessInput{State: ExecutionStateRunning, ModelKnown: true, ObservedAt: fresh, HeartbeatAt: fresh, ReportedAt: old, ReceivedAt: fresh, GenerationCurrent: true, ControllerLive: true}, FreshnessStale},
		{"old observation with fresh heartbeat/report/receive is stale", ExecutionFreshnessInput{State: ExecutionStateRunning, ModelKnown: true, ObservedAt: old, HeartbeatAt: fresh, ReportedAt: fresh, ReceivedAt: fresh, GenerationCurrent: true, ControllerLive: true}, FreshnessStale},
		{"old generation is stale", ExecutionFreshnessInput{State: ExecutionStateRunning, ModelKnown: true, ObservedAt: fresh, HeartbeatAt: fresh, ReportedAt: fresh, ReceivedAt: fresh, GenerationCurrent: false, ControllerLive: true}, FreshnessStale},
		{"heartbeat expired is offline", ExecutionFreshnessInput{State: ExecutionStateRunning, ModelKnown: true, ObservedAt: fresh, HeartbeatAt: old, ReportedAt: old, ReceivedAt: old, GenerationCurrent: true, ControllerLive: true}, FreshnessOffline},
		{"controller stopped is offline", ExecutionFreshnessInput{State: ExecutionStateRunning, ModelKnown: true, ObservedAt: fresh, HeartbeatAt: fresh, ReportedAt: fresh, ReceivedAt: fresh, GenerationCurrent: true, ControllerLive: false}, FreshnessOffline},
		{"unknown model never becomes current", ExecutionFreshnessInput{State: ExecutionStateRunning, ModelKnown: false, ObservedAt: fresh, HeartbeatAt: fresh, ReportedAt: fresh, ReceivedAt: fresh, GenerationCurrent: true, ControllerLive: true}, FreshnessUnknown},
		{"completed is history", ExecutionFreshnessInput{State: ExecutionStateCompleted, ModelKnown: true, ObservedAt: fresh, HeartbeatAt: fresh, ReportedAt: fresh, ReceivedAt: fresh, GenerationCurrent: true, ControllerLive: true}, FreshnessHistory},
		{"failed old is history", ExecutionFreshnessInput{State: ExecutionStateFailed, ModelKnown: true, ObservedAt: fresh, HeartbeatAt: old, ReportedAt: old, ReceivedAt: old, GenerationCurrent: false, ControllerLive: false}, FreshnessHistory},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, ExecutionFreshness(executionNow, c.in))
		})
	}
}
