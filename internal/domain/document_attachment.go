package domain

import (
	"time"

	"github.com/google/uuid"
)

// DocumentAttachment is a file uploaded into a document — an image a markdown
// body references, or any other file attached to the page. The bytes are an
// object in S3/MinIO addressed by StorageKey; this is the record that names it.
//
// Deletes are soft, mirroring Document: a restored document has to come back with
// its images intact.
//
// UploadedByType is an ActorType, not the UploaderType that artifacts use. The two
// are different enumerations over the same idea, and the column here is the
// actor_type Postgres enum — the same one documents.created_by_type uses — so a
// document and its attachments record their author in one vocabulary.
type DocumentAttachment struct {
	ID         uuid.UUID `json:"id" db:"id"`
	DocumentID uuid.UUID `json:"document_id" db:"document_id"`
	Name       string    `json:"name" db:"name"`
	MimeType   string    `json:"mime_type" db:"mime_type"`
	SizeBytes  int64     `json:"size_bytes" db:"size_bytes"`
	StorageKey string    `json:"storage_key" db:"storage_key"`

	UploadedBy     uuid.UUID  `json:"uploaded_by" db:"uploaded_by"`
	UploadedByType ActorType  `json:"uploaded_by_type" db:"uploaded_by_type"`
	CreatedAt      time.Time  `json:"created_at" db:"created_at"`
	DeletedAt      *time.Time `json:"deleted_at,omitempty" db:"deleted_at"`

	// SourcePath/SourceSHA256 name where this attachment was copied FROM, for
	// attachments the Team Relay mount sync imports from image embeds. The dedup
	// key is (DocumentID, SourcePath) — unique among live rows — with
	// SourceSHA256 as the "did the source file change" signal, the same pair of
	// roles documents' (SourceShare, SourcePath)/SourceSHA256 play. Nil on every
	// human upload: only the mount sync's embed importer writes these.
	SourcePath   *string `json:"source_path,omitempty" db:"source_path"`
	SourceSHA256 *string `json:"source_sha256,omitempty" db:"source_sha256"`
}
