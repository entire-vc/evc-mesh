package domain

import (
	"time"

	"github.com/google/uuid"
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
