package domain

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Error codes of a refused fenced write (
// docs/extensions/agents-me-contract.md "Fencing a stale worker").
const (
	// CodeCheckoutPreconditionFailed: the caller does not hold the named lock
	// (another generation, expired, released, force-released).
	CodeCheckoutPreconditionFailed = "checkout_precondition_failed"
	// CodeInputRevisionChanged: the assignment content changed since the caller
	// read input_revision.
	CodeInputRevisionChanged = "input_revision_changed"
)

// ResultFence is the optional precondition of a write that publishes a result
// (comment, artifact, finish, move). It names the caller's lock the way extend
// and release do (Token, or Holder + SessionID + Generation) and/or the
// input_revision the caller worked from. The write commits only if the fence
// holds under the same row lock as the write; otherwise nothing is written.
type ResultFence struct {
	TaskID uuid.UUID
	// Holder is the caller; required for the session+generation form.
	Holder                uuid.UUID
	Token                 *uuid.UUID
	SessionID             *uuid.UUID
	Generation            int64
	ExpectedInputRevision *int64
}

// NamesLock reports whether the fence names a checkout (not only a revision).
func (f *ResultFence) NamesLock() bool {
	return f != nil && (f.Token != nil || f.Generation > 0)
}

// ResultFenceState is the locked task row the fence is checked against.
type ResultFenceState struct {
	Holder        *uuid.UUID `db:"checked_out_by"`
	Token         *uuid.UUID `db:"checkout_token"`
	Expires       *time.Time `db:"checkout_expires"`
	SessionID     *uuid.UUID `db:"checkout_session_id"`
	Generation    int64      `db:"checkout_generation"`
	InputRevision int64      `db:"input_revision"`
	Now           time.Time  `db:"now"`
}

// Check returns nil when the fence holds on the row, else a 409 with a stable
// error code. It never reveals the current token.
func (f *ResultFence) Check(s ResultFenceState) error {
	if f == nil {
		return nil
	}
	if f.NamesLock() {
		live := s.Holder != nil && s.Expires != nil && s.Expires.After(s.Now)
		match := false
		switch {
		case !live:
		case f.Token != nil:
			match = s.Token != nil && *s.Token == *f.Token
		default:
			match = *s.Holder == f.Holder && s.Generation == f.Generation &&
				uuidPtrEqual(s.SessionID, f.SessionID)
		}
		if !match {
			return &apierror.Error{
				Code: http.StatusConflict, ErrorCode: CodeCheckoutPreconditionFailed,
				Message: "you no longer hold this checkout: the lock expired, was released or taken over; nothing was written",
				Context: map[string]any{"checkout_generation": s.Generation, "checkout_live": live},
			}
		}
	}
	if f.ExpectedInputRevision != nil && *f.ExpectedInputRevision != s.InputRevision {
		return &apierror.Error{
			Code: http.StatusConflict, ErrorCode: CodeInputRevisionChanged,
			Message: "the task's title, description, custom fields or delegation level changed since input_revision was read; nothing was written",
			Context: map[string]any{"input_revision": s.InputRevision},
		}
	}
	return nil
}

func uuidPtrEqual(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
