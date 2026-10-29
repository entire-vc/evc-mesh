package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// Where a card's start_after came from (decision of 28.10, doc an internal ticket).
const (
	StartAfterHumanExplicit = "human_explicit"
	StartAfterAgentEvidence = "agent_evidence"
	StartAfterLegacyUnknown = "legacy_unknown"
)

// Refusal codes of a start_after write, next to the typed-outcome ones.
const (
	CodeNaiveTime         = "naive_time"
	CodeRelativeTime      = "relative_time"
	CodeOriginForbidden   = "origin_forbidden"
	CodeClearNeedsCompare = "clear_requires_compare"
	CodeGenericRetry      = "generic_retry"
)

// StartAfterEvidence is what an agent's start_after rests on: the external
// event, where its deadline is published, and what happens at that moment.
// The rules are the ones of a time wait (see validateTimeEvidence).
type StartAfterEvidence struct {
	EventRef string `json:"event_ref"`
	Source   string `json:"source"`
	Why      string `json:"why"`
}

// StartAfterWrite is one requested change of start_after, built by the
// handler and checked by the service before anything is written.
type StartAfterWrite struct {
	// Raw is the value as sent; empty with Null for an explicit null.
	Raw  string
	Null bool
	// Origin is what the caller claims; only agent_evidence (or nothing) is
	// accepted from an agent.
	Origin   string
	Evidence *StartAfterEvidence
}

// StartAfterReceipt is the immutable record of one set or clear.
type StartAfterReceipt struct {
	ID               uuid.UUID  `json:"id" db:"id"`
	TaskID           uuid.UUID  `json:"task_id" db:"task_id"`
	Op               string     `json:"op" db:"op"`
	StartAfter       time.Time  `json:"start_after" db:"start_after"`
	Origin           string     `json:"origin" db:"origin"`
	ActorID          uuid.UUID  `json:"actor_id" db:"actor_id"`
	ActorType        ActorType  `json:"actor_type" db:"actor_type"`
	SourceOffset     *string    `json:"source_offset,omitempty" db:"source_offset"`
	EventRef         *string    `json:"event_ref,omitempty" db:"event_ref"`
	Source           *string    `json:"source,omitempty" db:"evidence_source"`
	Why              *string    `json:"why,omitempty" db:"evidence_why"`
	EvidenceStatus   *string    `json:"evidence_status,omitempty" db:"evidence_status"`
	ClearedReceiptID *uuid.UUID `json:"cleared_receipt_id,omitempty" db:"cleared_receipt_id"`
	IdempotencyKey   *string    `json:"idempotency_key,omitempty" db:"idempotency_key"`
	TaskVersion      *int64     `json:"task_version,omitempty" db:"task_version"`
	Reason           *string    `json:"reason,omitempty" db:"reason"`
	CreatedAt        time.Time  `json:"created_at" db:"created_at"`
}

// ParseStartAfter reads a start_after value. A person may give a bare date
// (YYYY-MM-DD, taken as UTC midnight, as before); an agent only an absolute
// RFC 3339 timestamp with Z or an offset. The offset it was given in is
// returned so it can be kept next to the normalized instant.
func ParseStartAfter(raw string, human bool) (time.Time, string, error) {
	s := strings.TrimSpace(raw)
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), t.Format("Z07:00"), nil
	}
	if _, err := time.Parse("2006-01-02T15:04:05", s); err == nil {
		return time.Time{}, "", outcomeErr(CodeNaiveTime, "start_after needs an explicit Z or UTC offset")
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		if human {
			return t, "", nil
		}
		return time.Time{}, "", outcomeErr(CodeNaiveTime, "start_after from an agent is an RFC 3339 timestamp with Z or an offset, not a bare date")
	}
	if _, err := time.ParseDuration(strings.TrimPrefix(s, "+")); err == nil || looksRelative(s) {
		return time.Time{}, "", outcomeErr(CodeRelativeTime, "start_after is an absolute timestamp; a relative time like \"6h\" is not accepted")
	}
	return time.Time{}, "", outcomeErr(CodeInvalidTarget, "start_after must be RFC 3339 (or YYYY-MM-DD for a person)")
}

func looksRelative(s string) bool {
	l := strings.ToLower(s)
	for _, w := range []string{"now", "tomorrow", "later", "in ", "next ", "hour", "day", "week", "min"} {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

// genericRetryWords name a timer, not an external event: a card that waits on
// one of them has nothing to wait for and must be refused.
var genericRetryWords = map[string]bool{
	"retry": true, "retries": true, "poll": true, "polling": true, "backoff": true,
	"recheck": true, "timer": true, "reminder": true, "later": true, "followup": true,
}

// ValidateStartAfterEvidence checks an agent's evidence exactly as a time
// wait's is checked.
func ValidateStartAfterEvidence(ev *StartAfterEvidence) error {
	if ev == nil {
		return outcomeErr(CodeMissingEvidence, "start_after from an agent needs start_after_evidence: event_ref, source and why")
	}
	ev.EventRef, ev.Source, ev.Why = strings.TrimSpace(ev.EventRef), strings.TrimSpace(ev.Source), strings.TrimSpace(ev.Why)
	offset := "Z"
	w := &TaskWait{EventRef: &ev.EventRef, Source: &ev.Source, Why: &ev.Why, UntilAtOffset: &offset}
	return validateTimeEvidence(w)
}

// refuseGenericRetry refuses an event_ref that is a retry or poll.
func refuseGenericRetry(eventRef string) error {
	for _, tok := range strings.FieldsFunc(strings.ToLower(eventRef), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		if genericRetryWords[tok] {
			return outcomeErr(CodeGenericRetry, "event_ref names a retry or poll, not an external event with a published deadline")
		}
	}
	return nil
}

// StartAfterCompareClear is the body of POST /tasks/:id/start-after/compare-clear.
type StartAfterCompareClear struct {
	ExpectedStartAfter *time.Time `json:"expected_start_after"`
	ExpectedVersion    *int64     `json:"expected_version"`
	// ExpectedOrigin and ScheduleReceiptID pin the exact schedule; an agent
	// must give them (a legacy_unknown date has no receipt to give).
	ExpectedOrigin    *string    `json:"expected_origin"`
	ScheduleReceiptID *uuid.UUID `json:"schedule_receipt_id"`
	IdempotencyKey    string     `json:"idempotency_key"`
	Reason            *string    `json:"reason"`
}

// Validate refuses a request that does not name what it expects to clear.
func (in *StartAfterCompareClear) Validate(human bool) error {
	in.IdempotencyKey = strings.TrimSpace(in.IdempotencyKey)
	switch {
	case in.ExpectedStartAfter == nil || in.ExpectedVersion == nil:
		return outcomeErr(CodeInvalidTarget, "compare-clear needs expected_start_after and expected_version")
	case in.IdempotencyKey == "" || len(in.IdempotencyKey) > 200:
		return outcomeErr(CodeInvalidTarget, "compare-clear needs an idempotency_key of at most 200 characters")
	case in.Reason != nil && len(*in.Reason) > MaxTimeEvidenceLen:
		return outcomeErr(CodeInvalidTarget, "reason is longer than 500 characters")
	case human:
		return nil
	case in.ExpectedOrigin == nil:
		return outcomeErr(CodeInvalidTarget, "compare-clear by an agent needs expected_origin")
	case *in.ExpectedOrigin != StartAfterLegacyUnknown && in.ScheduleReceiptID == nil:
		return outcomeErr(CodeInvalidTarget, "compare-clear by an agent needs schedule_receipt_id")
	}
	return nil
}

// Mismatch compares the request with the locked task. The instant is compared
// at the database's microsecond precision; version, origin and receipt catch
// the same instant set again by someone else (ABA).
func (in *StartAfterCompareClear) Mismatch(t *Task) *StartAfterConflict {
	same := t.StartAfter != nil && t.StartAfterOrigin != nil &&
		t.StartAfter.Truncate(time.Microsecond).Equal(in.ExpectedStartAfter.Truncate(time.Microsecond)) &&
		t.Version == *in.ExpectedVersion &&
		(in.ExpectedOrigin == nil || *in.ExpectedOrigin == *t.StartAfterOrigin) &&
		(in.ScheduleReceiptID == nil || (t.StartAfterReceiptID != nil && *t.StartAfterReceiptID == *in.ScheduleReceiptID))
	if same {
		return nil
	}
	return &StartAfterConflict{
		CurrentVersion: t.Version, CurrentStartAfter: t.StartAfter,
		CurrentOrigin: t.StartAfterOrigin, CurrentReceipt: t.StartAfterReceiptID,
	}
}

// StartAfterConflict is the 409 cas_conflict of a compare-clear: nothing was
// changed, and the caller re-reads before deciding anything.
type StartAfterConflict struct {
	CurrentVersion    int64      `json:"current_version"`
	CurrentStartAfter *time.Time `json:"current_start_after"`
	CurrentOrigin     *string    `json:"current_origin"`
	CurrentReceipt    *uuid.UUID `json:"current_receipt"`
}

func (e *StartAfterConflict) Error() string {
	return "cas_conflict: start_after changed since it was read"
}

// StartAfterClearResult is the answer of a compare-clear: its receipt, and
// Replayed when the same idempotency_key had already cleared it.
type StartAfterClearResult struct {
	Receipt  StartAfterReceipt `json:"receipt"`
	Replayed bool              `json:"replayed"`
	Version  int64             `json:"version"`
}
