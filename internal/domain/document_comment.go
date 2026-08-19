package domain

import (
	"time"

	"github.com/google/uuid"
)

// DocumentAnchor is where a comment thread is attached in a document body.
//
// The shape is ADR G1's {start, end, exact, prefix, suffix}, and it is the SAME
// one a paragraph link uses (web/src/lib/docs/anchor.ts): a comment range is the
// general case, and a linked paragraph is the range that happens to cover one
// whole block. One scheme, so a document has one answer to "where was this
// pointing" rather than two that can disagree.
//
// Start/End are half-open character offsets into the document's text projection
// — every top-level block's text, whitespace-collapsed, joined with newlines.
// They are a HINT: identity is carried by Exact, with Prefix/Suffix as the
// surrounding context. Offsets go stale on the first edit above the range; the
// quoted text does not. Resolution is therefore quote-first and refuses to guess
// (see resolveAnchor) — a resolver that trusted the offsets would silently move a
// comment onto whatever text now occupies that position, which is worse than
// losing the anchor, because it looks like it worked.
//
// Resolution itself is client-side: the body lives in object storage and the
// reader already has it parsed. Nothing on the server needs to resolve an anchor,
// so nothing here does.
type DocumentAnchor struct {
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Exact  string `json:"exact"`
	Prefix string `json:"prefix"`
	Suffix string `json:"suffix"`
}

// DocumentComment is one comment on a document: either a thread root, which owns
// an Anchor and can be resolved, or a reply, which owns neither and names its
// parent.
//
// The two are one table and one type because they are one thread. The invariants
// that keep them apart — a root has an anchor, a reply does not, only a root can
// be resolved — are CHECK constraints in migrations/20260819099, not conventions
// the service is trusted to remember.
type DocumentComment struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	DocumentID uuid.UUID  `json:"document_id" db:"document_id"`
	ParentID   *uuid.UUID `json:"parent_comment_id" db:"parent_comment_id"`
	Body       string     `json:"body" db:"body"`

	// Anchor is non-nil exactly when this is a thread root.
	Anchor *DocumentAnchor `json:"anchor,omitempty" db:"-"`

	// ResolvedAt marks the thread done. Roots only, and cleared together with
	// ResolvedBy/ResolvedByType — a timestamp without an actor would attribute a
	// resolution nobody made.
	ResolvedAt     *time.Time `json:"resolved_at,omitempty" db:"resolved_at"`
	ResolvedBy     *uuid.UUID `json:"resolved_by,omitempty" db:"resolved_by"`
	ResolvedByType *ActorType `json:"resolved_by_type,omitempty" db:"resolved_by_type"`

	AuthorID   uuid.UUID  `json:"author_id" db:"author_id"`
	AuthorType ActorType  `json:"author_type" db:"author_type"`
	CreatedAt  time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at" db:"updated_at"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty" db:"deleted_at"`
}

// IsRoot reports whether this comment owns the thread's anchor.
func (c *DocumentComment) IsRoot() bool { return c.ParentID == nil }

// IsResolved reports whether the thread has been marked done.
func (c *DocumentComment) IsResolved() bool { return c.ResolvedAt != nil }
