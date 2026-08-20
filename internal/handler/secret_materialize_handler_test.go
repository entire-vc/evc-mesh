package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

type mockMaterializationService struct {
	resolve func(ctx context.Context, workspaceID uuid.UUID, projectID, agentID *uuid.UUID) ([]domain.MaterializedSecret, error)
}

func (m *mockMaterializationService) ResolveForSpawn(ctx context.Context, workspaceID uuid.UUID, projectID, agentID *uuid.UUID) ([]domain.MaterializedSecret, error) {
	return m.resolve(ctx, workspaceID, projectID, agentID)
}

func postMaterialize(t *testing.T, h *SecretMaterializeHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/internal/secrets/materialize", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Materialize(c))
	return rec
}

func TestSecretMaterializeHandler_ReturnsValuesAndExpiredFlag(t *testing.T) {
	wsID := uuid.New()
	var gotWS uuid.UUID
	svc := &mockMaterializationService{
		resolve: func(_ context.Context, workspaceID uuid.UUID, _, _ *uuid.UUID) ([]domain.MaterializedSecret, error) {
			gotWS = workspaceID
			return []domain.MaterializedSecret{
				{Name: "GITHUB_TOKEN", Value: "ghp_live", Expired: false},
				{Name: "OLD_KEY", Value: "", Expired: true},
			}, nil
		},
	}
	h := NewSecretMaterializeHandler(svc)

	rec := postMaterialize(t, h, `{"workspace_id":"`+wsID.String()+`"}`)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, wsID, gotWS)

	var out []materializedSecretResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out, 2)
	assert.Equal(t, "GITHUB_TOKEN", out[0].Name)
	assert.Equal(t, "ghp_live", out[0].Value)
	assert.False(t, out[0].Expired)
	assert.Equal(t, "OLD_KEY", out[1].Name)
	assert.True(t, out[1].Expired)
}

func TestSecretMaterializeHandler_MissingWorkspaceIDIsBadRequest(t *testing.T) {
	svc := &mockMaterializationService{
		resolve: func(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID) ([]domain.MaterializedSecret, error) {
			t.Fatal("service must not be called without a workspace_id")
			return nil, nil
		},
	}
	h := NewSecretMaterializeHandler(svc)

	rec := postMaterialize(t, h, `{}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSecretMaterializeHandler_InvalidBodyIsBadRequest(t *testing.T) {
	svc := &mockMaterializationService{
		resolve: func(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID) ([]domain.MaterializedSecret, error) {
			t.Fatal("service must not be called with an unparsable body")
			return nil, nil
		},
	}
	h := NewSecretMaterializeHandler(svc)

	rec := postMaterialize(t, h, `not json`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSecretMaterializeHandler_ServiceErrorSurfaces(t *testing.T) {
	svc := &mockMaterializationService{
		resolve: func(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID) ([]domain.MaterializedSecret, error) {
			return nil, errors.New("db unavailable")
		},
	}
	h := NewSecretMaterializeHandler(svc)

	rec := postMaterialize(t, h, `{"workspace_id":"`+uuid.New().String()+`"}`)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestSecretMaterializeHandler_PassesProjectAndAgentScope(t *testing.T) {
	wsID, projID, agentID := uuid.New(), uuid.New(), uuid.New()
	var gotProj, gotAgent *uuid.UUID
	svc := &mockMaterializationService{
		resolve: func(_ context.Context, _ uuid.UUID, projectID, agentIDArg *uuid.UUID) ([]domain.MaterializedSecret, error) {
			gotProj, gotAgent = projectID, agentIDArg
			return nil, nil
		},
	}
	h := NewSecretMaterializeHandler(svc)

	body := `{"workspace_id":"` + wsID.String() + `","project_id":"` + projID.String() + `","agent_id":"` + agentID.String() + `"}`
	rec := postMaterialize(t, h, body)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, gotProj)
	require.NotNil(t, gotAgent)
	assert.Equal(t, projID, *gotProj)
	assert.Equal(t, agentID, *gotAgent)
}

func TestSecretMaterializeHandler_EmptyResultIsEmptyArrayNotNull(t *testing.T) {
	svc := &mockMaterializationService{
		resolve: func(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID) ([]domain.MaterializedSecret, error) {
			return []domain.MaterializedSecret{}, nil
		},
	}
	h := NewSecretMaterializeHandler(svc)

	rec := postMaterialize(t, h, `{"workspace_id":"`+uuid.New().String()+`"}`)

	var out []materializedSecretResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Empty(t, out)
}
