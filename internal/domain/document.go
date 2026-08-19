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

	CreatedBy     uuid.UUID  `json:"created_by" db:"created_by"`
	CreatedByType ActorType  `json:"created_by_type" db:"created_by_type"`
	CreatedAt     time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at" db:"updated_at"`
	DeletedAt     *time.Time `json:"deleted_at,omitempty" db:"deleted_at"`

	// Body is the markdown fetched from object storage. There is no such column:
	// it is populated only by the reads that ask for it (a single document), so a
	// list of 200 documents is not 200 round-trips to S3. Same arrangement as
	// Artifact.StorageURL — a domain field the storage layer fills in.
	Body string `json:"body,omitempty" db:"-"`
}
