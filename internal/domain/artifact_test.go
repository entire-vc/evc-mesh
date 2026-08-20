package domain

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These are the structural counterpart to
// internal/handler.TestArtifactReadPathsAllRedact: that test catches a
// missing redaction call by scanning source text for a call site, which only
// sees internal/handler and only at file granularity. These tests instead
// prove the leak is impossible by construction — they marshal an Artifact the
// naive way (bare json.Marshal, no handler, no redaction helper, no
// awareness this type carries a secret) from contexts a source scan can't
// reach: a value, a pointer, a slice, and nested inside an arbitrary response
// shape. If any of these ever needed a call to a redaction helper to pass,
// MarshalJSON below would not be doing its job.

func artifactWithSecret(id uuid.UUID) Artifact {
	return Artifact{
		ID:   id,
		Name: "report.md",
		Metadata: json.RawMessage(
			`{"tr_public_url":"https://relay.example.com/f","tr_agent_key":"tr_agent_secret"}`,
		),
	}
}

func TestArtifact_MarshalJSON_RedactsSensitiveMetadata(t *testing.T) {
	art := artifactWithSecret(uuid.New())

	// The naive path: no stripSensitiveMetadata call, no awareness of the
	// secret. This is exactly what a brand-new handler in a brand-new
	// package would do if it just returned the artifact it got from the
	// service layer.
	b, err := json.Marshal(art)
	require.NoError(t, err)

	assert.NotContains(t, string(b), "tr_agent_key")
	assert.NotContains(t, string(b), "tr_agent_secret")
	assert.Contains(t, string(b), "tr_public_url",
		"must still serialise non-sensitive metadata — otherwise the assertions "+
			"above would also pass on a response with no metadata at all")
	assert.Contains(t, string(b), "https://relay.example.com/f")
}

func TestArtifact_MarshalJSON_RedactsThroughPointer(t *testing.T) {
	art := artifactWithSecret(uuid.New())

	b, err := json.Marshal(&art)
	require.NoError(t, err)

	assert.NotContains(t, string(b), "tr_agent_secret")
}

func TestArtifact_MarshalJSON_RedactsInSlice(t *testing.T) {
	// The exact shape ListByTask returns to a caller (e.g. inside
	// pagination.Page[Artifact].Items) — a plain []Artifact, marshaled
	// directly with no per-item loop calling a redaction helper.
	items := []Artifact{artifactWithSecret(uuid.New()), artifactWithSecret(uuid.New())}

	b, err := json.Marshal(items)
	require.NoError(t, err)

	assert.NotContains(t, string(b), "tr_agent_secret")
}

// Second negative control: a non-redacting read path nested inside a larger,
// unrelated response — the shape GET /tasks/:id/context uses (an artifact
// list embedded under a "artifacts" key alongside task/comments/deps). A
// second such path could be added anywhere, including a file that already
// has an unrelated redacted call site elsewhere, and it still can't leak.
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

func TestArtifact_MarshalJSON_PreservesNonSensitiveMetadata(t *testing.T) {
	art := Artifact{
		ID:       uuid.New(),
		Metadata: json.RawMessage(`{"tr_public_url":"https://example.com/file.txt","custom":"value"}`),
	}

	b, err := json.Marshal(art)
	require.NoError(t, err)

	var decoded struct {
		Metadata map[string]any `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal(b, &decoded))
	assert.Equal(t, "https://example.com/file.txt", decoded.Metadata["tr_public_url"])
	assert.Equal(t, "value", decoded.Metadata["custom"])
}

func TestArtifact_MarshalJSON_NilMetadata(t *testing.T) {
	art := Artifact{ID: uuid.New(), Metadata: nil}

	b, err := json.Marshal(art)
	require.NoError(t, err)
	assert.NotPanics(t, func() { _ = art.RedactedMetadata() })
	assert.Contains(t, string(b), `"metadata":null`)
}

func TestArtifact_MarshalJSON_MalformedMetadataPassesThroughUnchanged(t *testing.T) {
	// Not a JSON object — RedactedMetadata must not panic or corrupt it; it
	// simply can't redact something it can't parse as key/value pairs.
	art := Artifact{ID: uuid.New(), Metadata: json.RawMessage(`"not an object"`)}

	b, err := json.Marshal(art)
	require.NoError(t, err)

	var decoded struct {
		Metadata string `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal(b, &decoded))
	assert.Equal(t, "not an object", decoded.Metadata)
}

func TestArtifact_RedactedMetadata_DoesNotMutateReceiver(t *testing.T) {
	art := artifactWithSecret(uuid.New())
	original := string(art.Metadata)

	_ = art.RedactedMetadata()

	assert.Equal(t, original, string(art.Metadata),
		"RedactedMetadata must return a redacted copy, not mutate the artifact in place — "+
			"MarshalJSON relies on the receiver being a value copy already, but this guards "+
			"the method's own contract independent of that")
}
