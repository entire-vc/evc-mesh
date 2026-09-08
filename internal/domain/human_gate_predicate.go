package domain

import (
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// CopyTierA and CopyTierB are the two values GateArmPredicate.CopyTier accepts
// (task 1.17a, §1r.A). Tier A is externally-visible or legally-binding copy — site
// marketing/legal prose, external comms, a claim or promise — and genuinely needs
// Pavel. Tier B is an interface label the product lead ships without him: a field
// caption, a menu item, a system/validation message, a section name, an existing
// dictionary string.
const (
	CopyTierA = "A"
	CopyTierB = "B"
)

// copyApprovalReasonRegex flags an ask that is ABOUT visible product copy — the shape
// §1r.A splits into tier A (Pavel decides) and tier B (the lead ships it and Pavel sees
// it after the fact). When an ask's reason matches, CopyTier is no longer optional:
// Decide cannot classify a copy ask correctly without it. Motivating case: #6a0c7ae3,
// a real gate that sat open for five days on exactly a tier-B ask ("апрув видимого
// текста: подписи пунктов меню и диалога") before a human closed it by hand as a false
// gate — this predicate is what makes that refusal automatic instead.
//
// Deliberately fails OPEN: a reason that does not match leaves CopyTier optional and
// Decide runs the ordinary four-question check, unchanged. A prose regex over free text
// is necessarily incomplete, and the asymmetry that matters is which side is cheap here.
// Missing a real copy ask costs one avoidable gate — visible, correctable in the log.
// A false match on a non-copy ask costs a blocked legitimate ask — invisible to
// everyone but its author. Same reasoning §5d3dc714 already applied to the
// four-question predicate itself; see that task and this file's package doc.
//
// \p{L} (not \w) on the Cyrillic stems below: Go's regexp \w is ASCII-only and would
// silently stop matching at the first non-ASCII letter, which is every letter here.
var copyApprovalReasonRegex = regexp.MustCompile(
	`(?i)апрув\p{L}*\s+(видимого\s+)?текст|копирайт|видим\p{L}+\s+текст|формулировк|` +
		`подпис\p{L}+\s+(пол|пункт|кнопк|раздел)|текст\s+(кнопк|пункт|формы|модалк|письма|рассылк)|` +
		`словар|локализ|FAQ|wording|copy approval`,
)

// RequiresCopyTier reports whether an ask's reason is shaped like a copy-approval
// question (see copyApprovalReasonRegex). When true, GateArmPredicate.CopyTier is no
// longer an optional field for an API-sourced arm.
func RequiresCopyTier(reason string) bool {
	return copyApprovalReasonRegex.MatchString(reason)
}

// GateArmPredicate is the four questions an agent must answer before asking a human
// (task #5d3dc714, audit §3.2b). The audit measured that 40-45% of asks to Pavel were
// cases the agent could have decided from a rule already written down: an access that
// was already in keys.env, its own 403 read as a human's decision, an approval Pavel had
// already declined, or waiting on someone else's card instead of adding a dependency.
//
// Each answer carries one line of justification. That is not ceremony: a bare bool is
// unreviewable, and the whole failure being fixed is agents answering these questions
// implicitly, in their heads, and getting them wrong. Writing the reason down is what
// makes a wrong answer visible afterwards in gate_predicate_log.
type GateArmPredicate struct {
	// CredentialExists — do I already hold the credential or access this needs?
	CredentialExists bool   `json:"credential_exists"`
	CredentialReason string `json:"credential_reason"`
	// Reversible — is there a rollback anchor (git, backup, snapshot, image tag)?
	Reversible       bool   `json:"reversible"`
	ReversibleReason string `json:"reversible_reason"`
	// BlockedByOtherTask — is the thing I'm waiting on actually someone else's card?
	BlockedByOtherTask bool   `json:"blocked_by_other_task"`
	BlockedReason      string `json:"blocked_reason"`
	// CustomerVisibleNow — does this change what a customer sees or pays RIGHT NOW?
	CustomerVisibleNow bool   `json:"customer_visible_now"`
	CustomerReason     string `json:"customer_reason"`
	// CopyTier — optional, CopyTierA or CopyTierB (task 1.17a, §1r.A). Left empty when
	// the ask is not about visible product copy at all. RequiresCopyTier decides when
	// empty stops being a legitimate answer; ArmHumanGateInput.Validate enforces that,
	// not this type's own Validate, because the check needs the ask's Reason, which
	// lives one level up.
	CopyTier string `json:"copy_tier,omitempty"`
}

// GatePredicateOutcome is what the predicate decided.
type GatePredicateOutcome string

const (
	// GatePredicateAllowed — at least one answer is a genuine stop; the ask stands.
	GatePredicateAllowed GatePredicateOutcome = "allowed"
	// GatePredicateRefusedSelfServe — nothing here needs a human. Reversibility is the
	// license to act, not the human's approval.
	GatePredicateRefusedSelfServe GatePredicateOutcome = "refused_self_serve"
	// GatePredicateRefusedUseDependency — this is a real blocker, but it lives on
	// ANOTHER card. The freeze belongs on a `blocks` edge, which stops the feed without
	// adding anything to a human's queue; a gate here asks a person about a state they
	// have already been asked about.
	GatePredicateRefusedUseDependency GatePredicateOutcome = "refused_use_dependency"
	// GatePredicateRefusedCopyTierB — the ask is tier-B interface copy (§1r.A): a field
	// caption, menu item, system/validation message, section name, or an existing
	// dictionary string. The product lead ships it without asking Pavel; he sees it
	// after the fact in the weekly digest and corrects it with a follow-up card if
	// needed. Kept as its own outcome, never merged into refused_self_serve — the two
	// populations answer different questions and merging them would make the
	// gate_predicate_log ratio measurement (#7084b912) unreadable.
	GatePredicateRefusedCopyTierB GatePredicateOutcome = "refused_copy_tier_b"
)

// Decide applies the gate. Order is deliberate: the dependency case is checked FIRST,
// because "someone else's card" and "reversible" are frequently both true and the
// dependency is the more specific, more useful answer.
//
// ⚠️ DELIBERATE DEVIATION FROM THE CARD, flagged rather than made silently.
// The card specifies the self-serve refusal as:
//
//	reversible && !customer_visible_now && !blocked_by_other_task
//
// which omits CredentialExists. Implemented literally, that refuses the arm of an agent
// that states "the action is reversible, but I do not have the credential" — and an
// agent with no credential cannot act at all, so the refusal would leave it with no exit:
// it may not ask, and it cannot do. That is the silently-dropped-ask failure this whole
// area keeps producing (#58a6f4ff, #f421ad57), manufactured by the very guard meant to
// reduce asks.
//
// So CredentialExists is included in the conjunction: a missing credential is a genuine
// stop and the ask stands. Reverting to the card's literal three-term rule is a one-line
// change (drop the first clause). Raised on the card for the author to accept or reject
// rather than quietly shipped either way.
func (p GateArmPredicate) Decide() GatePredicateOutcome {
	// Checked before everything else, including the dependency case: tier B is a
	// policy statement independent of the other four answers ("ship it yourself",
	// full stop) — whether the change also happens to be reversible or blocked on
	// another card doesn't change what the author should do next.
	if p.CopyTier == CopyTierB {
		return GatePredicateRefusedCopyTierB
	}
	if p.BlockedByOtherTask {
		return GatePredicateRefusedUseDependency
	}
	if p.CredentialExists && p.Reversible && !p.CustomerVisibleNow {
		return GatePredicateRefusedSelfServe
	}
	return GatePredicateAllowed
}

// Validate checks every answer carries a justification. Returns
// *ArmHumanGateValidationError naming the field, so the refusal says what to write.
//
// A whitespace-only reason is rejected the same as an empty one: " " satisfies a
// non-empty check while telling a reviewer nothing, and this log is only worth keeping
// if the reasons in it can be read.
func (p GateArmPredicate) Validate() error {
	for _, f := range []struct{ field, reason string }{
		{"predicate.credential_reason", p.CredentialReason},
		{"predicate.reversible_reason", p.ReversibleReason},
		{"predicate.blocked_reason", p.BlockedReason},
		{"predicate.customer_reason", p.CustomerReason},
	} {
		if strings.TrimSpace(f.reason) == "" {
			return &ArmHumanGateValidationError{
				Field:   f.field,
				Message: "required — one line saying why you answered as you did",
			}
		}
	}
	if p.CopyTier != "" && p.CopyTier != CopyTierA && p.CopyTier != CopyTierB {
		return &ArmHumanGateValidationError{
			Field:   "predicate.copy_tier",
			Message: "must be \"A\" or \"B\" when set",
		}
	}
	return nil
}

// RefusalMessage is what the caller is told. It names the exit that is actually open to
// them, because a refusal that only says "no" gets retried verbatim or read as "I am not
// allowed to raise this at all" — the misreading that turns a guard into an escalation.
func (o GatePredicateOutcome) RefusalMessage() string {
	switch o {
	case GatePredicateRefusedSelfServe:
		return "the predicate says nobody needs to be asked: you hold the credential, " +
			"the action is reversible, and nothing a customer sees or pays changes right " +
			"now. Capture a rollback anchor and do it. Reversibility is the license to " +
			"act — a human's approval is not required for work you can undo"
	case GatePredicateRefusedUseDependency:
		return "this blocker lives on another task: add_dependency(this_task, " +
			"depends_on=<that task>) instead. A `blocks` edge onto a still-open blocker " +
			"freezes the feed by itself and adds NOTHING to a human's queue, whereas a " +
			"gate asks a person about a state they have already been asked about"
	case GatePredicateRefusedCopyTierB:
		return "тир B (подписи полей, пункты меню, системные и валидационные сообщения, " +
			"названия разделов, отдельные строки существующего словаря) шипит лид " +
			"продукта — отгружай сам, поставь карточке метку `copy:b` и процитируй " +
			"строку в закрывающем комментарии: Павел увидит правку в недельной сводке и " +
			"поправит follow-up-карточкой (§1r.A)"
	default:
		return ""
	}
}

// GatePredicateLogEntry is one recorded evaluation — allowed and refused alike. Both are
// stored because the acceptance question is a RATIO: counting only refusals measures how
// often the guard fired, which reads the same whether it is preventing everything or
// nothing.
type GatePredicateLogEntry struct {
	ID        uuid.UUID `json:"id" db:"id"`
	TaskID    uuid.UUID `json:"task_id" db:"task_id"`
	ActorID   uuid.UUID `json:"actor_id" db:"actor_id"`
	ActorType ActorType `json:"actor_type" db:"actor_type"`
	// 'allowed' | 'refused_self_serve' | 'refused_use_dependency' | 'refused_copy_tier_b'.
	Outcome GatePredicateOutcome `json:"outcome" db:"outcome"`

	CredentialExists   bool `json:"credential_exists" db:"credential_exists"`
	Reversible         bool `json:"reversible" db:"reversible"`
	BlockedByOtherTask bool `json:"blocked_by_other_task" db:"blocked_by_other_task"`
	CustomerVisibleNow bool `json:"customer_visible_now" db:"customer_visible_now"`

	CredentialReason string `json:"credential_reason" db:"credential_reason"`
	ReversibleReason string `json:"reversible_reason" db:"reversible_reason"`
	BlockedReason    string `json:"blocked_reason" db:"blocked_reason"`
	CustomerReason   string `json:"customer_reason" db:"customer_reason"`

	Source    ArmHumanGateSource `json:"source" db:"source"`
	CreatedAt time.Time          `json:"created_at" db:"created_at"`
}
