package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
)

// gateEventRecorder stands in for the webhook service — the boundary the public
// event leaves through. Only Dispatch is called by the code under test.
type gateEventRecorder struct {
	WebhookService
	got chan gateEventCall
}

type gateEventCall struct {
	ws      uuid.UUID
	event   string
	payload map[string]any
}

func (r *gateEventRecorder) Dispatch(_ context.Context, ws uuid.UUID, event string, payload any) {
	r.got <- gateEventCall{ws: ws, event: event, payload: payload.(map[string]any)}
}

func setupGateEventEnv(t *testing.T) (env hgdEnv, rec *gateEventRecorder, ws, projectID uuid.UUID) {
	t.Helper()
	commentRepo := NewMockCommentRepository()
	projects := NewMockProjectRepository()
	ws, projectID = uuid.New(), uuid.New()
	require.NoError(t, projects.Create(context.Background(), &domain.Project{ID: projectID, WorkspaceID: ws}))
	hgdRepo := &fakeHGDRepo{}
	taskSvc := newFakeHumanGateTaskService()
	rec = &gateEventRecorder{got: make(chan gateEventCall, 4)}

	svc := NewCommentService(commentRepo, NewMockTaskRepository(), NewMockActivityLogRepository(),
		WithCommentTaskService(taskSvc),
		WithHumanGateDecisionRepo(hgdRepo),
		WithCommentProjectRepo(projects),
		WithCommentWebhookService(rec),
	).(*commentService)
	env = hgdEnv{svc: svc, hgdRepo: hgdRepo, taskSvc: taskSvc, commentRepo: commentRepo}
	return env, rec, ws, projectID
}

func (r *gateEventRecorder) next(t *testing.T) gateEventCall {
	t.Helper()
	select {
	case c := <-r.got:
		return c
	case <-time.After(2 * time.Second):
		require.FailNow(t, "task.gate.decided was not dispatched")
		return gateEventCall{}
	}
}

func TestRecordHumanGateDecision_DispatchesGateDecided(t *testing.T) {
	cases := []struct {
		name       string
		provenance domain.HumanGateProvenance
		recordedBy *uuid.UUID
		wantType   domain.ActorType
	}{
		{"direct decision is a user act", domain.HumanGateProvenanceDirect, nil, domain.ActorTypeUser},
		{"bridged decision is a user act", domain.HumanGateProvenanceBridged, nil, domain.ActorTypeUser},
		{"bridged decision recorded by the bridge stays a user act", domain.HumanGateProvenanceBridged, ptrUUID(uuid.New()), domain.ActorTypeUser},
		{"attested decision is the recording agent's act", domain.HumanGateProvenanceAttested, ptrUUID(uuid.New()), domain.ActorTypeAgent},
		{"default applied is a system act", domain.HumanGateProvenanceDefaultApplied, nil, domain.ActorTypeSystem},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, rec, ws, projectID := setupGateEventEnv(t)
			taskID, human := uuid.New(), uuid.New()
			env.taskSvc.seed(&domain.Task{ID: taskID, ProjectID: projectID, HumanGate: true})
			ref := uuid.New()

			d, err := env.svc.RecordHumanGateDecision(context.Background(), domain.RecordHumanGateDecisionInput{
				TaskID: taskID, QuestionRef: &ref, DecidedBy: human, RecordedBy: tc.recordedBy,
				Provenance: tc.provenance, Channel: domain.HumanGateChannelMesh, Quote: strP("approve hash abc"),
			})
			require.NoError(t, err)

			call := rec.next(t)
			assert.Equal(t, ws, call.ws, "delivered in the task's workspace")
			assert.Equal(t, extensions.EventTaskGateDecided, call.event)
			// Receivers pick their signing secret by workspace_id (docs/webhooks.md).
			assert.Equal(t, ws, call.payload["workspace_id"], "body names the task's workspace")
			assert.Equal(t, taskID, call.payload["task_id"])
			assert.Equal(t, d.ID, call.payload["decision_id"])
			assert.Equal(t, "approve hash abc", call.payload["decision"])
			assert.Equal(t, human, call.payload["decided_by"])
			assert.Equal(t, tc.wantType, call.payload["decided_by_type"])
			assert.Equal(t, string(tc.provenance), call.payload["provenance"])
		})
	}
}

func TestRecordHumanGateDecision_NoGateDecidedWhenNothingRecorded(t *testing.T) {
	env, rec, _, projectID := setupGateEventEnv(t)
	taskID := uuid.New()
	env.taskSvc.seed(&domain.Task{ID: taskID, ProjectID: projectID, HumanGate: true})
	env.hgdRepo.createErr = assertErr("ledger down")

	_, err := env.svc.RecordHumanGateDecision(context.Background(), domain.RecordHumanGateDecisionInput{
		TaskID: taskID, DecidedBy: uuid.New(), Provenance: domain.HumanGateProvenanceDirect, Channel: domain.HumanGateChannelMesh,
		QuestionRef: ptrUUID(uuid.New()),
	})
	require.Error(t, err)
	select {
	case c := <-rec.got:
		require.FailNow(t, "event dispatched for a decision that was not recorded", "%v", c)
	case <-time.After(200 * time.Millisecond):
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

func ptrUUID(u uuid.UUID) *uuid.UUID { return &u }

func TestRecordHumanGateDecision_GateDecidedCarriesExternalBinding(t *testing.T) {
	env, rec, _, projectID := setupGateEventEnv(t)
	taskID := uuid.New()
	env.taskSvc.seed(&domain.Task{ID: taskID, ProjectID: projectID, HumanGate: true})
	refs := &memExtRefRepo{m: map[uuid.UUID]*domain.TaskExternalRef{
		taskID: {TaskID: taskID, Ref: "keep:req-7", Hash: "sha256:abc"},
	}}
	env.svc.extRefRepo = refs

	_, err := env.svc.RecordHumanGateDecision(context.Background(), domain.RecordHumanGateDecisionInput{
		TaskID: taskID, DecidedBy: uuid.New(), Provenance: domain.HumanGateProvenanceDirect,
		Channel: domain.HumanGateChannelMesh, QuestionRef: ptrUUID(uuid.New()), Quote: strP("ok"),
	})
	require.NoError(t, err)
	call := rec.next(t)
	assert.Equal(t, "keep:req-7", call.payload["external_ref"])
	assert.Equal(t, "sha256:abc", call.payload["external_hash"])
}

func TestRecordHumanGateDecision_GateDecidedWithoutBindingOmitsExternalKeys(t *testing.T) {
	env, rec, _, projectID := setupGateEventEnv(t)
	taskID := uuid.New()
	env.taskSvc.seed(&domain.Task{ID: taskID, ProjectID: projectID, HumanGate: true})
	env.svc.extRefRepo = &memExtRefRepo{m: map[uuid.UUID]*domain.TaskExternalRef{}}

	_, err := env.svc.RecordHumanGateDecision(context.Background(), domain.RecordHumanGateDecisionInput{
		TaskID: taskID, DecidedBy: uuid.New(), Provenance: domain.HumanGateProvenanceDirect,
		Channel: domain.HumanGateChannelMesh, QuestionRef: ptrUUID(uuid.New()), Quote: strP("ok"),
	})
	require.NoError(t, err)
	call := rec.next(t)
	assert.NotContains(t, call.payload, "external_ref")
	assert.NotContains(t, call.payload, "external_hash")
}
