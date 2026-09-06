package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Independent verification of #5d3dc714 found this gap by mutation: making
// recordGatePredicate return unconditionally at the top left the ENTIRE suite green,
// because no test ever wired a log repository into the service — every existing test
// hits the `if s.gatePredicateLog == nil { return }` early-out.
//
// That is the same defect class already guarded at the SQL layer
// (gate_predicate_log_repo_sqlmock_test.go), one seam higher: the statement is pinned,
// but nothing proved the statement is ever REACHED. And because the recorder is
// deliberately best-effort, a silent no-op here produces no runtime signal at all — just
// an empty table quietly answering the card's two-week ratio as zero.

type recordingGateLog struct {
	mu      sync.Mutex
	entries []domain.GatePredicateLogEntry
	err     error
}

func (r *recordingGateLog) Record(_ context.Context, e *domain.GatePredicateLogEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.entries = append(r.entries, *e)
	return nil
}

func (r *recordingGateLog) CountByOutcome(context.Context, time.Time) (map[domain.GatePredicateOutcome]int, error) {
	return nil, nil
}

func (r *recordingGateLog) recorded() []domain.GatePredicateLogEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.GatePredicateLogEntry(nil), r.entries...)
}

func newLoggingArmService(t *testing.T) (TaskService, *recordingGateLog, uuid.UUID) {
	t.Helper()
	taskRepo := NewMockTaskRepository()
	logRepo := &recordingGateLog{}
	svc := newTestTaskService(taskRepo, NewMockTaskStatusRepository(),
		NewMockTaskDependencyRepository(), NewMockActivityLogRepository(),
		WithGatePredicateLog(logRepo))

	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{ID: taskID, Title: "gate log fixture"}
	return svc, logRepo, taskID
}

// Both outcomes must be recorded. Only logging refusals would leave the ratio without a
// denominator — "how often the guard fired" reads identically whether it is preventing
// everything or nothing.
func TestGatePredicateLog_RecordsAllowedAndRefusedAlike(t *testing.T) {
	for _, tc := range []struct {
		name        string
		reversible  bool
		wantOutcome domain.GatePredicateOutcome
		wantErr     bool
	}{
		{"refused", true, domain.GatePredicateRefusedSelfServe, true},
		{"allowed", false, domain.GatePredicateAllowed, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, logRepo, taskID := newLoggingArmService(t)
			p := basePredicate()
			p.Reversible = tc.reversible

			err := svc.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
				TaskID: taskID, Author: uuid.New(), AuthorType: domain.ActorTypeAgent,
				Reason: "r", RecommendedDefault: "d",
				Source: domain.ArmHumanGateSourceAPI, Predicate: &p,
			})
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			got := logRepo.recorded()
			require.Len(t, got, 1, "every evaluation must be recorded, not just refusals")
			assert.Equal(t, tc.wantOutcome, got[0].Outcome)
			assert.Equal(t, taskID, got[0].TaskID)
			assert.Equal(t, domain.ArmHumanGateSourceAPI, got[0].Source)
			// The reasons are the point of the table: a bool with no reason is
			// unreviewable, so they must survive into the row.
			assert.Equal(t, p.CustomerReason, got[0].CustomerReason)
			assert.Equal(t, p.ReversibleReason, got[0].ReversibleReason)
		})
	}
}

// Marker arms must be recorded too, and must be TELLABLE APART from API arms. Markers
// carry no predicate and always log `allowed`; since markers are today's dominant
// ask-a-human channel, a ratio that mixes the two populations is dominated by marker
// noise and understates how often the predicate actually refuses API asks.
func TestGatePredicateLog_MarkerArmsAreDistinguishable(t *testing.T) {
	svc, logRepo, taskID := newLoggingArmService(t)

	require.NoError(t, svc.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID: taskID, Author: uuid.New(), AuthorType: domain.ActorTypeAgent,
		Reason: "какой шлюз выбираем?", Source: domain.ArmHumanGateSourceMarker,
	}))

	got := logRepo.recorded()
	require.Len(t, got, 1)
	assert.Equal(t, domain.ArmHumanGateSourceMarker, got[0].Source,
		"source must distinguish marker from api, or the two-week ratio mixes populations")
	assert.Equal(t, domain.GatePredicateAllowed, got[0].Outcome)
	assert.Contains(t, got[0].CredentialReason, "not stated",
		"a marker's absent predicate must say so, not masquerade as a stated false")
}

// A logging failure must never change the arm/refuse decision. The recorder is
// best-effort on purpose — a broken table must not become a broken guard.
func TestGatePredicateLog_FailureDoesNotChangeTheDecision(t *testing.T) {
	// Refusal still refuses.
	svc, logRepo, taskID := newLoggingArmService(t)
	logRepo.err = errors.New("relation \"gate_predicate_log\" does not exist")
	p := basePredicate()
	p.Reversible = true

	err := svc.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID: taskID, Author: uuid.New(), AuthorType: domain.ActorTypeAgent,
		Reason: "r", RecommendedDefault: "d",
		Source: domain.ArmHumanGateSourceAPI, Predicate: &p,
	})
	require.Error(t, err, "a dead log must not turn a refusal into an arm")

	// And an allow still allows.
	svc2, logRepo2, taskID2 := newLoggingArmService(t)
	logRepo2.err = errors.New("connection reset")
	p2 := basePredicate()

	require.NoError(t, svc2.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID: taskID2, Author: uuid.New(), AuthorType: domain.ActorTypeAgent,
		Reason: "r", RecommendedDefault: "d",
		Source: domain.ArmHumanGateSourceAPI, Predicate: &p2,
	}), "a dead log must not block a legitimate ask either")
}
