package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// DocumentCommentAnchor locates a comment in the text of a document.
//
// It is the W3C Web Annotation selector pair, which is also the shape Hypothesis
// stores: Exact/Prefix/Suffix are a TextQuoteSelector, Start/End a
// TextPositionSelector. Both are kept, because neither works alone.
//
// Offsets alone are provably insufficient. They are true only of the exact
// revision they were taken from, and a document body here is one mutable object
// in storage with no revision to pin them to — inserting a character above a
// comment moves every anchor below it, and the row goes on pointing confidently
// at the wrong words. The quote is what survives an edit, because it says what
// the comment was about rather than where it sat, and Prefix/Suffix are what
// disambiguate a quote that occurs more than once.
//
// The quote alone is not enough either: it makes every render a full re-scan of
// the body, and a repeated quote needs a starting point to prefer the nearest
// match to.
//
// Start and End are BYTE offsets, half-open [Start, End) — matching memory_chunks
// and the way Postgres substring() and most tooling index text.
type DocumentCommentAnchor struct {
	Exact  string `json:"exact"`
	Prefix string `json:"prefix"`
	Suffix string `json:"suffix"`

	// Start and End are nil together when the anchor is orphaned: the quote is
	// still known, the position is not. See IsOrphaned.
	Start *int `json:"start"`
	End   *int `json:"end"`
}

// NewDocumentCommentAnchor builds an anchor, or nil when there is no quote to
// anchor to.
//
// A comment with no quote is not "an anchor with empty strings" — it is a comment
// on the document as a whole, or a reply inheriting its parent's anchor, and the
// two states have to stay distinguishable from an orphan (which has a quote and
// no position). Returning nil is what keeps them apart in one field.
func NewDocumentCommentAnchor(exact, prefix, suffix string, start, end *int) *DocumentCommentAnchor {
	if exact == "" {
		return nil
	}
	return &DocumentCommentAnchor{Exact: exact, Prefix: prefix, Suffix: suffix, Start: start, End: end}
}

// IsOrphaned reports whether the anchored text can no longer be located.
//
// An orphan is a quote with no position: the comment still says what it was
// written about, and the document no longer says where that is. It is
// representable by nulling the offsets rather than by a separate flag, so that
// "orphaned but here are the offsets" — a flag disagreeing with the stale numbers
// beside it, which a client would happily highlight — cannot be written down.
func (a *DocumentCommentAnchor) IsOrphaned() bool {
	return a != nil && a.Start == nil
}

// MarshalJSON emits the computed `orphaned` flag alongside the stored fields.
//
// It is computed on the way out rather than stored, for the same reason the
// column does not exist: one fact, one place. A client reading `orphaned` can
// never be told something the offsets contradict.
func (a DocumentCommentAnchor) MarshalJSON() ([]byte, error) {
	type anchor DocumentCommentAnchor
	return json.Marshal(struct {
		anchor
		Orphaned bool `json:"orphaned"`
	}{anchor(a), a.IsOrphaned()})
}

// DocumentComment is a Confluence-style comment on a document: usually anchored
// to a run of its text, optionally a reply to another comment, resolvable.
//
// It is not a domain.Comment. Those hang off a task (comments.task_id is NOT NULL)
// and every create runs through the task-workflow machinery in comment_service —
// triage enforcement, human gates, task snapshots — which has nothing to say about
// a typo in paragraph three.
//
// Threading is a single parent pointer, like Comment, and the service keeps it one
// level deep: a reply answers a top-level comment, and a reply to a reply is
// refused rather than silently flattened.
type DocumentComment struct {
	ID              uuid.UUID  `json:"id" db:"id"`
	DocumentID      uuid.UUID  `json:"document_id" db:"document_id"`
	ParentCommentID *uuid.UUID `json:"parent_comment_id" db:"parent_comment_id"`
	AuthorID        uuid.UUID  `json:"author_id" db:"author_id"`
	AuthorType      ActorType  `json:"author_type" db:"author_type"`
	Body            string     `json:"body" db:"body"`

	// Anchor is nil when the comment was never anchored — a comment on the document
	// as a whole, or a reply, which inherits its parent's anchor rather than
	// carrying a copy that could drift away from it.
	//
	// It is assembled from five columns rather than scanned, hence db:"-".
	Anchor *DocumentCommentAnchor `json:"anchor" db:"-"`

	ResolvedAt     *time.Time `json:"resolved_at" db:"resolved_at"`
	ResolvedBy     *uuid.UUID `json:"resolved_by" db:"resolved_by"`
	ResolvedByType *ActorType `json:"resolved_by_type" db:"resolved_by_type"`

	CreatedAt time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt time.Time  `json:"updated_at" db:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty" db:"deleted_at"`

	// Computed (not DB columns — populated via correlated subqueries in SELECT,
	// the same arrangement as Comment.AuthorName): read-time resolution, so a
	// renamed principal is renamed everywhere they ever commented rather than
	// frozen under the name they had at write time.
	AuthorName     *string `json:"author_name,omitempty" db:"author_name"`
	ResolvedByName *string `json:"resolved_by_name,omitempty" db:"resolved_by_name"`
}

// IsResolved reports whether the comment has been marked resolved.
func (c *DocumentComment) IsResolved() bool {
	return c != nil && c.ResolvedAt != nil
}

// IsReply reports whether the comment answers another one.
func (c *DocumentComment) IsReply() bool {
	return c != nil && c.ParentCommentID != nil
}
