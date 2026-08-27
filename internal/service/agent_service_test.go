package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// setupAgentService returns an agentService wired to fresh mocks and a pre-created workspace.
func setupAgentService() (
	*agentService,
	*MockAgentRepository,
	*domain.Workspace,
) {
	agentRepo := NewMockAgentRepository()
	activityRepo := NewMockActivityLogRepository()
	wsRepo := NewMockWorkspaceRepository()

	ws := &domain.Workspace{
		ID:   uuid.New(),
		Name: "Acme Corp",
		Slug: "acme",
	}
	wsRepo.items[ws.ID] = ws

	svc := NewAgentService(agentRepo, activityRepo, wsRepo, NewMockUserRepository()).(*agentService)

	// Freeze the clock.
	timeNow = func() time.Time { return frozenTime }

	return svc, agentRepo, ws
}

// ---------------------------------------------------------------------------
// TestAgentService_Register
// ---------------------------------------------------------------------------

func TestAgentService_Register(t *testing.T) {
	tests := []struct {
		name      string
		input     func(ws *domain.Workspace) RegisterAgentInput
		wantErr   bool
		errCode   int
		checkFunc func(t *testing.T, out *RegisterAgentOutput, agentRepo *MockAgentRepository)
	}{
		{
			name: "success - generates API key with correct format",
			input: func(ws *domain.Workspace) RegisterAgentInput {
				return RegisterAgentInput{
					WorkspaceID: ws.ID,
					Name:        "Claude Code Agent",
					AgentType:   domain.AgentTypeClaudeCode,
				}
			},
			wantErr: false,
			checkFunc: func(t *testing.T, out *RegisterAgentOutput, agentRepo *MockAgentRepository) {
				// Verify API key format: agk_{slug}_{random_hex}
				assert.True(t, strings.HasPrefix(out.APIKey, "agk_acme_"),
					"API key should start with agk_{workspace_slug}_")
				parts := strings.SplitN(out.APIKey, "_", 3)
				require.Len(t, parts, 3)
				assert.Equal(t, "agk", parts[0])
				assert.Equal(t, "acme", parts[1])
				assert.True(t, parts[2] != "", "random part should not be empty")

				// Verify the stored agent has a hash, NOT the raw key.
				agent := out.Agent
				assert.NotEmpty(t, agent.APIKeyHash)
				assert.NotEqual(t, out.APIKey, agent.APIKeyHash,
					"stored hash must differ from raw key")

				// Verify the hash actually matches the raw key.
				err := bcrypt.CompareHashAndPassword([]byte(agent.APIKeyHash), []byte(out.APIKey))
				assert.NoError(t, err, "hash should match raw key via bcrypt")

				// Verify agent fields.
				assert.NotEqual(t, uuid.Nil, agent.ID)
				assert.Equal(t, "Claude Code Agent", agent.Name)
				assert.Equal(t, "claude-code-agent", agent.Slug)
				assert.Equal(t, domain.AgentTypeClaudeCode, agent.AgentType)
				assert.Equal(t, domain.AgentStatusOffline, agent.Status)
				assert.Equal(t, frozenTime, agent.CreatedAt)

				// Verify prefix is stored for lookup.
				assert.NotEmpty(t, agent.APIKeyPrefix)
				assert.Len(t, agent.APIKeyPrefix, apiKeyPrefixLen)

				// Verify persisted in repo.
				stored, _ := agentRepo.GetByID(context.Background(), agent.ID)
				require.NotNil(t, stored)
				assert.Equal(t, agent.APIKeyHash, stored.APIKeyHash)
			},
		},
		{
			name: "success - profile fields omitted get sane defaults, not zero values",
			input: func(ws *domain.Workspace) RegisterAgentInput {
				return RegisterAgentInput{
					WorkspaceID: ws.ID,
					Name:        "Bare Agent",
					AgentType:   domain.AgentTypeClaudeCode,
				}
			},
			wantErr: false,
			checkFunc: func(t *testing.T, out *RegisterAgentOutput, _ *MockAgentRepository) {
				agent := out.Agent
				assert.Equal(t, "developer", agent.Role, "omitted role should default, not land empty")
				assert.Equal(t, "24/7", agent.WorkingHours, "omitted working_hours should default, not land empty")
				assert.Equal(t, defaultMaxConcurrentTasks, agent.MaxConcurrentTasks,
					"omitted max_concurrent_tasks must not silently stay 0 — that reads as \"takes no tasks\" (#85714565)")
				assert.Equal(t, "", agent.ResponsibilityZone, "zone has no universal default — omitted stays empty, not invented")
				assert.Nil(t, agent.EscalationTo)
				// AcceptsFrom is intentionally left nil at the service layer —
				// AgentRepo.Create (the real Postgres repo) defaults a nil
				// value to ["*"], which is what "omitted" should mean here.
				// The mock repo used by this test does not replicate that
				// guard, so this only asserts the service does not invent an
				// empty array of its own.
				assert.Nil(t, agent.AcceptsFrom)
			},
		},
		{
			name: "success - explicit profile fields are respected as-is, including a deliberate zero",
			input: func(ws *domain.Workspace) RegisterAgentInput {
				role := "reviewer"
				zone := "Mesh — QA"
				hours := "9-18 MSK"
				zero := 0
				escalation := "Garfield"
				acceptsFrom := json.RawMessage(`["Linus","Riker"]`)
				return RegisterAgentInput{
					WorkspaceID:        ws.ID,
					Name:               "Configured Agent",
					AgentType:          domain.AgentTypeClaudeCode,
					Role:               &role,
					ResponsibilityZone: &zone,
					WorkingHours:       &hours,
					MaxConcurrentTasks: &zero,
					EscalationTo:       &escalation,
					AcceptsFrom:        acceptsFrom,
					Capabilities:       map[string]any{"code-review": true},
				}
			},
			wantErr: false,
			checkFunc: func(t *testing.T, out *RegisterAgentOutput, _ *MockAgentRepository) {
				agent := out.Agent
				assert.Equal(t, "reviewer", agent.Role)
				assert.Equal(t, "Mesh — QA", agent.ResponsibilityZone)
				assert.Equal(t, "9-18 MSK", agent.WorkingHours)
				assert.Equal(t, 0, agent.MaxConcurrentTasks,
					"an explicit 0 (deliberately paused) must survive, not be upgraded to the default")
				require.NotNil(t, agent.EscalationTo)
				assert.JSONEq(t, `"Garfield"`, string(*agent.EscalationTo))
				assert.JSONEq(t, `["Linus","Riker"]`, string(agent.AcceptsFrom))
				require.NotNil(t, agent.Capabilities)
				assert.JSONEq(t, `{"code-review":true}`, string(agent.Capabilities))
			},
		},
		{
			name: "error - empty name",
			input: func(ws *domain.Workspace) RegisterAgentInput {
				return RegisterAgentInput{
					WorkspaceID: ws.ID,
					Name:        "",
					AgentType:   domain.AgentTypeCustom,
				}
			},
			wantErr: true,
			errCode: http.StatusBadRequest,
		},
		{
			name: "error - workspace not found",
			input: func(_ *domain.Workspace) RegisterAgentInput {
				return RegisterAgentInput{
					WorkspaceID: uuid.New(), // non-existent
					Name:        "Agent",
					AgentType:   domain.AgentTypeAider,
				}
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, agentRepo, ws := setupAgentService()
			ctx := context.Background()
			input := tt.input(ws)

			out, err := svc.Register(ctx, input)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
				assert.Nil(t, out)
			} else {
				require.NoError(t, err)
				require.NotNil(t, out)
				if tt.checkFunc != nil {
					tt.checkFunc(t, out, agentRepo)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestAgentService_Authenticate
// ---------------------------------------------------------------------------

func TestAgentService_Authenticate(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(svc *agentService, ws *domain.Workspace) string // returns the raw key (or a bad key)
		slug    func(ws *domain.Workspace) string
		wantErr bool
		errCode int
	}{
		{
			name: "valid key succeeds",
			setup: func(svc *agentService, ws *domain.Workspace) string {
				out, err := svc.Register(context.Background(), RegisterAgentInput{
					WorkspaceID: ws.ID,
					Name:        "Auth Test Agent",
					AgentType:   domain.AgentTypeClaudeCode,
				})
				require.NoError(t, err)
				return out.APIKey
			},
			slug: func(ws *domain.Workspace) string {
				return ws.Slug
			},
			wantErr: false,
		},
		{
			name: "wrong key fails",
			setup: func(svc *agentService, ws *domain.Workspace) string {
				_, err := svc.Register(context.Background(), RegisterAgentInput{
					WorkspaceID: ws.ID,
					Name:        "Auth Test Agent 2",
					AgentType:   domain.AgentTypeClaudeCode,
				})
				require.NoError(t, err)
				return "agk_acme_00000000000000000000000000000000000000000000dead"
			},
			slug: func(ws *domain.Workspace) string {
				return ws.Slug
			},
			wantErr: true,
			errCode: http.StatusUnauthorized,
		},
		{
			name: "non-existent workspace slug fails",
			setup: func(_ *agentService, _ *domain.Workspace) string {
				return "agk_nonexistent_abcdef1234567890abcdef1234567890abcdef1234567890"
			},
			slug: func(_ *domain.Workspace) string {
				return "nonexistent"
			},
			wantErr: true,
			errCode: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, ws := setupAgentService()
			ctx := context.Background()
			rawKey := tt.setup(svc, ws)
			slug := tt.slug(ws)

			agent, err := svc.Authenticate(ctx, slug, rawKey)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
				assert.Nil(t, agent)
			} else {
				require.NoError(t, err)
				require.NotNil(t, agent)
				assert.Equal(t, ws.ID, agent.WorkspaceID)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestAgentService_Heartbeat
// ---------------------------------------------------------------------------

func TestAgentService_Heartbeat(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(svc *agentService, ws *domain.Workspace, agentRepo *MockAgentRepository) uuid.UUID
		wantErr   bool
		checkFunc func(t *testing.T, agentRepo *MockAgentRepository, agentID uuid.UUID)
	}{
		{
			name: "success - updates status to online",
			setup: func(svc *agentService, ws *domain.Workspace, _ *MockAgentRepository) uuid.UUID {
				out, err := svc.Register(context.Background(), RegisterAgentInput{
					WorkspaceID: ws.ID,
					Name:        "Heartbeat Agent",
					AgentType:   domain.AgentTypeClaudeCode,
				})
				require.NoError(t, err)
				return out.Agent.ID
			},
			wantErr: false,
			checkFunc: func(t *testing.T, agentRepo *MockAgentRepository, agentID uuid.UUID) {
				agent := agentRepo.items[agentID]
				require.NotNil(t, agent)
				assert.Equal(t, domain.AgentStatusOnline, agent.Status)
				require.NotNil(t, agent.LastHeartbeat)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, agentRepo, ws := setupAgentService()
			ctx := context.Background()
			agentID := tt.setup(svc, ws, agentRepo)

			err := svc.Heartbeat(ctx, agentID, nil)

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				if tt.checkFunc != nil {
					tt.checkFunc(t, agentRepo, agentID)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestAgentService_RotateAPIKey
// ---------------------------------------------------------------------------

func TestAgentService_RotateAPIKey(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(svc *agentService, ws *domain.Workspace) (agentID uuid.UUID, oldKey string)
		wantErr   bool
		errCode   int
		checkFunc func(t *testing.T, svc *agentService, ws *domain.Workspace, agentID uuid.UUID, oldKey, newKey string)
	}{
		{
			name: "generates new key - old key no longer works",
			setup: func(svc *agentService, ws *domain.Workspace) (uuid.UUID, string) {
				out, err := svc.Register(context.Background(), RegisterAgentInput{
					WorkspaceID: ws.ID,
					Name:        "Rotate Agent",
					AgentType:   domain.AgentTypeCline,
				})
				require.NoError(t, err)
				return out.Agent.ID, out.APIKey
			},
			wantErr: false,
			checkFunc: func(t *testing.T, svc *agentService, ws *domain.Workspace, agentID uuid.UUID, oldKey, newKey string) {
				// New key should have the correct format.
				assert.True(t, strings.HasPrefix(newKey, "agk_acme_"))
				assert.NotEqual(t, oldKey, newKey, "new key should differ from old key")

				ctx := context.Background()

				// Old key should fail authentication.
				_, err := svc.Authenticate(ctx, ws.Slug, oldKey)
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, http.StatusUnauthorized, apiErr.Code)

				// New key should succeed.
				agent, err := svc.Authenticate(ctx, ws.Slug, newKey)
				require.NoError(t, err)
				require.NotNil(t, agent)
				assert.Equal(t, agentID, agent.ID)
			},
		},
		{
			name: "agent not found",
			setup: func(_ *agentService, _ *domain.Workspace) (uuid.UUID, string) {
				return uuid.New(), ""
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, ws := setupAgentService()
			ctx := context.Background()
			agentID, oldKey := tt.setup(svc, ws)

			newKey, err := svc.RotateAPIKey(ctx, agentID)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
			} else {
				require.NoError(t, err)
				assert.NotEmpty(t, newKey)
				if tt.checkFunc != nil {
					tt.checkFunc(t, svc, ws, agentID, oldKey, newKey)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestAgentService_KeyExpiry
// ---------------------------------------------------------------------------

// TestAgentService_Register_SetsDefaultTTL verifies that newly registered agents
// receive an expires_at one year from registration time.
func TestAgentService_Register_SetsDefaultTTL(t *testing.T) {
	svc, _, ws := setupAgentService()

	out, err := svc.Register(context.Background(), RegisterAgentInput{
		WorkspaceID: ws.ID,
		Name:        "TTL Agent",
		AgentType:   domain.AgentTypeClaudeCode,
	})
	require.NoError(t, err)
	require.NotNil(t, out.Agent.ExpiresAt)
	expected := frozenTime.Add(agentKeyDefaultTTL)
	assert.Equal(t, expected, *out.Agent.ExpiresAt)
}

// TestAgentService_Authenticate_ExpiredKey verifies that an expired API key is rejected
// with 401 even when the bcrypt hash matches.
func TestAgentService_Authenticate_ExpiredKey(t *testing.T) {
	svc, agentRepo, ws := setupAgentService()

	out, err := svc.Register(context.Background(), RegisterAgentInput{
		WorkspaceID: ws.ID,
		Name:        "Expired Agent",
		AgentType:   domain.AgentTypeClaudeCode,
	})
	require.NoError(t, err)

	// Back-date the expiry so the key is already expired.
	past := frozenTime.Add(-24 * time.Hour)
	agentRepo.items[out.Agent.ID].ExpiresAt = &past

	_, err = svc.Authenticate(context.Background(), ws.Slug, out.APIKey)
	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusUnauthorized, apiErr.Code)
}

// TestAgentService_Authenticate_NilExpiresAt verifies that a key with no expiry (legacy
// agents where expires_at IS NULL) continues to authenticate successfully.
func TestAgentService_Authenticate_NilExpiresAt(t *testing.T) {
	svc, agentRepo, ws := setupAgentService()

	out, err := svc.Register(context.Background(), RegisterAgentInput{
		WorkspaceID: ws.ID,
		Name:        "Legacy Agent",
		AgentType:   domain.AgentTypeClaudeCode,
	})
	require.NoError(t, err)

	// Clear expiry to simulate a pre-migration agent row (expires_at IS NULL).
	agentRepo.items[out.Agent.ID].ExpiresAt = nil

	agent, err := svc.Authenticate(context.Background(), ws.Slug, out.APIKey)
	require.NoError(t, err)
	require.NotNil(t, agent)
}

// TestAgentService_RotateAPIKey_UpdatesExpiry verifies that rotating a key resets
// last_rotated_at and extends expires_at by the default TTL.
func TestAgentService_RotateAPIKey_UpdatesExpiry(t *testing.T) {
	svc, agentRepo, ws := setupAgentService()

	out, err := svc.Register(context.Background(), RegisterAgentInput{
		WorkspaceID: ws.ID,
		Name:        "Rotate Expiry Agent",
		AgentType:   domain.AgentTypeClaudeCode,
	})
	require.NoError(t, err)

	// Back-date the expiry so it's almost expired.
	nearExpiry := frozenTime.Add(24 * time.Hour)
	agentRepo.items[out.Agent.ID].ExpiresAt = &nearExpiry

	_, err = svc.RotateAPIKey(context.Background(), out.Agent.ID)
	require.NoError(t, err)

	updated := agentRepo.items[out.Agent.ID]
	require.NotNil(t, updated.ExpiresAt)
	assert.Equal(t, frozenTime.Add(agentKeyDefaultTTL), *updated.ExpiresAt, "expiry must be reset to 1y from now")
	require.NotNil(t, updated.LastRotatedAt)
	assert.Equal(t, frozenTime, *updated.LastRotatedAt)
}
