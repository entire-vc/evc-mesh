package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// HumanGateProvenance grades how a recorded decision came to exist. Never
// averaged or collapsed to a single "trusted" bucket — see the contract at
// docs/human-gate-decision-recorded.md §2 (P2) in the evc-mesh repo.
type HumanGateProvenance string

const (
	// HumanGateProvenanceDirect means Pavel acted inside Mesh (his own
	// comment). Unforgeable by an agent key: comment_handler.go derives
	// author_type from the authenticated identity.
	HumanGateProvenanceDirect HumanGateProvenance = "direct"
	// HumanGateProvenanceBridged means the Telegram bridge delivered the
	// decision, tied to a real chat_id/message_id.
	HumanGateProvenanceBridged HumanGateProvenance = "bridged"
	// HumanGateProvenanceAttested means an agent transcribed an answer from
	// an external channel. Forgeable by construction — the control is
	// visibility (always labeled) and revocability, never prevention.
	HumanGateProvenanceAttested HumanGateProvenance = "attested"
	// HumanGateProvenanceDefaultApplied means the OPPOSITE of the three above:
	// nobody answered by gate_deadline, and the gate's own pre-stated
	// recommended_default was applied mechanically by the default-on-timeout
	// sweep (task #060ccaae). Never conflate with attested — that means a human
	// answer was transcribed; this means no answer arrived at all.
	HumanGateProvenanceDefaultApplied HumanGateProvenance = "default_applied"
)

// HumanGateChannel is where the decision was actually communicated.
type HumanGateChannel string

const (
	HumanGateChannelMesh     HumanGateChannel = "mesh"
	HumanGateChannelTelegram HumanGateChannel = "telegram"
	HumanGateChannelChat     HumanGateChannel = "chat"
	HumanGateChannelVoice    HumanGateChannel = "voice"
	HumanGateChannelInPerson HumanGateChannel = "in-person"
)

// HumanGateDecision is one row of the append-only ledger backing the third
// human_gate exit: "the question was answered, the answer arrived through
// another channel" (task #c56339b1). A row with RevokesID nil is a decision;
// a row with RevokesID set is a revocation of the decision it names — never
// an edit of that decision's own fields (migration 20260821001 enforces this
// with a hard UPDATE-rejecting trigger).
type HumanGateDecision struct {
	ID            uuid.UUID            `json:"id" db:"id"`
	TaskID        uuid.UUID            `json:"task_id" db:"task_id"`
	QuestionRef   *uuid.UUID           `json:"question_ref,omitempty" db:"question_ref"`
	CanonicalKey  *string              `json:"canonical_key,omitempty" db:"canonical_key"`
	DecidedBy     uuid.UUID            `json:"decided_by" db:"decided_by"`
	Provenance    *HumanGateProvenance `json:"provenance,omitempty" db:"provenance"`
	Channel       *HumanGateChannel    `json:"channel,omitempty" db:"channel"`
	Quote         *string              `json:"quote,omitempty" db:"quote"`
	ChannelRef    json.RawMessage      `json:"channel_ref,omitempty" db:"channel_ref"`
	RecordedBy    *uuid.UUID           `json:"recorded_by,omitempty" db:"recorded_by"`
	RevokesID     *uuid.UUID           `json:"revokes_id,omitempty" db:"revokes_id"`
	RevokedReason *string              `json:"revoked_reason,omitempty" db:"revoked_reason"`
	CreatedAt     time.Time            `json:"created_at" db:"created_at"`

	// RevokedAt is populated at READ time from a later revocation row
	// targeting this decision — never stored on this row itself (contract
	// §3: revocation is a new row, not a rewrite). Nil on a decision that
	// has not been revoked, and always nil on a revocation row itself (a
	// revocation cannot itself be revoked). RevokedReason above doubles as
	// the revocation's own reason on a revocation row.
	RevokedAt *time.Time `json:"revoked_at,omitempty" db:"-"`
}

// IsDecision reports whether this row is a decision (as opposed to a
// revocation of one).
func (d *HumanGateDecision) IsDecision() bool { return d.RevokesID == nil }

// RecordHumanGateDecisionInput is the input to recording a new decision.
type RecordHumanGateDecisionInput struct {
	TaskID       uuid.UUID
	QuestionRef  *uuid.UUID
	CanonicalKey *string
	DecidedBy    uuid.UUID
	Provenance   HumanGateProvenance
	Channel      HumanGateChannel
	Quote        *string
	ChannelRef   json.RawMessage
	RecordedBy   *uuid.UUID
}

// RevokeHumanGateDecisionInput is the input to revoking an existing decision.
// RevokedByType MUST be the caller's AUTHENTICATED actor type — the handler
// layer is responsible for populating it from actorctx, mirroring
// task_handler.go's PATCH {human_gate:false} guard. The service enforces it
// as defense-in-depth, not as the sole check.
type RevokeHumanGateDecisionInput struct {
	DecisionID    uuid.UUID
	RevokedBy     uuid.UUID
	RevokedByType ActorType
	Reason        string
}
