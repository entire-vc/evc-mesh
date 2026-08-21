package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// This is the negative control task #4b33a6fd asked for: "add a read path in
// another package that never calls the redaction guard, and confirm it still
// redacts." The old guard (a source-text scanner in internal/handler) could
// only ever be shown a fake read path INSIDE internal/handler — it had no way
// to prove anything about a package it doesn't scan. This test is that other
// package: internal/service has never imported the handler package's
// redaction helper and never will, so if this stays green, redaction really
// is on the type, not on remembering to call something.
//
// throwawayHandler below is deliberately NOT part of any real route — it
// exists only to be a caller with zero special knowledge of tr_agent_key.
func throwawayHandler(w http.ResponseWriter, art domain.Artifact) {
	b, _ := json.Marshal(art)
	w.Write(b) //nolint:errcheck // test helper
}

func TestArtifactRedaction_NewReadPathInAnotherPackage_StillRedacts(t *testing.T) {
	art := domain.Artifact{
		ID:       uuid.New(),
		Metadata: json.RawMessage(`{"tr_public_url":"https://relay.example.com/f","tr_agent_key":"tr_agent_secret_value"}`),
	}

	rec := httptest.NewRecorder()
	throwawayHandler(rec, art)
	body := rec.Body.String()

	assert.NotContains(t, body, "tr_agent_key")
	assert.NotContains(t, body, "tr_agent_secret_value")

	// Negative control's own negative control: prove this handler actually
	// serialises metadata at all, so the assertions above aren't vacuously
	// passing on an empty body.
	assert.Contains(t, body, "tr_public_url")
	assert.Contains(t, body, "https://relay.example.com/f")
}
