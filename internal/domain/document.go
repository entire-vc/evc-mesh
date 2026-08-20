package domain

import (
	"time"

	"github.com/google/uuid"
)

// DocumentSource says whether a document was written here or copied in from an
// external source. See migrations/20260820110_document_external_source.sql for
// why this is a discriminator column rather than something inferred from which
// of the source_* fields are NULL.
type DocumentSource string

const (
	DocumentSourceOwn       DocumentSource = "own"
	DocumentSourceTeamRelay DocumentSource = "team_relay"
)

// Document is a markdown page inside a project. Only its metadata lives in
// Postgres; the markdown itself is an object in S3/MinIO addressed by StorageKey.
//
// Documents form a tree through ParentID and are ordered among their siblings by
// Position. Deletes are soft so that a subtree can be restored intact.
type Document struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	ProjectID  uuid.UUID  `json:"project_id" db:"project_id"`
	ParentID   *uuid.UUID `json:"parent_id" db:"parent_id"`
	Slug       string     `json:"slug" db:"slug"`
	Title      string     `json:"title" db:"title"`
	StorageKey string     `json:"storage_key" db:"storage_key"`
	Position   int        `json:"position" db:"position"`

	// Version is a monotonic counter bumped by every write to the body or the
	// title. It is what makes a conditional write possible: a caller sends back
	// the version it read as base_version, and a write whose base_version no
	// longer matches is refused instead of silently overwriting somebody else's
	// edit. A move in the tree does not bump it — see
	// migrations/20260820101_document_version.sql.
	//
	// Always populated on a read, so every caller has the value it would need to
	// write safely without asking for it specially.
	Version int `json:"version" db:"version"`

	CreatedBy     uuid.UUID `json:"created_by" db:"created_by"`
	CreatedByType ActorType `json:"created_by_type" db:"created_by_type"`

	// UpdatedBy is who last changed the document. It is a pointer because the rows
	// that predate the column have no honest answer — see
	// migrations/20260819099_document_updated_by.sql for why they were not
	// back-filled with the creator. Every row written since is stamped, on create
	// as well as on every later mutation, so nil means "not recorded" and never
	// "never edited".
	UpdatedBy     *uuid.UUID `json:"updated_by" db:"updated_by"`
	UpdatedByType *ActorType `json:"updated_by_type" db:"updated_by_type"`

	CreatedAt time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt time.Time  `json:"updated_at" db:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty" db:"deleted_at"`

	// SourceKind, SourceShare, SourcePath, SourceSHA256 and SyncedAt describe
	// where this document came from. On an own document (the only kind this PR
	// can produce — nothing yet writes the others) SourceKind is "own" and the
	// rest are nil; on a copy all five are populated. See
	// migrations/20260820110_document_external_source.sql for the full
	// reasoning, including why this is enforced in the database rather than
	// trusted to whichever caller writes the row.
	SourceKind   DocumentSource `json:"source_kind" db:"source_kind"`
	SourceShare  *string        `json:"source_share,omitempty" db:"source_share"`
	SourcePath   *string        `json:"source_path,omitempty" db:"source_path"`
	SourceSHA256 *string        `json:"source_sha256,omitempty" db:"source_sha256"`
	SyncedAt     *time.Time     `json:"synced_at,omitempty" db:"synced_at"`

	// ExternalAuthor is who the source says wrote the page, as free text — Team
	// Relay names no per-file author anywhere in its sync or web-publish APIs
	// (verified against entire-vc/evc-team-relay@main, 2026-08-20), so this is
	// nil on every copy today. It is not a foreign key: the author is not a
	// principal in this workspace and has no rights to grant.
	ExternalAuthor *string `json:"external_author,omitempty" db:"external_author"`

	// Computed (not DB columns — populated via correlated subqueries in SELECT,
	// the same arrangement as Comment.AuthorName).
	//
	// Resolved at read time rather than denormalized at write time on purpose: a
	// stored copy of a display name freezes it, so somebody who renames themselves
	// keeps their old name on every document they ever touched. Reading through to
	// the actor heals history instead of preserving it wrong.
	//
	// nil where no name can be resolved: an unstamped legacy row, a system actor,
	// or a principal that has since been deleted.
	CreatedByName *string `json:"created_by_name,omitempty" db:"created_by_name"`
	UpdatedByName *string `json:"updated_by_name,omitempty" db:"updated_by_name"`

	// Body is the markdown fetched from object storage. There is no such column:
	// it is populated only by the reads that ask for it (a single document), so a
	// list of 200 documents is not 200 round-trips to S3. Same arrangement as
	// Artifact.StorageURL — a domain field the storage layer fills in.
	Body string `json:"body,omitempty" db:"-"`
}

// DocumentSearchHit is one result of a full-text search over document content.
//
// It is not a Document: a hit carries a snippet and a rank that exist only for
// this query, and it deliberately omits the body and the storage key so a search
// result cannot be mistaken for the document itself.
type DocumentSearchHit struct {
	ID        uuid.UUID `json:"id" db:"id"`
	ProjectID uuid.UUID `json:"project_id" db:"project_id"`
	Title     string    `json:"title" db:"title"`
	Slug      string    `json:"slug" db:"slug"`

	// Snippet is a fragment of the document with the matched words marked, or —
	// when the match lies past the window ts_headline was given — the opening of
	// the document with nothing marked.
	Snippet string `json:"snippet" db:"snippet"`
	// SnippetIsMatch says which of those two the snippet is.
	//
	// Without it the caller cannot tell a fragment containing the match from the
	// document's first sentence, and would highlight both. Showing a reader the
	// start of a document as though it were the reason the document matched is a
	// small lie that makes the whole result list untrustworthy.
	SnippetIsMatch bool `json:"snippet_is_match"`

	Rank float64 `json:"rank" db:"rank"`
}
