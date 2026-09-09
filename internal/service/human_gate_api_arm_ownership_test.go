package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Task #f933dc05. A gate armed through POST /tasks/:id/human-gate leaves no marker
// comment, so scanHumanGateOwnership — which reads the THREAD — found nothing and
// reported "no_live_marker", which every caller (and the refusal message itself) took
// to mean "armed without an ask an author could withdraw — there is no withdrawal path
// here by construction". That sentence is true of a raw PATCH/UI arm and was never true
// of an API arm: since task #4545660b the author of an API arm is written onto the task
// row as gate_author, from the caller's AUTHENTICATED identity. The server knew the
// author and looked in the wrong column.
//
// Every test below asserts a PAIR that must not come apart:
//  1. the API-armed shape now HAS an owner, with the clear_endpoint path named, and
//  2. every other shape reports exactly what it reported before.
//
// (1) alone would be indistinguishable from "ownership was opened up to everyone" —
// which is why the user-armed, authorless and marker-armed controls below carry the
// weight, not the new capability.

// seedAPIArmedTask is the shape this card is about: human_gate=true, gate_author
// recorded, and NO comment in the thread at all.
func seedAPIArmedTask(env triageTestEnv, author uuid.UUID, authorType domain.ActorType) uuid.UUID {
	taskID := uuid.New()
	at := authorType
	env.taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: env.projID, StatusID: env.inProgressID,
		Title: "API-armed", HumanGate: true,
		GateAuthor: &author, GateAuthorType: &at,
	}
	return taskID
}

func TestGetHumanGateOwner_APIArmedByAgent_OwnedAndClearableViaEndpoint(t *testing.T) {
	env := setupTriageEnv(t, true)
	author := uuid.New()
	taskID := seedAPIArmedTask(env, author, domain.ActorTypeAgent)

	info, err := env.svc.GetHumanGateOwner(context.Background(), taskID)
	require.NoError(t, err)
	require.NotNil(t, info)

	assert.True(t, info.Gated)
	require.NotNil(t, info.OwnerAgentID, "the recorded gate_author owns an API arm")
	assert.Equal(t, author, *info.OwnerAgentID)
	assert.True(t, info.ClearableByOwner)
	assert.Empty(t, info.ReasonIfNot, "an owner was found, so nothing is blocking the clear")
	assert.Equal(t, domain.HumanGateClearPathClearEndpoint, info.ClearPath,
		"the withdrawal-comment path is structurally unreachable here — naming it would "+
			"send the owner to a door that fails silently")
	assert.Nil(t, info.MarkerCommentID, "there is no marker comment to point at")
}

// TestGetHumanGateOwner_APIArmedByUser_StaysHumanOnly is the control that carries the
// weight. A human armed this gate; an agent clearing it with its own key would bypass
// not a technical limit but the POINT of the control. Same fixture as the test above
// apart from gate_author_type — if this ever reports an owner, the fix has become a
// permission widening.
func TestGetHumanGateOwner_APIArmedByUser_StaysHumanOnly(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := seedAPIArmedTask(env, uuid.New(), domain.ActorTypeUser)

	info, err := env.svc.GetHumanGateOwner(context.Background(), taskID)
	require.NoError(t, err)
	require.NotNil(t, info)

	assert.Nil(t, info.OwnerAgentID)
	assert.False(t, info.ClearableByOwner)
	assert.Equal(t, "no_live_marker", info.ReasonIfNot)
	assert.Empty(t, info.ClearPath)
}

// TestGetHumanGateOwner_APIArmedBySystem_StaysHumanOnly — a system arm has no session
// to withdraw it; it is resolved by whatever mechanism raised it, or by a human.
func TestGetHumanGateOwner_APIArmedBySystem_StaysHumanOnly(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := seedAPIArmedTask(env, uuid.New(), domain.ActorTypeSystem)

	info, err := env.svc.GetHumanGateOwner(context.Background(), taskID)
	require.NoError(t, err)
	assert.Nil(t, info.OwnerAgentID)
	assert.False(t, info.ClearableByOwner)
	assert.Equal(t, "no_live_marker", info.ReasonIfNot)
}

// TestGetHumanGateOwner_AuthorlessRawArm_Unchanged pins the ONE shape for which
// "no withdrawal path by construction" was always the literal truth: gate_author is
// NULL because a raw PATCH/UI arm has no author to record. Covered already by
// TestGetHumanGateOwner_NoLiveMarker_ReasonNoLiveMarker; restated here because that
// test's fixture would keep passing for the wrong reason if the fallback ever started
// inventing an owner from an empty column.
func TestGetHumanGateOwner_AuthorlessRawArm_Unchanged(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)

	info, err := env.svc.GetHumanGateOwner(context.Background(), taskID)
	require.NoError(t, err)
	assert.Nil(t, info.OwnerAgentID)
	assert.False(t, info.ClearableByOwner)
	assert.Equal(t, "no_live_marker", info.ReasonIfNot)
	assert.Empty(t, info.ClearPath)

	// And the nil-UUID variant, which a partially-written row could produce.
	nilAuthor := uuid.Nil
	agent := domain.ActorTypeAgent
	env.taskRepo.items[taskID].GateAuthor = &nilAuthor
	env.taskRepo.items[taskID].GateAuthorType = &agent
	info, err = env.svc.GetHumanGateOwner(context.Background(), taskID)
	require.NoError(t, err)
	assert.Nil(t, info.OwnerAgentID, "uuid.Nil is not an owner")
	assert.False(t, info.ClearableByOwner)
}

// TestGetHumanGateOwner_MarkerArmed_KeepsWithdrawPath is the second load-bearing
// control: a real "❓ Blocking @pavel" ask must still be reported as withdraw_marker,
// NOT as clear_endpoint. A change that simply opened the DELETE endpoint to every
// owner would pass the first test in this file and fail here.
func TestGetHumanGateOwner_MarkerArmed_KeepsWithdrawPath(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)
	// The gate ALSO carries a recorded gate_author, as any post-#4545660b marker arm
	// does — so this test proves the marker branch wins over the fallback, rather
	// than merely never reaching it.
	agent := domain.ActorTypeAgent
	env.taskRepo.items[taskID].GateAuthor = &askerID
	env.taskRepo.items[taskID].GateAuthorType = &agent

	info, err := env.svc.GetHumanGateOwner(context.Background(), taskID)
	require.NoError(t, err)
	require.NotNil(t, info.OwnerAgentID)
	assert.Equal(t, askerID, *info.OwnerAgentID)
	assert.True(t, info.ClearableByOwner)
	assert.Equal(t, domain.HumanGateClearPathWithdrawMarker, info.ClearPath,
		"an ask raised in the thread comes down where it was raised")
	require.NotNil(t, info.MarkerCommentID)
}

// TestGetHumanGateOwner_TaskReadFails_FailsClosed — "I could not look" must not read
// as "I looked and it is yours".
func TestGetHumanGateOwner_TaskReadFails_FailsClosed(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := seedAPIArmedTask(env, uuid.New(), domain.ActorTypeAgent)
	env.taskRepo.errToReturn = errors.New("db down")

	info, err := env.svc.GetHumanGateOwner(context.Background(), taskID)
	require.NoError(t, err, "a read hiccup must not turn a GET into an error")
	require.NotNil(t, info)
	assert.Nil(t, info.OwnerAgentID)
	assert.False(t, info.ClearableByOwner)
	assert.Equal(t, "no_live_marker", info.ReasonIfNot)
}

// TestGetHumanGateOwner_TaskMissing_FailsClosed — the mock returns (nil, nil) for an
// unknown id, the same shape TaskRepo.GetByID produces for a deleted row.
func TestGetHumanGateOwner_TaskMissing_FailsClosed(t *testing.T) {
	env := setupTriageEnv(t, true)

	info, err := env.svc.GetHumanGateOwner(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.Nil(t, info.OwnerAgentID)
	assert.False(t, info.ClearableByOwner)
	assert.Equal(t, "no_live_marker", info.ReasonIfNot)
}

// ---------------------------------------------------------------------------
// The gate_author fallback must not fire on a thread the scan could not finish
// ---------------------------------------------------------------------------

// Found by adversarial verification of this card, not in production.
//
// scanHumanGateOwnership reads ONE page (PageSize=100, ascending, no loop). On a
// longer thread a live "❓ Blocking @pavel" past comment 100 is invisible to it, and
// a marker arm ALSO writes gate_author for the marker's own poster. Those two facts
// together would let that agent clear a real, still-unanswered ask to a human with
// its own key — the exact wall the fallback must not touch.
//
// Before the fallback existed the same blind spot merely misreported such a gate as
// unclearable: it failed CLOSED. It has to keep failing closed now that the answer
// grants a right, so "I could not read the whole thread" must never become "it is
// yours".
//
// seedManyComments fills a thread past one page so page.HasMore is true.
func seedManyComments(env triageTestEnv, taskID uuid.UUID, n int) {
	for i := 0; i < n; i++ {
		cid := uuid.New()
		env.commentRepo.items[cid] = &domain.Comment{
			ID: cid, TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeAgent,
			Body: "ordinary progress note", CreatedAt: frozenTime.Add(time.Duration(i) * time.Minute),
		}
	}
}

func TestGetHumanGateOwner_TruncatedThread_FallbackRefusesToClaimOwner(t *testing.T) {
	env := setupTriageEnv(t, true)
	author := uuid.New()
	taskID := seedAPIArmedTask(env, author, domain.ActorTypeAgent)
	seedManyComments(env, taskID, 150) // > the scan's PageSize of 100

	info, err := env.svc.GetHumanGateOwner(context.Background(), taskID)
	require.NoError(t, err)
	require.NotNil(t, info)

	assert.Nil(t, info.OwnerAgentID,
		"a marker could be sitting past the page this scan read — do not grant a clearing right on a maybe")
	assert.False(t, info.ClearableByOwner)
	assert.Empty(t, info.ClearPath)
	assert.Equal(t, "marker_scan_truncated", info.ReasonIfNot,
		"and say WHY, so this is not mistaken for the authorless raw-arm shape")
}

// TestGetHumanGateOwner_ShortThread_FallbackStillFires is the positive control that
// keeps the guard from being vacuous. Same fixture, same API arm, only the thread is
// short enough to have been read in full — the fallback must still work, or the guard
// would have quietly disabled the capability instead of bounding it.
func TestGetHumanGateOwner_ShortThread_FallbackStillFires(t *testing.T) {
	env := setupTriageEnv(t, true)
	author := uuid.New()
	taskID := seedAPIArmedTask(env, author, domain.ActorTypeAgent)
	seedManyComments(env, taskID, 40) // well inside one page

	info, err := env.svc.GetHumanGateOwner(context.Background(), taskID)
	require.NoError(t, err)
	require.NotNil(t, info.OwnerAgentID)
	assert.Equal(t, author, *info.OwnerAgentID)
	assert.True(t, info.ClearableByOwner)
	assert.Equal(t, domain.HumanGateClearPathClearEndpoint, info.ClearPath)
}

// TestGetHumanGateOwner_TruncatedThread_MarkerFoundOnFirstPage_Unaffected — the guard
// is scoped to the fallback only. A marker the scan DID find is judged exactly as
// before, truncation or not; widening the single-page scan is a separate, pre-existing
// defect and is deliberately not touched here.
func TestGetHumanGateOwner_TruncatedThread_MarkerFoundOnFirstPage_Unaffected(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID) // created 2h before frozenTime
	seedManyComments(env, taskID, 150)

	info, err := env.svc.GetHumanGateOwner(context.Background(), taskID)
	require.NoError(t, err)
	require.NotNil(t, info.OwnerAgentID)
	assert.Equal(t, askerID, *info.OwnerAgentID)
	assert.Equal(t, domain.HumanGateClearPathWithdrawMarker, info.ClearPath)
}
