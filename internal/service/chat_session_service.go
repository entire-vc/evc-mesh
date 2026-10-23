package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

const (
	// ChatSessionTokenPrefix marks a cst_ chat-session bearer. The auth
	// middleware routes on it.
	ChatSessionTokenPrefix = "cst_"

	// chatSessionTokenTTL and chatSessionSupersedeOverlap are the E0.4 defaults
	// (5 minutes, renewal one minute ahead is the runner's job). The overlap is
	// how long the previous token keeps working after a renewal so a file swap in
	// the runner cannot strand an in-flight call; it is a bound, not a grace.
	chatSessionTokenTTL         = 5 * time.Minute
	chatSessionSupersedeOverlap = 10 * time.Second

	// chatSessionWorkspaceRole is the workspace_role WorkspaceRLS sees for a chat
	// token, same as an OAuth connector. What the token may DO is decided by
	// agentPerms ∩ the user's live role (middleware.clampConnector), not by this.
	chatSessionWorkspaceRole = "member"
)

// ChatSessionBinding is the immutable (user, agent, workspace, project, chat,
// session) tuple a token is issued for. Every ID comes from the runner's
// server-side chat record, never from model output.
type ChatSessionBinding struct {
	UserID         uuid.UUID
	AgentID        uuid.UUID
	WorkspaceID    uuid.UUID
	ProjectID      uuid.UUID
	ChatThreadID   uuid.UUID
	ChatSessionID  uuid.UUID
	RunnerID       string
	ExecutionEpoch int64
}

// ChatSessionIssued is what an issue/renew returns: the bearer (shown once) and
// the server-decided expiry.
type ChatSessionIssued struct {
	Token     string
	TokenID   uuid.UUID
	ExpiresAt time.Time
	Binding   ChatSessionBinding
}

// ChatSessionPrincipal is the verified identity behind a cst_ request: the
// agent that acts (a copy bound to the token's workspace) and the token record,
// whose UserID is the human the agent acts for.
type ChatSessionPrincipal struct {
	Agent *domain.Agent
	Token *domain.ChatSessionToken
	// UserRole is the user's workspace role as checkLive read it for this very
	// request (the workspace owner counts as owner even without a membership row).
	// The role clamp uses it instead of a second lookup that lacks the owner rule.
	UserRole string
}

// ChatSessionService issues, renews, revokes and authenticates chat-session tokens.
type ChatSessionService interface {
	Issue(ctx context.Context, b ChatSessionBinding) (*ChatSessionIssued, error)
	// Renew mints a fresh token for the session's existing binding after re-running
	// every liveness check. It never widens the binding and never revives a
	// stopped session.
	Renew(ctx context.Context, sessionID uuid.UUID, runnerID string, epoch int64) (*ChatSessionIssued, error)
	// RevokeSession is Stop / chat deletion: every token of the session dies now.
	RevokeSession(ctx context.Context, sessionID uuid.UUID) (int64, error)
	// Authenticate resolves a presented cst_ bearer. Anything wrong with it — unknown,
	// expired, revoked, user or agent no longer entitled — is a 401.
	Authenticate(ctx context.Context, rawToken string) (*ChatSessionPrincipal, error)
	PurgeExpired(ctx context.Context) (int64, error)
}

type chatSessionService struct {
	repo           repository.ChatSessionTokenRepository
	agentService   AgentService
	workspaceRepo  repository.WorkspaceRepository
	memberRepo     repository.WorkspaceMemberRepository
	projectRepo    repository.ProjectRepository
	projectMembers repository.ProjectMemberRepository
	agentGrantRepo repository.AgentWorkspaceGrantRepository
	ttl            time.Duration
}

// NewChatSessionService constructs the chat-session token service.
func NewChatSessionService(
	repo repository.ChatSessionTokenRepository,
	agentService AgentService,
	workspaceRepo repository.WorkspaceRepository,
	memberRepo repository.WorkspaceMemberRepository,
	projectRepo repository.ProjectRepository,
	projectMembers repository.ProjectMemberRepository,
	agentGrantRepo repository.AgentWorkspaceGrantRepository,
) ChatSessionService {
	return &chatSessionService{
		repo: repo, agentService: agentService, workspaceRepo: workspaceRepo,
		memberRepo: memberRepo, projectRepo: projectRepo, projectMembers: projectMembers,
		agentGrantRepo: agentGrantRepo, ttl: chatSessionTokenTTL,
	}
}

// SetTTLForTesting shortens the token lifetime so expiry can be exercised.
func (s *chatSessionService) SetTTLForTesting(ttl time.Duration) { s.ttl = ttl }

func (s *chatSessionService) Issue(ctx context.Context, b ChatSessionBinding) (*ChatSessionIssued, error) {
	if b.UserID == uuid.Nil || b.AgentID == uuid.Nil || b.WorkspaceID == uuid.Nil || b.ProjectID == uuid.Nil ||
		b.ChatThreadID == uuid.Nil || b.ChatSessionID == uuid.Nil || strings.TrimSpace(b.RunnerID) == "" || b.ExecutionEpoch < 0 {
		return nil, apierror.BadRequest("user_id, agent_id, workspace_id, project_id, chat_thread_id, chat_session_id, runner_id and execution_epoch are required")
	}
	if _, _, err := s.checkLive(ctx, b); err != nil {
		return nil, err
	}
	return s.mint(ctx, b)
}

func (s *chatSessionService) Renew(ctx context.Context, sessionID uuid.UUID, runnerID string, epoch int64) (*ChatSessionIssued, error) {
	latest, err := s.repo.LatestForSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if latest == nil {
		return nil, apierror.NotFound("chat session token")
	}
	if latest.SessionEnded() {
		return nil, apierror.Unauthorized("chat session has ended")
	}
	// Only the execution that holds the session renews it; a newer execution
	// comes in through Issue with a higher epoch, an older one is stale.
	if latest.RunnerID != runnerID || latest.ExecutionEpoch != epoch {
		return nil, apierror.Conflict("runner or execution epoch does not match the session")
	}
	b := ChatSessionBinding{
		UserID: latest.UserID, AgentID: latest.AgentID, WorkspaceID: latest.WorkspaceID, ProjectID: latest.ProjectID,
		ChatThreadID: latest.ChatThreadID, ChatSessionID: latest.ChatSessionID, RunnerID: latest.RunnerID, ExecutionEpoch: latest.ExecutionEpoch,
	}
	if _, _, err := s.checkLive(ctx, b); err != nil {
		return nil, err
	}
	return s.mint(ctx, b)
}

func (s *chatSessionService) mint(ctx context.Context, b ChatSessionBinding) (*ChatSessionIssued, error) {
	suffix, err := randomHex(32)
	if err != nil {
		return nil, err
	}
	raw := ChatSessionTokenPrefix + suffix
	now := timeNow()
	row := &domain.ChatSessionToken{
		ID: uuid.New(), TokenHash: sha256Hex(raw),
		UserID: b.UserID, AgentID: b.AgentID, WorkspaceID: b.WorkspaceID, ProjectID: b.ProjectID,
		ChatThreadID: b.ChatThreadID, ChatSessionID: b.ChatSessionID, RunnerID: b.RunnerID, ExecutionEpoch: b.ExecutionEpoch,
		IssuedAt: now, ExpiresAt: now.Add(s.ttl),
	}
	if err := s.repo.Issue(ctx, row, now.Add(chatSessionSupersedeOverlap)); err != nil {
		if errors.Is(err, repository.ErrChatEpochStale) {
			return nil, apierror.Conflict("execution epoch is stale for this chat session")
		}
		return nil, err
	}
	return &ChatSessionIssued{Token: raw, TokenID: row.ID, ExpiresAt: row.ExpiresAt, Binding: b}, nil
}

func (s *chatSessionService) RevokeSession(ctx context.Context, sessionID uuid.UUID) (int64, error) {
	return s.repo.RevokeSession(ctx, sessionID, domain.ChatSessionRevokedStop, timeNow())
}

func (s *chatSessionService) PurgeExpired(ctx context.Context) (int64, error) {
	return s.repo.PurgeExpired(ctx, timeNow().Add(-24*time.Hour))
}

func (s *chatSessionService) Authenticate(ctx context.Context, rawToken string) (*ChatSessionPrincipal, error) {
	if !strings.HasPrefix(rawToken, ChatSessionTokenPrefix) {
		return nil, apierror.Unauthorized("invalid chat session token")
	}
	row, err := s.repo.GetByHash(ctx, sha256Hex(rawToken))
	if err != nil {
		return nil, err
	}
	if row == nil || !row.IsUsable(timeNow()) {
		return nil, apierror.Unauthorized("invalid or expired chat session token")
	}
	// No cache: the user's membership and the agent's connection are re-read on
	// every request, so a revocation takes effect on the very next call.
	agent, userRole, err := s.checkLive(ctx, ChatSessionBinding{
		UserID: row.UserID, AgentID: row.AgentID, WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID,
	})
	if err != nil {
		var apiErr *apierror.Error
		if errors.As(err, &apiErr) {
			return nil, apierror.Unauthorized("chat session access has been revoked")
		}
		return nil, err
	}
	return &ChatSessionPrincipal{Agent: agent, Token: row, UserRole: userRole}, nil
}

// checkLive verifies, against current state, that the user may still act in the
// project and the agent is still connected to it. Lookup failures are errors
// (not denials) so callers never revoke on an outage; every definite "no" is a
// Forbidden apierror.
func (s *chatSessionService) checkLive(ctx context.Context, b ChatSessionBinding) (*domain.Agent, string, error) {
	deny := func(msg string) error { return apierror.Forbidden(msg) }

	ws, err := s.workspaceRepo.GetByID(ctx, b.WorkspaceID)
	if err != nil {
		return nil, "", err
	}
	if ws == nil {
		return nil, "", deny("workspace not found")
	}
	proj, err := s.projectRepo.GetByID(ctx, b.ProjectID)
	if err != nil {
		return nil, "", err
	}
	if proj == nil || proj.WorkspaceID != b.WorkspaceID {
		return nil, "", deny("project does not belong to the workspace")
	}

	// The user: still in the workspace, and in the project unless an owner/admin.
	role := ""
	if ws.OwnerID == b.UserID {
		role = domain.RoleOwner
	} else {
		m, merr := s.memberRepo.GetByWorkspaceAndUser(ctx, b.WorkspaceID, b.UserID)
		if merr != nil {
			return nil, "", merr
		}
		if m == nil {
			return nil, "", deny("user is not a workspace member")
		}
		role = m.Role
	}
	if role != domain.RoleOwner && role != domain.RoleAdmin {
		pm, perr := s.projectMembers.GetByProjectAndUser(ctx, b.ProjectID, b.UserID)
		if perr != nil {
			return nil, "", perr
		}
		if pm == nil {
			return nil, "", deny("user is not a project member")
		}
	}

	// The agent: exists, holds an active connection to this workspace, and is an
	// explicit member of the project (agents never get the owner/admin bypass).
	agent, err := s.agentService.GetByID(ctx, b.AgentID)
	if err != nil {
		var apiErr *apierror.Error
		if errors.As(err, &apiErr) && apiErr.Code == http.StatusNotFound {
			return nil, "", deny("agent not found")
		}
		return nil, "", err
	}
	if agent == nil {
		return nil, "", deny("agent not found")
	}
	if s.agentGrantRepo != nil {
		g, gerr := s.agentGrantRepo.GetByAgentAndWorkspace(ctx, b.AgentID, b.WorkspaceID)
		if gerr != nil {
			return nil, "", gerr
		}
		switch {
		case g != nil && g.IsRevoked():
			return nil, "", deny("agent has no active connection to the workspace")
		case g == nil && agent.WorkspaceID != b.WorkspaceID:
			// No connection row is the legacy single-workspace agent, valid only
			// in its home workspace — the same rule agk_ authentication applies.
			return nil, "", deny("agent has no connection to the workspace")
		}
	}
	am, err := s.projectMembers.GetByProjectAndAgent(ctx, b.ProjectID, b.AgentID)
	if err != nil {
		return nil, "", err
	}
	if am == nil {
		return nil, "", deny("agent is not a project member")
	}

	resolved := *agent
	resolved.WorkspaceID = b.WorkspaceID
	resolved.WorkspaceRole = chatSessionWorkspaceRole
	userID := b.UserID
	resolved.OAuthConnectorUserID = &userID
	return &resolved, role, nil
}
