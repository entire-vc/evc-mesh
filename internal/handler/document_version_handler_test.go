package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// base_version and append_body are passed straight through: the compare belongs
// to the service, so that every future caller — MCP included — inherits it
// rather than re-implementing it per transport.
func TestDocumentHandler_Update_PassesBaseVersionAndAppendThrough(t *testing.T) {
	wsID := uuid.New()
	var got service.UpdateDocumentInput

	mockSvc := &MockDocumentService{
		UpdateFunc: func(_ context.Context, id, _ uuid.UUID, in service.UpdateDocumentInput) (*domain.Document, error) {
			got = in
			return &domain.Document{ID: id, Version: 5}, nil
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docRequest(e, http.MethodPatch, uuid.New().String(), &wsID,
		`{"append_body":"\n- run 3: ok\n","base_version":4}`)
	require.NoError(t, h.Update(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got.BaseVersion)
	assert.Equal(t, 4, *got.BaseVersion)
	require.NotNil(t, got.AppendBody)
	assert.Equal(t, "\n- run 3: ok\n", *got.AppendBody)
	assert.Nil(t, got.Body, "an append is not a replacement")
}

// The distinction the whole compatibility decision rests on: an absent
// base_version reaches the service as nil, which is what makes it an
// unconditional write rather than a write against version zero.
func TestDocumentHandler_Update_AbsentBaseVersionIsNilNotZero(t *testing.T) {
	wsID := uuid.New()
	var got service.UpdateDocumentInput

	mockSvc := &MockDocumentService{
		UpdateFunc: func(_ context.Context, id, _ uuid.UUID, in service.UpdateDocumentInput) (*domain.Document, error) {
			got = in
			return &domain.Document{ID: id}, nil
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docRequest(e, http.MethodPatch, uuid.New().String(), &wsID, `{"body":"new body"}`)
	require.NoError(t, h.Update(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Nil(t, got.BaseVersion, "absent must not arrive as 0, which no live document is ever at")
}

// The 409 has to carry the version the document is actually at, or a retry is a
// guess.
func TestDocumentHandler_Update_VersionConflictIs409WithTheCurrentVersion(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockDocumentService{
		UpdateFunc: func(context.Context, uuid.UUID, uuid.UUID, service.UpdateDocumentInput) (*domain.Document, error) {
			return nil, &service.DocumentVersionConflictError{CurrentVersion: 7}
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docRequest(e, http.MethodPatch, uuid.New().String(), &wsID,
		`{"body":"stale write","base_version":3}`)
	require.NoError(t, h.Update(c))

	assert.Equal(t, http.StatusConflict, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "document_version_conflict", body["code"])
	assert.Equal(t, float64(7), body["current_version"])
	assert.Contains(t, body["message"], "append_body",
		"the answer points at the operation that needs no version")
}

// version is on every document the API returns, which is what makes the frontend
// follow-up — send it back as base_version — a small change rather than a new
// read on every save.
func TestDocumentHandler_GetByID_ReturnsVersion(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockDocumentService{
		GetByIDInWorkspaceFunc: func(_ context.Context, id, _ uuid.UUID) (*domain.Document, error) {
			return &domain.Document{ID: id, Title: "Runbook", Version: 12, Body: "# Runbook\n"}, nil
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docRequest(e, http.MethodGet, uuid.New().String(), &wsID, "")
	require.NoError(t, h.GetByID(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"version":12`)
}
