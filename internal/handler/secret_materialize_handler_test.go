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
	h := NewSecretMaterializeHandler(svc, &recordingActivityLog{})

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
	h := NewSecretMaterializeHandler(svc, &recordingActivityLog{})

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
	h := NewSecretMaterializeHandler(svc, &recordingActivityLog{})

	rec := postMaterialize(t, h, `not json`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSecretMaterializeHandler_ServiceErrorSurfaces(t *testing.T) {
	svc := &mockMaterializationService{
		resolve: func(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID) ([]domain.MaterializedSecret, error) {
			return nil, errors.New("db unavailable")
		},
	}
	h := NewSecretMaterializeHandler(svc, &recordingActivityLog{})

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
	h := NewSecretMaterializeHandler(svc, &recordingActivityLog{})

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
	h := NewSecretMaterializeHandler(svc, &recordingActivityLog{})

	rec := postMaterialize(t, h, `{"workspace_id":"`+uuid.New().String()+`"}`)

	var out []materializedSecretResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Empty(t, out)
}

// --- Issuance journal (#1c9f527d) -----------------------------------------

// Values chosen so that no legitimate audit field can contain them by
// accident: a hit on either one in the raw journal JSON means a value leaked.
const (
	auditLiveValue    = "ghp_AUDITLEAKCANARY_live_7f3e9c"
	auditExpiredValue = "sk_AUDITLEAKCANARY_expired_2b1d"
)

func postMaterializeWithRequestID(t *testing.T, h *SecretMaterializeHandler, body, requestID string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/internal/secrets/materialize", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	// What echo's RequestID middleware does before any handler runs.
	c.Response().Header().Set(echo.HeaderXRequestID, requestID)
	require.NoError(t, h.Materialize(c))
	return rec
}

func TestMaterializeAudit_NeverCarriesAValue(t *testing.T) {
	wsID, projID, agentID := uuid.New(), uuid.New(), uuid.New()
	liveID, expiredID := uuid.New(), uuid.New()
	svc := &mockMaterializationService{
		resolve: func(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID) ([]domain.MaterializedSecret, error) {
			return []domain.MaterializedSecret{
				{ID: liveID, Name: "GITHUB_TOKEN", Value: auditLiveValue, ValueSHA256Prefix: "1a2b3c4d"},
				// Expired rows carry no Value from the repo today; give this one a
				// value anyway so the test does not lean on that repo behaviour.
				{ID: expiredID, Name: "OLD_KEY", Value: auditExpiredValue, ValueSHA256Prefix: "5e6f7a8b", Expired: true},
			}, nil
		},
	}
	act := &recordingActivityLog{}
	h := NewSecretMaterializeHandler(svc, act)

	body := `{"workspace_id":"` + wsID.String() + `","project_id":"` + projID.String() + `","agent_id":"` + agentID.String() + `"}`
	rec := postMaterializeWithRequestID(t, h, body, "req-abc-123")

	require.Equal(t, http.StatusOK, rec.Code)
	// Positive control: the values really were in the handler's hands and on
	// the wire — otherwise "not in the journal" would prove nothing.
	require.Contains(t, rec.Body.String(), auditLiveValue)

	require.Len(t, act.entries, 1, "exactly one journal entry per issuance")
	entry := act.entries[0]
	raw := string(entry.Changes)
	assert.NotContains(t, raw, auditLiveValue, "journal entry leaked a live secret value")
	assert.NotContains(t, raw, auditExpiredValue, "journal entry leaked an expired secret value")
	assert.NotContains(t, raw, "AUDITLEAKCANARY")

	assert.Equal(t, wsID, entry.WorkspaceID)
	assert.Equal(t, "secret_materialization", entry.EntityType)
	assert.Equal(t, "materialized", entry.Action)
	assert.NotEqual(t, uuid.Nil, entry.EntityID)
	assert.Equal(t, agentID, entry.ActorID)
	assert.Equal(t, domain.ActorTypeAgent, entry.ActorType)

	var got materializationAudit
	require.NoError(t, json.Unmarshal(entry.Changes, &got))
	assert.Equal(t, "req-abc-123", got.RequestID)
	assert.Equal(t, wsID, got.WorkspaceID)
	require.NotNil(t, got.ProjectID)
	assert.Equal(t, projID, *got.ProjectID)
	require.NotNil(t, got.AgentID)
	assert.Equal(t, agentID, *got.AgentID)
	assert.Equal(t, 2, got.Count)
	assert.Equal(t, []materializationAuditSecret{
		{Name: "GITHUB_TOKEN", SecretID: liveID, ValueSHA256Prefix: "1a2b3c4d"},
		{Name: "OLD_KEY", SecretID: expiredID, ValueSHA256Prefix: "5e6f7a8b", Expired: true},
	}, got.Secrets)
}

// Every field of the journal line, by name. If someone adds a field to
// materializationAuditSecret, this fails and makes them look at it — the
// type having no room for a value is the guarantee, so its shape is pinned.
func TestMaterializeAudit_SecretLineShapeIsPinned(t *testing.T) {
	b, err := json.Marshal(materializationAuditSecret{})
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(b, &fields))
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	assert.ElementsMatch(t, []string{"name", "secret_id", "value_sha256_prefix", "expired"}, keys)
}

func TestMaterializeAudit_NoAgentIsAttributedToSystem(t *testing.T) {
	svc := &mockMaterializationService{
		resolve: func(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID) ([]domain.MaterializedSecret, error) {
			return nil, nil
		},
	}
	act := &recordingActivityLog{}
	h := NewSecretMaterializeHandler(svc, act)

	rec := postMaterialize(t, h, `{"workspace_id":"`+uuid.New().String()+`"}`)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, act.entries, 1, "an issuance of zero secrets is still an issuance")
	assert.Equal(t, domain.ActorTypeSystem, act.entries[0].ActorType)
	assert.Equal(t, uuid.Nil, act.entries[0].ActorID)
}

// No journal, no values: an issuance nobody can account for is the gap.
func TestMaterializeAudit_JournalFailureReturnsNoValues(t *testing.T) {
	svc := &mockMaterializationService{
		resolve: func(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID) ([]domain.MaterializedSecret, error) {
			return []domain.MaterializedSecret{{ID: uuid.New(), Name: "GITHUB_TOKEN", Value: auditLiveValue}}, nil
		},
	}
	act := &recordingActivityLog{err: errors.New("activity_log insert failed")}
	h := NewSecretMaterializeHandler(svc, act)

	rec := postMaterialize(t, h, `{"workspace_id":"`+uuid.New().String()+`"}`)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.NotContains(t, rec.Body.String(), auditLiveValue)
}

func TestMaterializeAudit_ResolveErrorWritesNoEntry(t *testing.T) {
	svc := &mockMaterializationService{
		resolve: func(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID) ([]domain.MaterializedSecret, error) {
			return nil, errors.New("db unavailable")
		},
	}
	act := &recordingActivityLog{}
	h := NewSecretMaterializeHandler(svc, act)

	rec := postMaterialize(t, h, `{"workspace_id":"`+uuid.New().String()+`"}`)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Empty(t, act.entries, "nothing was handed out, so nothing to journal")
}
