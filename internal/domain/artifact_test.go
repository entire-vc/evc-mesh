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
