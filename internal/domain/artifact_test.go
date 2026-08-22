package domain

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArtifact_MarshalJSON_RedactsTrAgentKey(t *testing.T) {
	art := Artifact{
		ID:       uuid.New(),
		Metadata: json.RawMessage(`{"tr_public_url":"https://example.com/file.txt","tr_agent_key":"tr_agent_abc123"}`),
	}

	b, err := json.Marshal(art)
	require.NoError(t, err)
	body := string(b)

	assert.NotContains(t, body, "tr_agent_key")
	assert.NotContains(t, body, "tr_agent_abc123")

	// Negative control: without this, the assertions above would also pass on
	// output that dropped all metadata, proving nothing about redaction
	// specifically.
	assert.Contains(t, body, "tr_public_url")
	assert.Contains(t, body, "https://example.com/file.txt")
}

func TestArtifact_MarshalJSON_PreservesOtherMetadata(t *testing.T) {
	art := Artifact{
		ID:       uuid.New(),
		Metadata: json.RawMessage(`{"tr_public_url":"https://example.com/file.txt","custom":"value"}`),
	}

	b, err := json.Marshal(art)
	require.NoError(t, err)

	var decoded Artifact
	require.NoError(t, json.Unmarshal(b, &decoded))
	var meta map[string]any
	require.NoError(t, json.Unmarshal(decoded.Metadata, &meta))
	assert.Equal(t, "https://example.com/file.txt", meta["tr_public_url"])
	assert.Equal(t, "value", meta["custom"])
	assert.NotContains(t, meta, "tr_agent_key")
}

func TestArtifact_MarshalJSON_NilMetadata(t *testing.T) {
	art := Artifact{ID: uuid.New(), Metadata: nil}
	b, err := json.Marshal(art)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"metadata":null`)
}

func TestArtifact_MarshalJSON_EmptyMetadata(t *testing.T) {
	// A non-nil zero-length RawMessage isn't valid JSON on its own — this must
	// not error out the whole Artifact marshal, and should normalize the same
	// way nil does.
	art := Artifact{ID: uuid.New(), Metadata: json.RawMessage{}}
	b, err := json.Marshal(art)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"metadata":null`)
}

func TestArtifact_MarshalJSON_MalformedMetadataPassesThrough(t *testing.T) {
	// Not valid JSON at all (shouldn't happen in practice — Metadata is a
	// jsonb column — but MarshalJSON must not panic or corrupt the artifact
	// when it does).
	art := Artifact{ID: uuid.New(), Metadata: json.RawMessage(`not json`)}
	_, err := json.Marshal(art)
	assert.Error(t, err, "invalid RawMessage should fail marshal the same way it would have before this change, not panic")
}

// Proves the redaction applies through a pointer too — handlers marshal both
// domain.Artifact values (in a slice from List) and *domain.Artifact
// (from GetByIDInWorkspace).
func TestArtifact_MarshalJSON_ThroughPointer(t *testing.T) {
	art := &Artifact{
		ID:       uuid.New(),
		Metadata: json.RawMessage(`{"tr_agent_key":"secret"}`),
	}
	b, err := json.Marshal(art)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "secret")
}

// TestArtifact_MarshalJSON_DownloadPath proves every serialised Artifact
// carries the stable, machine-readable download path — the counterpart to
// metadata.tr_public_url that stays valid regardless of the Team Relay
// share's visibility (task #97c60be9).
func TestArtifact_MarshalJSON_DownloadPath(t *testing.T) {
	id := uuid.New()
	art := Artifact{ID: id}

	b, err := json.Marshal(art)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(b, &decoded))

	want := "/api/v1/artifacts/" + id.String() + "/download"
	assert.Equal(t, want, decoded["download_path"])
}

// TestArtifact_MarshalJSON_DownloadPath_IgnoresInputField proves the field is
// always computed from ID — a caller cannot smuggle a different value (e.g.
// an absolute URL, or a path pointing at a different artifact) through by
// pre-populating DownloadPath before marshaling. This is the guard against
// DownloadPath silently becoming an attacker-controlled redirect if the
// struct is ever built from untrusted input elsewhere.
func TestArtifact_MarshalJSON_DownloadPath_IgnoresInputField(t *testing.T) {
	id := uuid.New()
	art := Artifact{ID: id, DownloadPath: "https://evil.example.com/steal"}

	b, err := json.Marshal(art)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(b, &decoded))

	want := "/api/v1/artifacts/" + id.String() + "/download"
	assert.Equal(t, want, decoded["download_path"])
	assert.NotContains(t, decoded["download_path"], "evil.example.com")
}

// artifactWithSecret builds an Artifact carrying a tr_agent_key secret in its
// Metadata, for the redaction-coverage tests below. Ported from the closed
// PR #674 (superseded on main by this file's own MarshalJSON tests, but three
// of #674's cases were never carried over — see the tests that use this
// helper for why each one still earns its place).
func artifactWithSecret(id uuid.UUID) Artifact {
	return Artifact{
		ID:   id,
		Name: "report.md",
		Metadata: json.RawMessage(
			`{"tr_public_url":"https://relay.example.com/f","tr_agent_key":"tr_agent_secret"}`,
		),
	}
}

// TestArtifact_MarshalJSON_RedactsInSlice proves redaction survives the exact
// shape ListByTask returns to a caller (e.g. inside pagination.Page[Artifact]
// .Items) — a plain []Artifact, marshaled directly with no per-item loop
// calling a redaction helper. This follows from MarshalJSON being defined on
// the type (encoding/json calls it for every element when marshaling a
// slice), but it wasn't asserted anywhere on main until now — ported from the
// closed PR #674.
func TestArtifact_MarshalJSON_RedactsInSlice(t *testing.T) {
	items := []Artifact{artifactWithSecret(uuid.New()), artifactWithSecret(uuid.New())}

	b, err := json.Marshal(items)
	require.NoError(t, err)

	assert.NotContains(t, string(b), "tr_agent_secret")
}

// TestArtifact_MarshalJSON_RedactsWhenNestedInUnrelatedResponse proves
// redaction survives an Artifact embedded arbitrarily deep inside a larger,
// unrelated response shape — e.g. GET /tasks/:id/context, which nests an
// artifact list under an "artifacts" key alongside task/comments/deps. A
// second such nested read path could be added anywhere, including a file
// that already has an unrelated redacted call site elsewhere, and it still
// can't leak — because redaction runs from MarshalJSON on the type itself,
// not from a per-call-site helper. Ported from the closed PR #674.
func TestArtifact_MarshalJSON_RedactsWhenNestedInUnrelatedResponse(t *testing.T) {
	art := artifactWithSecret(uuid.New())

	resp := map[string]any{
		"task":      struct{ Title string }{"unrelated task"},
		"artifacts": []Artifact{art},
	}

	b, err := json.Marshal(resp)
	require.NoError(t, err)

	assert.NotContains(t, string(b), "tr_agent_secret")
}

// TestArtifact_RedactedMetadata_DoesNotMutateReceiver proves
// redactArtifactMetadata returns a redacted copy rather than mutating the
// caller's Metadata bytes in place. This is a genuinely independent property
// from the other tests in this file: they all prove metadata gets redacted
// on the way OUT (into a JSON response); this one guards the opposite
// direction — that computing a redacted response never corrupts the artifact
// still sitting in memory (and, transitively, whatever might write that
// same in-memory object back to storage or a cache afterwards). A mutating
// implementation would permanently lose tr_agent_key from the object even
// though the caller only asked for a display copy — data loss in the
// opposite direction from the leak this whole redaction effort defends
// against. Ported from the closed PR #674 (there it exercised a public
// RedactedMetadata() method; main's redaction lives in the unexported
// redactArtifactMetadata function called from MarshalJSON, so this calls
// that directly — the property under test is identical).
func TestArtifact_RedactedMetadata_DoesNotMutateReceiver(t *testing.T) {
	art := artifactWithSecret(uuid.New())
	original := string(art.Metadata)

	_ = redactArtifactMetadata(art.Metadata)

	assert.Equal(t, original, string(art.Metadata),
		"redactArtifactMetadata must return a redacted copy, not mutate the caller's "+
			"raw metadata bytes in place")
}
