package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Task #5d3dc714, audit §3.2b. 40-45% of asks to Pavel were cases the agent could have
// decided from a rule already written down: an access already sitting in keys.env, its
// own 403 read as a human's decision, an approval Pavel had already declined, or waiting
// on someone else's card instead of adding a dependency.
//
// The card names four branches, and each is asserted on TWO things that must not come
// apart: the outcome, AND whether the gate actually got armed. Asserting the 422 alone
// would pass on an implementation that returns an error and arms anyway — the wall has
// to stand, not just the message.

// basePredicate is a genuine stop (not reversible), i.e. an ask that should go through.
func basePredicate() domain.GateArmPredicate {
	return domain.GateArmPredicate{
		CredentialExists:   true,
		CredentialReason:   "token is in keys.env",
		Reversible:         false,
		ReversibleReason:   "an outbound payment cannot be un-sent",
		BlockedByOtherTask: false,
		BlockedReason:      "no other card owns this",
		CustomerVisibleNow: false,
		CustomerReason:     "gateway inactive, nobody can be charged",
	}
}

func armWithPredicate(t *testing.T, p *domain.GateArmPredicate) (error, bool) {
	t.Helper()
	svc, repo, taskID := newArmingTestService(t)
	err := svc.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID:             taskID,
		Author:             uuid.New(),
		AuthorType:         domain.ActorTypeAgent,
		Reason:             "мёржим сейчас или ждём?",
		RecommendedDefault: "жду ответа до дедлайна",
		Source:             domain.ArmHumanGateSourceAPI,
		Predicate:          p,
	})
	return err, repo.items[taskID].HumanGate
}

// BRANCH 1 — reversible, nothing customer-facing, nobody else's card → refuse.
// "Reversibility is the license to act, not the human's approval."
func TestPredicate_ReversibleAndSafe_RefusesToArm(t *testing.T) {
	p := basePredicate()
	p.Reversible = true
	p.ReversibleReason = "migration is additive and nullable; goose down restores it"

	err, armed := armWithPredicate(t, &p)

	require.Error(t, err)
	var vErr *domain.ArmHumanGateValidationError
	require.ErrorAs(t, err, &vErr)
	assert.Equal(t, "predicate", vErr.Field)
	assert.Contains(t, vErr.Message, "Reversibility is the license to act")
	assert.False(t, armed, "a refused arm must not have armed the gate anyway")
}

// BRANCH 2 — the blocker is on someone else's card → refuse, and name add_dependency.
// A `blocks` edge freezes the feed without adding anything to a human's queue; a gate
// here asks a person about a state they have already been asked about.
func TestPredicate_BlockedByOtherTask_DemandsDependencyNotGate(t *testing.T) {
	p := basePredicate()
	p.BlockedByOtherTask = true
	p.BlockedReason = "waiting on #0104878c, which Linus owns"

	err, armed := armWithPredicate(t, &p)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "add_dependency")
	assert.False(t, armed)
}

// BRANCH 2b — dependency beats self-serve when BOTH would fire. Order matters: the
// dependency answer is the more specific and more useful one, and an implementation that
// checked self-serve first would tell the agent to "just do it" about work that is
// literally blocked on another card.
func TestPredicate_BlockedWins_WhenAlsoReversible(t *testing.T) {
	p := basePredicate()
	p.BlockedByOtherTask = true
	p.BlockedReason = "waiting on #0104878c"
	p.Reversible = true
	p.ReversibleReason = "revertible by a migration down"

	err, armed := armWithPredicate(t, &p)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "add_dependency",
		"the dependency answer must win over 'just do it' — the work IS blocked")
	assert.NotContains(t, err.Error(), "Reversibility is the license")
	assert.False(t, armed)
}

// BRANCH 3 — a genuine stop → the ask stands and the gate arms. Without this, every
// assertion above would pass on an implementation that refuses everything.
func TestPredicate_GenuineStop_ArmsNormally(t *testing.T) {
	p := basePredicate()

	err, armed := armWithPredicate(t, &p)

	require.NoError(t, err, "an ask with a real stop must go through")
	assert.True(t, armed)
}

// BRANCH 3b — customer-visible right now is its own stop, even when everything is
// reversible and self-serviceable. This is the case the whole predicate must not
// over-refuse: money and things a customer sees still go to a human.
func TestPredicate_CustomerVisibleNow_ArmsEvenIfReversible(t *testing.T) {
	p := basePredicate()
	p.Reversible = true
	p.ReversibleReason = "config change, revertible in one commit"
	p.CustomerVisibleNow = true
	p.CustomerReason = "the VAT rate prints on invoices clients already download"

	err, armed := armWithPredicate(t, &p)

	require.NoError(t, err, "customer-visible-now is a stop regardless of reversibility")
	assert.True(t, armed)
}

// BRANCH 4 — a missing predicate on the API path is refused, naming the field.
func TestPredicate_MissingOnAPIPath_Refused(t *testing.T) {
	err, armed := armWithPredicate(t, nil)

	require.Error(t, err)
	var vErr *domain.ArmHumanGateValidationError
	require.ErrorAs(t, err, &vErr)
	assert.Equal(t, "predicate", vErr.Field)
	assert.False(t, armed)
}

// A bool with no stated reason is unreviewable, and unreviewable answers are exactly how
// these four questions came to be answered wrongly in agents' heads. Whitespace counts
// as absent: " " satisfies a non-empty check while telling a reader nothing.
func TestPredicate_ReasonRequiredPerAnswer(t *testing.T) {
	for _, tc := range []struct {
		name, wantField string
		blank           func(*domain.GateArmPredicate)
	}{
		{"credential", "predicate.credential_reason", func(p *domain.GateArmPredicate) { p.CredentialReason = "" }},
		{"reversible", "predicate.reversible_reason", func(p *domain.GateArmPredicate) { p.ReversibleReason = "  " }},
		{"blocked", "predicate.blocked_reason", func(p *domain.GateArmPredicate) { p.BlockedReason = "" }},
		{"customer", "predicate.customer_reason", func(p *domain.GateArmPredicate) { p.CustomerReason = "\t" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := basePredicate()
			tc.blank(&p)

			err, armed := armWithPredicate(t, &p)

			require.Error(t, err)
			var vErr *domain.ArmHumanGateValidationError
			require.ErrorAs(t, err, &vErr)
			assert.Equal(t, tc.wantField, vErr.Field)
			assert.False(t, armed)
		})
	}
}

// TestPredicate_MarkerPathExempt: a "❓ Blocking @pavel" comment carries no predicate and
// must still arm. Refusing it would be silent to its author — they post the question,
// believe it was handed over, and the card keeps being fed (#58a6f4ff, #f421ad57). The
// same asymmetry recommended_default already carries, and for the same reason.
func TestPredicate_MarkerPathExempt(t *testing.T) {
	svc, repo, taskID := newArmingTestService(t)

	err := svc.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID:     taskID,
		Author:     uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Reason:     "какой шлюз выбираем?",
		Source:     domain.ArmHumanGateSourceMarker,
		// no Predicate, no RecommendedDefault
	})

	require.NoError(t, err, "a live marker must always deliver")
	assert.True(t, repo.items[taskID].HumanGate)
}

// TestPredicate_DecideIsPureAndTotal walks the whole 2^4 truth table against the rule as
// stated, so a future edit to Decide cannot quietly change one corner. Written as an
// independent re-derivation of the intent rather than a copy of the implementation's
// expression — a table that mirrors the code proves only that the code equals itself.
func TestPredicate_DecideIsPureAndTotal(t *testing.T) {
	for i := 0; i < 16; i++ {
		cred := i&1 != 0
		rev := i&2 != 0
		blocked := i&4 != 0
		customer := i&8 != 0

		p := domain.GateArmPredicate{
			CredentialExists: cred, Reversible: rev,
			BlockedByOtherTask: blocked, CustomerVisibleNow: customer,
		}

		var want domain.GatePredicateOutcome
		switch {
		case blocked:
			// Someone else's card: always a dependency, never a gate.
			want = domain.GatePredicateRefusedUseDependency
		case customer:
			// A customer sees or pays right now: always a human.
			want = domain.GatePredicateAllowed
		case cred && rev:
			// Holds the access, can undo it, nobody is watching: just do it.
			want = domain.GatePredicateRefusedSelfServe
		default:
			// No credential, or no way back: a real stop.
			want = domain.GatePredicateAllowed
		}

		assert.Equalf(t, want, p.Decide(),
			"cred=%v rev=%v blocked=%v customer=%v", cred, rev, blocked, customer)
	}
}

// TestPredicate_MissingCredentialIsAStop is the DELIBERATE DEVIATION from the card,
// pinned by a test so it cannot be lost silently in a later edit.
//
// The card specifies the self-serve refusal as `reversible && !customer_visible_now &&
// !blocked_by_other_task`, omitting credential_exists. Implemented literally, an agent
// stating "the action is reversible, but I do not have the credential" is REFUSED — and
// it cannot act either, so it has no exit at all: may not ask, cannot do. That is the
// silently-dropped-ask failure this area keeps producing, manufactured by the very guard
// meant to reduce asks.
//
// Raised on the card for the author to accept or reject. If rejected, delete this test
// and drop the first clause of Decide — one line each.
func TestPredicate_MissingCredentialIsAStop(t *testing.T) {
	p := basePredicate()
	p.CredentialExists = false
	p.CredentialReason = "no key for the Точка gateway anywhere in keys.env"
	p.Reversible = true
	p.ReversibleReason = "the config edit is revertible"

	err, armed := armWithPredicate(t, &p)

	require.NoError(t, err,
		"an agent that cannot obtain the credential must still be able to ask — "+
			"refusing here leaves it unable to ask AND unable to act")
	assert.True(t, armed)
}
