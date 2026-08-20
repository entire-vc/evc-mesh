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

// sensitiveArtifactMetadataKeys lists Metadata keys that must never reach an
// API response. These are internal service credentials the platform needs in
// order to act on an artifact's behalf (e.g. push it to TeamRelay) but that
// grant access to whoever holds them, so a caller with read access to the
// artifact must never see them. This is the single place a new one is added —
// MarshalJSON below reads from it, so every response shape picks it up
// automatically.
var sensitiveArtifactMetadataKeys = []string{
	"tr_agent_key",
}

// RedactedMetadata returns a.Metadata with every key in
// sensitiveArtifactMetadataKeys removed. MarshalJSON calls this automatically,
// so callers normally never need to call it directly; it is exported for the
// rare case that needs a redacted copy without a full JSON round-trip.
//
// Malformed/non-object Metadata is returned unchanged rather than dropped or
// panicked on — metadata we cannot parse is not a credential-leak vector we
// know how to redact, and silently discarding legitimate data on a parse
// hiccup would be its own bug.
func (a Artifact) RedactedMetadata() json.RawMessage {
	if len(a.Metadata) == 0 {
		return a.Metadata
	}
	var m map[string]any
	if err := json.Unmarshal(a.Metadata, &m); err != nil {
		return a.Metadata
	}
	changed := false
	for _, k := range sensitiveArtifactMetadataKeys {
		if _, ok := m[k]; ok {
			delete(m, k)
			changed = true
		}
	}
	if !changed {
		return a.Metadata
	}
	b, err := json.Marshal(m)
	if err != nil {
		return a.Metadata
	}
	return b
}

// MarshalJSON redacts sensitive metadata keys before serialising an Artifact.
//
// This is the structural fix for a defect class, not a single leak: a handler
// that reads an artifact from the service layer and forgets to call a
// redaction helper before writing the HTTP response used to serve
// tr_agent_key in the clear (see GET /tasks/:id/context, which did exactly
// this for months). Putting the redaction inside Artifact's own JSON encoding
// means it runs for every response shape — a lone artifact, a slice of them,
// one nested inside a larger struct — regardless of which package or handler
// produced it, and regardless of whether that code even knows this type
// carries a secret. Forgetting to redact stops being possible to forget.
//
// Value receiver so it satisfies json.Marshaler for both Artifact and
// *Artifact (a pointer's method set includes its value-receiver methods).
func (a Artifact) MarshalJSON() ([]byte, error) {
	a.Metadata = a.RedactedMetadata()
	type artifactAlias Artifact
	return json.Marshal(artifactAlias(a))
}
