package domain

import (
	"encoding/json"
	"fmt"
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

	// DownloadPath is not stored — it is computed in MarshalJSON below, from
	// ID alone, on every response that serialises an Artifact. It is the
	// stable, always-valid counterpart to metadata.tr_public_url: on a
	// PRIVATE Team Relay share that URL 401s for anything without a TR
	// login, and it was the ONLY link-shaped field on an artifact, so an
	// automated consumer (verifier subagent, acceptance script) had nowhere
	// else to go. This field names the real path instead —
	// GET <DownloadPath> with a valid X-Agent-Key/session returns
	// {"url": "<presigned S3 URL>"}. Deliberately a PATH, not the presigned
	// URL itself: the task response this rides along on gets cached
	// (ContextCacheService, 60s) and pasted into comments/logs, and a
	// presigned URL is short-lived — shipping it here would just relocate
	// the dead-link problem to a later timestamp.
	DownloadPath string `json:"download_path"`
}

// sensitiveArtifactMetadataKeys are Metadata fields the service layer needs
// internally but that must never reach an API response. tr_agent_key is a
// long-lived TeamRelay share credential.
var sensitiveArtifactMetadataKeys = []string{"tr_agent_key"}

// MarshalJSON redacts sensitiveArtifactMetadataKeys from Metadata before
// serialising. This runs for every caller that turns an Artifact into JSON —
// handler, package, or future read path alike — because it is on the type,
// not on each call site. The previous approach (a helper each handler had to
// remember to call) missed the GET /tasks/:id/context path for months
// (#58a0e7aa) before anyone noticed; a per-callsite guard can always be
// forgotten again by the next one. This can't be.
func (a Artifact) MarshalJSON() ([]byte, error) {
	type alias Artifact // same fields/tags, no MarshalJSON — avoids recursion
	out := alias(a)
	out.Metadata = redactArtifactMetadata(a.Metadata)
	// Canonical machine path to the bytes — see the DownloadPath field doc.
	// Matches the route registered in cmd/api/main.go:
	// api.GET("/artifacts/:artifact_id/download", ...).
	out.DownloadPath = fmt.Sprintf("/api/v1/artifacts/%s/download", a.ID)
	return json.Marshal(out)
}

func redactArtifactMetadata(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		// A non-nil zero-length RawMessage ("") is not valid JSON — passing it
		// through unchanged makes encoding/json fail the whole marshal with
		// "unexpected end of JSON input" (caught by
		// TestArtifact_MarshalJSON_EmptyMetadata). nil marshals to `null`,
		// which is what a genuinely absent Metadata should look like anyway.
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		// Not a JSON object we can inspect — pass through rather than drop
		// legitimate metadata on an unexpected shape.
		return raw
	}
	changed := false
	for _, k := range sensitiveArtifactMetadataKeys {
		if _, ok := m[k]; ok {
			delete(m, k)
			changed = true
		}
	}
	if !changed {
		return raw
	}
	if b, err := json.Marshal(m); err == nil {
		return b
	}
	return raw
}
