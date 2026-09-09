package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Task #f933dc05 — the hole this card's own fix would otherwise open.
//
// TaskRepo.ArmHumanGate's UPDATE writes gate_author unconditionally, re-arm included.
// That was harmless while gate_author decided nothing. The moment the recorded author
// may clear the gate with its own key, "take over someone else's gate" becomes two
// calls: POST /human-gate to become its author, then DELETE to release it — which would
// hand agents a back-door key to a USER-armed gate, the exact control this card is
// protecting. A fix without this guard would pass every acceptance criterion on the card
// and quietly be worse than the defect.

// seedLiveGate puts an already-armed gate on the fixture task, authored by someone else.
func seedLiveGate(repo *MockTaskRepository, taskID, author uuid.UUID, authorType domain.ActorType) {
	at := authorType
	task := repo.items[taskID]
	task.HumanGate = true
	task.GateAuthor = &author
	task.GateAuthorType = &at
}

func armAPI(svc TaskService, taskID, author uuid.UUID) error {
	return svc.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID:             taskID,
		Author:             author,
		AuthorType:         domain.ActorTypeAgent,
		Reason:             "нужен выбор варианта",
		RecommendedDefault: "закрыть как есть",
		Source:             domain.ArmHumanGateSourceAPI,
		Predicate:          allowingPredicate(),
	})
}

func TestArmHumanGate_APIReArmByAnotherAgent_Refused(t *testing.T) {
	svc, repo, taskID := newArmingTestService(t)
	original := uuid.New()
	seedLiveGate(repo, taskID, original, domain.ActorTypeAgent)

	err := armAPI(svc, taskID, uuid.New())

	var vErr *domain.ArmHumanGateValidationError
	require.ErrorAs(t, err, &vErr, "a takeover must be refused as a named validation failure, not silently")
	assert.Equal(t, "gate_author", vErr.Field)
	assert.Contains(t, vErr.Message, "human-gate-decisions",
		"the refusal must name the exit that IS open: answer the ask, don't seize it")

	require.NotNil(t, repo.items[taskID].GateAuthor)
	assert.Equal(t, original, *repo.items[taskID].GateAuthor,
		"and authorship must be unchanged — a refusal that still wrote the column would be no guard at all")
}

// TestArmHumanGate_APIReArmOverUserGate_Refused is the case with teeth: a human armed
// this gate. Seizing it would convert "only a human may release this" into "any agent
// may, in two calls".
func TestArmHumanGate_APIReArmOverUserGate_Refused(t *testing.T) {
	svc, repo, taskID := newArmingTestService(t)
	pavel := uuid.New()
	seedLiveGate(repo, taskID, pavel, domain.ActorTypeUser)

	err := armAPI(svc, taskID, uuid.New())

	var vErr *domain.ArmHumanGateValidationError
	require.ErrorAs(t, err, &vErr)
	require.NotNil(t, repo.items[taskID].GateAuthorType)
	assert.Equal(t, domain.ActorTypeUser, *repo.items[taskID].GateAuthorType,
		"a human's gate stays a human's gate")
}

// TestArmHumanGate_APIReArmBySameAuthor_Allowed — the guard is about seizing someone
// else's ask, not about editing your own. An author refining their own reason, deadline
// or class must still be able to.
func TestArmHumanGate_APIReArmBySameAuthor_Allowed(t *testing.T) {
	svc, repo, taskID := newArmingTestService(t)
	author := uuid.New()
	seedLiveGate(repo, taskID, author, domain.ActorTypeAgent)

	require.NoError(t, armAPI(svc, taskID, author))
	require.NotNil(t, repo.items[taskID].GateReason)
	assert.Contains(t, *repo.items[taskID].GateReason, "нужен выбор варианта",
		"the re-arm went through and updated the ask")
}

// TestArmHumanGate_APIArmOnUngatedTask_Allowed — the ordinary first arm, which must not
// have become harder. Without this the guard could refuse everything and still look
// correct in the two tests above.
func TestArmHumanGate_APIArmOnUngatedTask_Allowed(t *testing.T) {
	svc, repo, taskID := newArmingTestService(t)
	author := uuid.New()

	require.NoError(t, armAPI(svc, taskID, author))
	assert.True(t, repo.items[taskID].HumanGate)
	require.NotNil(t, repo.items[taskID].GateAuthor)
	assert.Equal(t, author, *repo.items[taskID].GateAuthor)
}

// TestArmHumanGate_MarkerReArmByAnotherAgent_Allowed — scoped deliberately. A marker
// arm is a live ask arriving through a channel we do not control; refusing it is SILENT
// to its author, who posts the question, believes it was handed over, and watches the
// card keep being fed (#58a6f4ff, #f421ad57). It needs no guard anyway: a marker-armed
// gate is released via the comment scan, which reads the thread, not gate_author.
func TestArmHumanGate_MarkerReArmByAnotherAgent_Allowed(t *testing.T) {
	svc, repo, taskID := newArmingTestService(t)
	seedLiveGate(repo, taskID, uuid.New(), domain.ActorTypeAgent)
	second := uuid.New()

	err := svc.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID:     taskID,
		Author:     second,
		AuthorType: domain.ActorTypeAgent,
		Reason:     "подтверждаю, вопрос ещё актуален",
		Source:     domain.ArmHumanGateSourceMarker,
	})
	require.NoError(t, err, "a marker arm must never be refused — the refusal is invisible to its author")
	require.NotNil(t, repo.items[taskID].GateAuthor)
	assert.Equal(t, second, *repo.items[taskID].GateAuthor)
}

// TestArmHumanGate_APIArm_TaskReadFails_Refused — fail-closed, unlike the disposable
// guard next to it. An API arm is refused LOUDLY (the caller sees the error and can
// retry), so "could not verify" is cheap here and a slipped takeover is not.
//
// Fails ONLY GetByID, not the whole repo. The first draft of this test set
// errToReturn, which also fails the ArmHumanGate write — so the arm errored whether or
// not the read was ever consulted, and the test passed against origin/main, where this
// guard does not exist. A check that cannot go red is not a check; the narrow hook is
// what makes this one measure the guard rather than the mock.
func TestArmHumanGate_APIArm_TaskReadFails_Refused(t *testing.T) {
	svc, repo, taskID := newArmingTestService(t)
	repo.getByIDErr = errors.New("db down")

	err := armAPI(svc, taskID, uuid.New())
	require.Error(t, err, "an arm that could not verify ownership must not be written")
	assert.Contains(t, err.Error(), "db down")
	assert.False(t, repo.items[taskID].HumanGate,
		"and the gate must not be armed anyway — the refusal has to precede the write")
}
