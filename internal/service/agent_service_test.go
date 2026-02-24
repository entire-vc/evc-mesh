package service

import (
	"context"
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
	*MockActivityLogRepository,
	*MockWorkspaceRepository,
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

	svc := NewAgentService(agentRepo, activityRepo, wsRepo).(*agentService)

	// Freeze the clock.
	timeNow = func() time.Time { return frozenTime }

	return svc, agentRepo, activityRepo, wsRepo, ws
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
			svc, agentRepo, _, _, ws := setupAgentService()
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
			svc, _, _, _, ws := setupAgentService()
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
			svc, agentRepo, _, _, ws := setupAgentService()
			ctx := context.Background()
			agentID := tt.setup(svc, ws, agentRepo)

			err := svc.Heartbeat(ctx, agentID)

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
			svc, _, _, _, ws := setupAgentService()
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
