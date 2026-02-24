package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ArtifactType classifies the kind of artifact attached to a task.
type ArtifactType string

const (
	ArtifactTypeFile   ArtifactType = "file"
	ArtifactTypeCode   ArtifactType = "code"
	ArtifactTypeLog    ArtifactType = "log"
	ArtifactTypeReport ArtifactType = "report"
	ArtifactTypeLink   ArtifactType = "link"
	ArtifactTypeImage  ArtifactType = "image"
	ArtifactTypeData   ArtifactType = "data"
)

// UploaderType determines whether an artifact was uploaded by a user or agent.
type UploaderType string

const (
	UploaderTypeUser  UploaderType = "user"
	UploaderTypeAgent UploaderType = "agent"
)

// Artifact is a file, code snippet, log, report, or link attached to a task.
// Stored in S3/MinIO with metadata for display and retrieval.
type Artifact struct {
	ID             uuid.UUID       `json:"id" db:"id"`
	TaskID         uuid.UUID       `json:"task_id" db:"task_id"`
	Name           string          `json:"name" db:"name"`
	ArtifactType   ArtifactType    `json:"artifact_type" db:"artifact_type"`
	MimeType       string          `json:"mime_type" db:"mime_type"`
	StorageKey     string          `json:"storage_key" db:"storage_key"`
	StorageURL     string          `json:"storage_url" db:"storage_url"`
	SizeBytes      int64           `json:"size_bytes" db:"size_bytes"`
	ChecksumSHA256 string          `json:"checksum_sha256" db:"checksum_sha256"`
	Metadata       json.RawMessage `json:"metadata" db:"metadata"`
	UploadedBy     uuid.UUID       `json:"uploaded_by" db:"uploaded_by"`
	UploadedByType UploaderType    `json:"uploaded_by_type" db:"uploaded_by_type"`
	CreatedAt      time.Time       `json:"created_at" db:"created_at"`
}
