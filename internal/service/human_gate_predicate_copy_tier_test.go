package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Task 1.17a (§1r.A machine gate). GateArmPredicate gains a fifth, optional field —
// copy_tier — and these four branches are the acceptance criteria verbatim:
//   1. copy_tier="B" → refused.
//   2. copy_tier="A" → passes through to the ordinary four-question check.
//   3. a copy-shaped ask with copy_tier omitted → refused, naming the field.
//   4. a non-copy ask with copy_tier omitted → unaffected (today's behavior).

// armWithPredicateAndReason is armWithPredicate with a caller-supplied reason, needed
// here because whether copy_tier is required at all depends on what the reason says.
func armWithPredicateAndReason(t *testing.T, reason string, p *domain.GateArmPredicate) (error, bool) {
	t.Helper()
	svc, repo, taskID := newArmingTestService(t)
	err := svc.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID:             taskID,
		Author:             uuid.New(),
		AuthorType:         domain.ActorTypeAgent,
		Reason:             reason,
		RecommendedDefault: "жду ответа до дедлайна",
		Source:             domain.ArmHumanGateSourceAPI,
		Predicate:          p,
	})
	return err, repo.items[taskID].HumanGate
}

// BRANCH 1 — copy_tier="B" refuses, regardless of the other four answers, and the
// refusal names the exit (ship it yourself, tag copy:b, quote it in the closing
// comment) rather than a bare "no".
func TestPredicate_CopyTierB_Refuses(t *testing.T) {
	p := basePredicate()
	p.CopyTier = domain.CopyTierB

	err, armed := armWithPredicate(t, &p)

	require.Error(t, err)
	var vErr *domain.ArmHumanGateValidationError
	require.ErrorAs(t, err, &vErr)
	assert.Equal(t, "predicate", vErr.Field)
	assert.Contains(t, vErr.Message, "copy:b")
	assert.Contains(t, vErr.Message, "§1r.A")
	assert.False(t, armed, "a refused arm must not have armed the gate anyway")
}

// BRANCH 1b — order: copy_tier="B" wins even when the other four answers would ALSO
// refuse via a different path (blocked_by_other_task). Tier B is a policy statement
// independent of the other four — mirrors TestPredicate_BlockedWins_WhenAlsoReversible's
// reasoning for the pre-existing dependency-vs-self-serve ordering.
func TestPredicate_CopyTierB_WinsOverBlockedByOtherTask(t *testing.T) {
	p := basePredicate()
	p.CopyTier = domain.CopyTierB
	p.BlockedByOtherTask = true
	p.BlockedReason = "waiting on #0104878c"

	err, armed := armWithPredicate(t, &p)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "copy:b",
		"tier B must win over the dependency answer — it's a ship-it-yourself policy, not a wait")
	assert.NotContains(t, err.Error(), "add_dependency")
	assert.False(t, armed)
}

// BRANCH 2 — copy_tier="A" passes through unaffected: it does NOT special-case the arm
// to always succeed or always fail. Proven both ways, so a future edit cannot special
// case A into either extreme without breaking a test.
func TestPredicate_CopyTierA_PassesThroughToOrdinaryCheck(t *testing.T) {
	t.Run("genuine stop still arms", func(t *testing.T) {
		p := basePredicate()
		p.CopyTier = domain.CopyTierA

		err, armed := armWithPredicate(t, &p)

		require.NoError(t, err, "tier A must not block an ask that is a genuine stop")
		assert.True(t, armed)
	})

	t.Run("self-serve is still refused", func(t *testing.T) {
		p := basePredicate()
		p.CopyTier = domain.CopyTierA
		p.Reversible = true
		p.ReversibleReason = "additive migration, goose down restores it"

		err, armed := armWithPredicate(t, &p)

		require.Error(t, err, "tier A must not exempt an otherwise self-serve ask")
		var vErr *domain.ArmHumanGateValidationError
		require.ErrorAs(t, err, &vErr)
		assert.Contains(t, vErr.Message, "Reversibility is the license to act")
		assert.False(t, armed)
	})
}

// BRANCH 3 — a copy-shaped ask with copy_tier omitted is refused, naming the field, so
// the caller learns what to write next rather than retrying the identical call. Uses the
// live #6a0c7ae3 reason verbatim (from Garfield's handoff comment on this task) as the
// negative control that matters most: this is the actual gate that sat open five days.
func TestPredicate_CopyTierRequired_OnCopyShapedReason(t *testing.T) {
	p := basePredicate()
	// CopyTier deliberately left unset.

	err, armed := armWithPredicateAndReason(t,
		"апрув видимого текста: подписи пунктов меню и диалога", &p)

	require.Error(t, err)
	var vErr *domain.ArmHumanGateValidationError
	require.ErrorAs(t, err, &vErr)
	assert.Equal(t, "predicate.copy_tier", vErr.Field)
	assert.Contains(t, vErr.Message, "A")
	assert.Contains(t, vErr.Message, "B")
	assert.False(t, armed, "a copy-shaped ask with no stated tier must not arm")
}

// BRANCH 4 — a non-copy ask with copy_tier omitted is unaffected: today's four-question
// behavior, unchanged. This is the fail-open guarantee from RequiresCopyTier's own doc
// comment, exercised end to end through ArmHumanGate rather than just at the regex.
func TestPredicate_CopyTierNotRequired_OnOrdinaryReason(t *testing.T) {
	p := basePredicate()
	// CopyTier deliberately left unset.

	err, armed := armWithPredicateAndReason(t, "мёржим сейчас или ждём зелёного CI?", &p)

	require.NoError(t, err, "an ordinary ask must not suddenly require copy_tier")
	assert.True(t, armed)
}

// copy_tier is a closed enum: anything other than "A", "B", or empty is refused up
// front, before Decide ever runs — the same way an out-of-range `class` is refused in
// parseSetHumanGateArgs on the MCP side.
func TestPredicate_CopyTierInvalidValue_Refused(t *testing.T) {
	p := basePredicate()
	p.CopyTier = "C"

	err, armed := armWithPredicate(t, &p)

	require.Error(t, err)
	var vErr *domain.ArmHumanGateValidationError
	require.ErrorAs(t, err, &vErr)
	assert.Equal(t, "predicate.copy_tier", vErr.Field)
	assert.False(t, armed)
}

// The marker path is exempt from copy_tier the same way it is exempt from the other
// four questions (TestPredicate_MarkerPathExempt): a live "❓ Blocking @pavel" comment
// carries no predicate at all, and refusing it would be silent to its author.
func TestPredicate_CopyTierRequired_DoesNotApplyToMarkerPath(t *testing.T) {
	svc, repo, taskID := newArmingTestService(t)

	err := svc.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID:     taskID,
		Author:     uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Reason:     "апрув видимого текста: подписи пунктов меню и диалога",
		Source:     domain.ArmHumanGateSourceMarker,
		// no Predicate — same as any other marker arm.
	})

	require.NoError(t, err, "a live marker must always deliver, even on a copy-shaped reason")
	assert.True(t, repo.items[taskID].HumanGate)
}

// gate_predicate_log must record the new outcome under its OWN name, never folded into
// refused_self_serve — merging the two populations would make the ratio measurement
// (#7084b912) unreadable, per this file's own package doc.
func TestGatePredicateLog_RecordsCopyTierBUnderItsOwnOutcome(t *testing.T) {
	svc, logRepo, taskID := newLoggingArmService(t)
	p := basePredicate()
	p.CopyTier = domain.CopyTierB

	err := svc.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID: taskID, Author: uuid.New(), AuthorType: domain.ActorTypeAgent,
		Reason:             "апрув видимого текста: подписи пунктов меню и диалога",
		RecommendedDefault: "закрываю сам под тиром B",
		Source:             domain.ArmHumanGateSourceAPI, Predicate: &p,
	})
	require.Error(t, err)

	got := logRepo.recorded()
	require.Len(t, got, 1)
	assert.Equal(t, domain.GatePredicateRefusedCopyTierB, got[0].Outcome)
	assert.NotEqual(t, domain.GatePredicateRefusedSelfServe, got[0].Outcome)
}
