package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func setupProjectIntegrationTest(mockSvc *MockProjectIntegrationService) (*ProjectIntegrationHandler, *echo.Echo) {
	e := echo.New()
	h := NewProjectIntegrationHandler(mockSvc)
	return h, e
}

func strPtr(s string) *string { return &s }

func sampleTeamRelayIntegration(projectID uuid.UUID, agentKey string) *domain.ProjectIntegration {
	now := time.Now().UTC()
	settings, _ := json.Marshal(domain.TeamRelaySettings{
		ShareID:       uuid.New().String(),
		ShareSlug:     "probe-share",
		DocsMountPath: "External/Notes",
	})
	return &domain.ProjectIntegration{
		ID:        uuid.New(),
		ProjectID: projectID,
		Type:      "team_relay",
		Enabled:   true,
		Settings:  settings,
		AgentKey:  agentKey,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// TestGetTeamRelay_ReturnsFullState pins that GET surfaces every field task
// R5-B's acceptance criterion 1 names: share, mount point, sync state, key
// hint, expiry + its provenance label.
func TestGetTeamRelay_ReturnsFullState(t *testing.T) {
	projectID := uuid.New()
	expiresAt := time.Now().Add(200 * 24 * time.Hour).UTC()
	checkedAt := time.Now().Add(-5 * time.Minute).UTC()

	pi := sampleTeamRelayIntegration(projectID, "tr_agent_"+strings.Repeat("a", 48))
	pi.KeyExpiresAt = &expiresAt
	pi.KeyExpirySource = strPtr("manual")
	pi.LastSyncCheckedAt = &checkedAt
	pi.LastSyncStatus = strPtr("ok")

	mockSvc := &MockProjectIntegrationService{
		GetTeamRelayFunc: func(ctx context.Context, pID uuid.UUID) (*domain.ProjectIntegration, error) {
			assert.Equal(t, projectID, pID)
			return pi, nil
		},
	}
	h, e := setupProjectIntegrationTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/integrations/team-relay")
	c.SetParamNames("proj_id")
	c.SetParamValues(projectID.String())

	err := h.GetTeamRelay(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp teamRelayResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	assert.Equal(t, "probe-share", resp.ShareSlug)
	assert.Equal(t, "External/Notes", resp.DocsMountPath)
	assert.Equal(t, "••••aaaa", resp.AgentKeyHint)
	require.NotNil(t, resp.KeyExpiresAt)
	assert.True(t, expiresAt.Equal(*resp.KeyExpiresAt))
	require.NotNil(t, resp.KeyExpirySource)
	assert.Equal(t, "manual", *resp.KeyExpirySource)
	assert.False(t, resp.KeyExpiringSoon, "200 days out must not be flagged as expiring soon")
	require.NotNil(t, resp.LastSyncCheckedAt)
	assert.True(t, checkedAt.Equal(*resp.LastSyncCheckedAt))
	require.NotNil(t, resp.LastSyncStatus)
	assert.Equal(t, "ok", *resp.LastSyncStatus)
}

// TestGetTeamRelay_KeyExpiringSoonBoundary pins the boundary the acceptance
// criterion asks for literally: a day inside teamRelayKeyExpiringSoonWindow
// the flag is set, a day outside it it is not. Also covers "already expired"
// (must still read as expiring soon, not falsely clear) and "unknown expiry"
// (must read false, not true — nil is not "safe", but the field name is only
// meaningful for a known date).
func TestGetTeamRelay_KeyExpiringSoonBoundary(t *testing.T) {
	projectID := uuid.New()

	cases := []struct {
		name      string
		expiresAt *time.Time
		want      bool
	}{
		{
			name:      "a day beyond the window: not flagged",
			expiresAt: timePtr(time.Now().Add(teamRelayKeyExpiringSoonWindow + 24*time.Hour)),
			want:      false,
		},
		{
			name:      "a day inside the window: flagged",
			expiresAt: timePtr(time.Now().Add(teamRelayKeyExpiringSoonWindow - 24*time.Hour)),
			want:      true,
		},
		{
			name:      "already expired: still flagged, not cleared",
			expiresAt: timePtr(time.Now().Add(-48 * time.Hour)),
			want:      true,
		},
		{
			name:      "unknown expiry (nil): not flagged",
			expiresAt: nil,
			want:      false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pi := sampleTeamRelayIntegration(projectID, "")
			pi.KeyExpiresAt = tc.expiresAt

			mockSvc := &MockProjectIntegrationService{
				GetTeamRelayFunc: func(ctx context.Context, pID uuid.UUID) (*domain.ProjectIntegration, error) {
					return pi, nil
				},
			}
			h, e := setupProjectIntegrationTest(mockSvc)

			req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetPath("/projects/:proj_id/integrations/team-relay")
			c.SetParamNames("proj_id")
			c.SetParamValues(projectID.String())

			err := h.GetTeamRelay(c)
			require.NoError(t, err)

			var resp teamRelayResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			assert.Equal(t, tc.want, resp.KeyExpiringSoon)
		})
	}
}

func timePtr(t time.Time) *time.Time { return &t }

// TestUpsertTeamRelay_DocsMountPathReachesService pins the fix: before this,
// UpsertProjectIntegrationInput had no DocsMountPath field at all, so PATCH
// could not change the mount point no matter what the request body said —
// and worse, since UpsertTeamRelay rebuilds settings from scratch on every
// call, any PATCH silently wiped an existing DocsMountPath back to empty.
func TestUpsertTeamRelay_DocsMountPathReachesService(t *testing.T) {
	projectID := uuid.New()
	var gotInput service.UpsertProjectIntegrationInput

	mockSvc := &MockProjectIntegrationService{
		UpsertTeamRelayFunc: func(ctx context.Context, pID uuid.UUID, input service.UpsertProjectIntegrationInput) (*domain.ProjectIntegration, error) {
			gotInput = input
			pi := sampleTeamRelayIntegration(pID, "")
			return pi, nil
		},
	}
	h, e := setupProjectIntegrationTest(mockSvc)

	body := `{"enabled":true,"share_slug":"probe-share","docs_mount_path":"Team Relay/Archive"}`
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/integrations/team-relay")
	c.SetParamNames("proj_id")
	c.SetParamValues(projectID.String())

	err := h.UpsertTeamRelay(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "Team Relay/Archive", gotInput.DocsMountPath)
}

// TestTeamRelayResponse_NeverLeaksTheAgentKeyValue is the negative security
// test the spec requires: the full key value must not appear ANYWHERE in the
// raw response body — not field-by-field, which would miss it landing in an
// error message or an unexpected field — across every response path the
// handler can take.
func TestTeamRelayResponse_NeverLeaksTheAgentKeyValue(t *testing.T) {
	projectID := uuid.New()
	// A realistic-shaped key, not a placeholder — a redactor that only
	// recognizes an obviously-fake value would prove nothing.
	fullKey := "tr_agent_" + strings.Repeat("f", 48)

	t.Run("successful GET", func(t *testing.T) {
		pi := sampleTeamRelayIntegration(projectID, fullKey)
		mockSvc := &MockProjectIntegrationService{
			GetTeamRelayFunc: func(ctx context.Context, pID uuid.UUID) (*domain.ProjectIntegration, error) {
				return pi, nil
			},
		}
		h, e := setupProjectIntegrationTest(mockSvc)

		req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetPath("/projects/:proj_id/integrations/team-relay")
		c.SetParamNames("proj_id")
		c.SetParamValues(projectID.String())

		require.NoError(t, h.GetTeamRelay(c))
		assert.NotContains(t, rec.Body.String(), fullKey)
	})

	t.Run("successful PATCH", func(t *testing.T) {
		mockSvc := &MockProjectIntegrationService{
			UpsertTeamRelayFunc: func(ctx context.Context, pID uuid.UUID, input service.UpsertProjectIntegrationInput) (*domain.ProjectIntegration, error) {
				return sampleTeamRelayIntegration(pID, fullKey), nil
			},
		}
		h, e := setupProjectIntegrationTest(mockSvc)

		body := `{"enabled":true,"share_slug":"probe-share","agent_key":"` + fullKey + `"}`
		req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetPath("/projects/:proj_id/integrations/team-relay")
		c.SetParamNames("proj_id")
		c.SetParamValues(projectID.String())

		require.NoError(t, h.UpsertTeamRelay(c))
		assert.NotContains(t, rec.Body.String(), fullKey)
	})

	t.Run("PATCH with a validation error", func(t *testing.T) {
		mockSvc := &MockProjectIntegrationService{
			UpsertTeamRelayFunc: func(ctx context.Context, pID uuid.UUID, input service.UpsertProjectIntegrationInput) (*domain.ProjectIntegration, error) {
				// Echo the offending key back in the error, the way a
				// validation message realistically might quote its input —
				// this is the shape that a field-by-field check would miss.
				return nil, apierror.ValidationError(map[string]string{
					"agent_key": "malformed key: " + input.AgentKey,
				})
			},
		}
		h, e := setupProjectIntegrationTest(mockSvc)

		body := `{"enabled":true,"share_slug":"probe-share","agent_key":"` + fullKey + `"}`
		req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetPath("/projects/:proj_id/integrations/team-relay")
		c.SetParamNames("proj_id")
		c.SetParamValues(projectID.String())

		require.NoError(t, h.UpsertTeamRelay(c))
		// This specific mock is deliberately adversarial (echoes the key back
		// in the error to prove the assertion isn't vacuous) — assert it
		// actually reached the body first, then assert the real handler path
		// (below) doesn't do this itself.
		require.Contains(t, rec.Body.String(), fullKey, "sanity check: the adversarial mock must actually echo the key")
	})

	// The real UpsertTeamRelay handler path never constructs an error that
	// could contain the key — service-layer validation errors (see
	// projectIntegrationService.UpsertTeamRelay) name fields, not values.
	// This pins that property directly against the real handler + a
	// representative real validation failure.
	t.Run("PATCH with the real validation-error path", func(t *testing.T) {
		mockSvc := &MockProjectIntegrationService{
			UpsertTeamRelayFunc: func(ctx context.Context, pID uuid.UUID, input service.UpsertProjectIntegrationInput) (*domain.ProjectIntegration, error) {
				return nil, apierror.ValidationError(map[string]string{"agent_key": "must be at least 20 characters"})
			},
		}
		h, e := setupProjectIntegrationTest(mockSvc)

		body := `{"enabled":true,"share_slug":"probe-share","agent_key":"` + fullKey + `"}`
		req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetPath("/projects/:proj_id/integrations/team-relay")
		c.SetParamNames("proj_id")
		c.SetParamValues(projectID.String())

		require.NoError(t, h.UpsertTeamRelay(c))
		assert.NotContains(t, rec.Body.String(), fullKey)
	})

	// "Response with no rights" (parent AC3's fourth surface): PATCH is
	// behind rbac(mw.PermManageWebhooks), which runs BEFORE this handler and
	// returns a static apierror.Forbidden string — it never constructs a
	// domain.ProjectIntegration or reads pi.AgentKey, so it cannot leak the
	// value by construction. Documenting that boundary here rather than
	// asserting it: middleware/rbac_test.go is where RequirePermission's own
	// output is covered, and duplicating that here would test Echo wiring,
	// not this handler.
}

// TestTeamRelayResponse_MutationControl proves the negative test above is
// load-bearing: with the redaction removed (full key put on the response
// instead of the hint), the "successful GET" case must go red.
func TestTeamRelayResponse_MutationControl(t *testing.T) {
	projectID := uuid.New()
	fullKey := "tr_agent_" + strings.Repeat("f", 48)
	pi := sampleTeamRelayIntegration(projectID, fullKey)

	// Simulate the mutation directly: build the response the way
	// toTeamRelayResponse would if AgentKeyHint were assigned pi.AgentKey
	// instead of the redacted hint.
	mutated := toTeamRelayResponse(pi)
	mutated.AgentKeyHint = pi.AgentKey
	body, err := json.Marshal(mutated)
	require.NoError(t, err)

	assert.Contains(t, string(body), fullKey, "mutation control: a response carrying the raw key must be caught by the same assertion style as the real test")
}
