package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

func setupArtifactTest(mockSvc *MockArtifactService) (*ArtifactHandler, *echo.Echo) {
	e := echo.New()
	h := NewArtifactHandler(mockSvc)
	return h, e
}

// --- stripSensitiveMetadata unit tests ---

func TestStripSensitiveMetadata_RemovesTrAgentKey(t *testing.T) {
	art := &domain.Artifact{
		ID:       uuid.New(),
		Metadata: json.RawMessage(`{"tr_public_url":"https://example.com/file.txt","tr_agent_key":"tr_agent_abc123"}`),
	}
	stripSensitiveMetadata(art)

	var m map[string]any
	require.NoError(t, json.Unmarshal(art.Metadata, &m))
	assert.NotContains(t, m, "tr_agent_key")
	assert.Equal(t, "https://example.com/file.txt", m["tr_public_url"])
}

func TestStripSensitiveMetadata_PreservesOtherKeys(t *testing.T) {
	art := &domain.Artifact{
		ID:       uuid.New(),
		Metadata: json.RawMessage(`{"tr_public_url":"https://example.com/file.txt","custom":"value"}`),
	}
	before := string(art.Metadata)
	stripSensitiveMetadata(art)
	assert.JSONEq(t, before, string(art.Metadata))
}

func TestStripSensitiveMetadata_NilMetadata(t *testing.T) {
	art := &domain.Artifact{ID: uuid.New(), Metadata: nil}
	assert.NotPanics(t, func() { stripSensitiveMetadata(art) })
	assert.Nil(t, art.Metadata)
}

func TestStripSensitiveMetadata_EmptyMetadata(t *testing.T) {
	art := &domain.Artifact{ID: uuid.New(), Metadata: json.RawMessage{}}
	assert.NotPanics(t, func() { stripSensitiveMetadata(art) })
}

// --- GetByID handler tests ---

func TestArtifactHandler_GetByID_StripsTrAgentKey(t *testing.T) {
	artifactID := uuid.New()

	mockSvc := &MockArtifactService{
		GetByIDFunc: func(_ context.Context, id uuid.UUID) (*domain.Artifact, error) {
			return &domain.Artifact{
				ID:       id,
				Name:     "report.md",
				Metadata: json.RawMessage(`{"tr_public_url":"https://relay.example.com/f","tr_agent_key":"tr_agent_secret"}`),
			}, nil
		},
	}

	h, e := setupArtifactTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/artifacts/:artifact_id")
	c.SetParamNames("artifact_id")
	c.SetParamValues(artifactID.String())

	require.NoError(t, h.GetByID(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var result map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))

	rawMeta, ok := result["metadata"].(map[string]any)
	require.True(t, ok, "metadata should be a JSON object")
	assert.NotContains(t, rawMeta, "tr_agent_key", "tr_agent_key must not appear in API response")
	assert.Equal(t, "https://relay.example.com/f", rawMeta["tr_public_url"])
}

// --- List handler tests ---

func TestArtifactHandler_List_StripsTrAgentKey(t *testing.T) {
	taskID := uuid.New()

	mockSvc := &MockArtifactService{
		ListByTaskFunc: func(_ context.Context, tid uuid.UUID, _ pagination.Params) (*pagination.Page[domain.Artifact], error) {
			return &pagination.Page[domain.Artifact]{
				Items: []domain.Artifact{
					{
						ID:       uuid.New(),
						Name:     "file1.txt",
						Metadata: json.RawMessage(`{"tr_public_url":"https://relay.example.com/1","tr_agent_key":"tr_agent_secret1"}`),
					},
					{
						ID:       uuid.New(),
						Name:     "file2.txt",
						Metadata: json.RawMessage(`{"tr_public_url":"https://relay.example.com/2","tr_agent_key":"tr_agent_secret2"}`),
					},
				},
				TotalCount: 2,
			}, nil
		},
	}

	h, e := setupArtifactTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/artifacts")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	require.NoError(t, h.List(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var result struct {
		Items []struct {
			Metadata map[string]any `json:"metadata"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	require.Len(t, result.Items, 2)

	for i, item := range result.Items {
		assert.NotContains(t, item.Metadata, "tr_agent_key", "item %d: tr_agent_key must not appear in list response", i)
	}
}
