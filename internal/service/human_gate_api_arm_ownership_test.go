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
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
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
// scanHumanGateOwnership used to read ONE page (PageSize=100, ascending, no
// loop). On a longer thread a live "❓ Blocking @pavel" past comment 100 was
// invisible to it, and a marker arm ALSO writes gate_author for the marker's
// own poster. Those two facts together would have let that agent clear a
// real, still-unanswered ask to a human with its own key — the exact wall
// the fallback must not touch.
//
// Before the fallback existed the same blind spot merely misreported such a
// gate as unclearable: it failed CLOSED. It had to keep failing closed while
// the scan itself could not see past page 1, so "I could not read the whole
// thread" was not allowed to become "it is yours" — see
// TestGetHumanGateOwner_MarkerPastFirstPage_NowFound below for the other
// side of the same coin, now that the scan CAN read the whole thread.
//
// Fixed 2026-09-09 (task #3b921ba7): scanHumanGateOwnership now walks every
// page until the repository says there is nothing left, so "no marker found"
// on a long thread is a real fact about the thread again, not an artefact of
// where page 1 happened to end. The fallback below is the direct
// consequence: a long thread with genuinely no marker anywhere is no longer
// truncated, so gate_author is safe to trust — which is the whole point of
// reading the full thread rather than refusing on a maybe.
//
// seedManyComments fills a thread past one page so a single-page scan would
// have missed anything sitting after item 100.
func seedManyComments(env triageTestEnv, taskID uuid.UUID, n int) {
	for i := 0; i < n; i++ {
		cid := uuid.New()
		env.commentRepo.items[cid] = &domain.Comment{
			ID: cid, TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeAgent,
			Body: "ordinary progress note", CreatedAt: frozenTime.Add(time.Duration(i) * time.Minute),
		}
	}
}

// TestGetHumanGateOwner_LongThreadNoMarker_FallbackNowFires is the corrected
// shape of what used to be TestGetHumanGateOwner_TruncatedThread_
// FallbackRefusesToClaimOwner. The thread is genuinely marker-free — the scan
// now reads all 150 ordinary comments, confirms that fact, and the gate_author
// fallback is free to answer instead of refusing on an unresolved "maybe".
func TestGetHumanGateOwner_LongThreadNoMarker_FallbackNowFires(t *testing.T) {
	env := setupTriageEnv(t, true)
	author := uuid.New()
	taskID := seedAPIArmedTask(env, author, domain.ActorTypeAgent)
	seedManyComments(env, taskID, 150) // > the scan's old single-page PageSize of 100

	info, err := env.svc.GetHumanGateOwner(context.Background(), taskID)
	require.NoError(t, err)
	require.NotNil(t, info)

	require.NotNil(t, info.OwnerAgentID,
		"the whole thread was read and holds no marker — gate_author is now trustworthy")
	assert.Equal(t, author, *info.OwnerAgentID)
	assert.True(t, info.ClearableByOwner)
	assert.Equal(t, domain.HumanGateClearPathClearEndpoint, info.ClearPath)
	assert.Empty(t, info.ReasonIfNot)
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

// TestGetHumanGateOwner_TruncatedThread_MarkerFoundOnFirstPage_Unaffected — a marker
// the scan finds on the very first page must be judged exactly as before the fix,
// whether or not the thread continues beyond it.
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

// ---------------------------------------------------------------------------
// The mandatory red leg (task #3b921ba7): a marker sitting PAST comment 100
// ---------------------------------------------------------------------------

// seedMarkerPastPageOne builds a thread of n ordinary comments (chronologically
// first, so they occupy page 1 of a 100-per-page ascending scan) followed by one
// real "❓ Blocking @pavel" marker whose CreatedAt sorts strictly AFTER all of
// them — i.e. past position n in the thread, well past the old single page's
// first 100 items whenever n > 100. Returns the marker's own comment ID.
func seedMarkerPastPageOne(env triageTestEnv, taskID, authorID uuid.UUID, n int) uuid.UUID {
	seedManyComments(env, taskID, n)
	cid := uuid.New()
	env.commentRepo.items[cid] = &domain.Comment{
		ID: cid, TaskID: taskID, AuthorID: authorID, AuthorType: domain.ActorTypeAgent,
		Body:      "❓ **Blocking @pavel**: нужен выбор варианта A/Б",
		CreatedAt: frozenTime.Add(time.Duration(n) * time.Minute), // strictly after all n ordinary comments
	}
	return cid
}

// TestGetHumanGateOwner_MarkerPastFirstPage_NowFound is the acceptance test's own
// non-negotiable red leg: "тест с тредом >100 комментов, где маркер стоит ЗА сотым,
// должен падать на текущем коде и проходить после" (#3b921ba7). Without the loop in
// scanHumanGateOwnership this marker sits at position 151 of a 151-comment thread —
// entirely outside the single PageSize=100 page the old code read — so the scan
// found nothing, GetHumanGateOwner reported marker_scan_truncated, and the marker's
// own author had no way to see (let alone use) the withdrawal path. After the fix
// the scan walks every page and finds it.
func TestGetHumanGateOwner_MarkerPastFirstPage_NowFound(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	markerID := seedMarkerPastPageOne(env, taskID, askerID, 150)

	info, err := env.svc.GetHumanGateOwner(context.Background(), taskID)
	require.NoError(t, err)
	require.NotNil(t, info)

	require.NotNil(t, info.OwnerAgentID,
		"the marker is real and live — a page boundary must not hide it")
	assert.Equal(t, askerID, *info.OwnerAgentID)
	assert.True(t, info.ClearableByOwner)
	assert.Equal(t, domain.HumanGateClearPathWithdrawMarker, info.ClearPath)
	require.NotNil(t, info.MarkerCommentID)
	assert.Equal(t, markerID, *info.MarkerCommentID)
	assert.Empty(t, info.ReasonIfNot, "a found marker must not also claim the scan was truncated")
}

// TestReleaseHumanGateOnWithdrawal_MarkerPastFirstPage_StillReleases is acceptance
// criterion 2 from #3b921ba7: the withdrawal path itself (not just the read-only
// report) must reach a marker sitting past comment 100. Before the fix this failed
// SILENTLY — the withdrawal comment published normally, scanHumanGateOwnership
// found no owner past page 1, and releaseHumanGateOnWithdrawal's own !scan.found
// guard left the gate permanently stuck, indistinguishable on the wire from success.
func TestReleaseHumanGateOnWithdrawal_MarkerPastFirstPage_StillReleases(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	seedMarkerPastPageOne(env, taskID, askerID, 150)

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   askerID,
		AuthorType: domain.ActorTypeAgent,
		Body:       "Blocker самоустранился, ask не нужен — снимаю.",
		CreatedAt:  frozenTime.Add(200 * time.Minute), // after the marker and all ordinary comments
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	gateCalls := env.taskMover.humanGateCalls()
	require.Len(t, gateCalls, 1,
		"SetHumanGate must be called exactly once — a marker past page 1 must not leave the gate silently stuck")
	assert.Equal(t, taskID, gateCalls[0].taskID)
	assert.False(t, gateCalls[0].value, "gate must be cleared (value=false)")
}
