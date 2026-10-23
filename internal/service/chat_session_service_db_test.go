package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ChatSessionService against the real schema: issue, renew, revoke and the live
// re-check of user and agent that Authenticate repeats on every request. The
// HTTP-level behaviour is in internal/handler/chat_session_e2e_db_test.go; these
// pin the service's own decisions, each named by the failure it would let through.

type chatSvcEnv struct {
	*oauthSvcEnv
	chat    *chatSessionService
	ws      uuid.UUID
	project uuid.UUID
	owner   uuid.UUID
	agent   *domain.Agent
}

func newChatSvcEnv(t *testing.T) *chatSvcEnv {
	t.Helper()
	base := newOAuthSvcEnv(t)
	db := base.db
	userRepo := postgres.NewUserRepo(db)
	workspaceRepo := postgres.NewWorkspaceRepo(db)
	grantRepo := postgres.NewAgentWorkspaceGrantRepo(db)
	agentSvc := NewAgentService(postgres.NewAgentRepo(db), postgres.NewActivityLogRepo(db), workspaceRepo, userRepo)
	if c, ok := agentSvc.(AgentServiceConfigurable); ok {
		c.SetAgentWorkspaceGrantRepo(grantRepo)
	}
	svc := NewChatSessionService(postgres.NewChatSessionTokenRepo(db), agentSvc, workspaceRepo,
		postgres.NewWorkspaceMemberRepo(db), postgres.NewProjectRepo(db), postgres.NewProjectMemberRepo(db), grantRepo)

	env := &chatSvcEnv{oauthSvcEnv: base, chat: svc.(*chatSessionService)}
	env.owner, _ = base.createUser(t, "chat-owner")
	env.ws = base.createWorkspace(t, env.owner)
	env.project = base.createProject(t, env.ws, "chat project")

	reg, err := agentSvc.Register(context.Background(), RegisterAgentInput{
		WorkspaceID: env.ws, Name: "Chat Svc Agent " + uuid.New().String()[:8], AgentType: "custom",
	})
	require.NoError(t, err)
	env.agent = reg.Agent
	env.addAgentToProject(t, env.project)
	return env
}

func (env *chatSvcEnv) addAgentToProject(t *testing.T, project uuid.UUID) {
	t.Helper()
	_, err := env.db.Exec(`INSERT INTO project_members (id, project_id, workspace_id, agent_id)
		VALUES ($1, $2, (SELECT workspace_id FROM projects WHERE id = $2), $3)`, uuid.New(), project, env.agent.ID)
	require.NoError(t, err)
}

func (env *chatSvcEnv) binding(user uuid.UUID, epoch int64) ChatSessionBinding {
	return ChatSessionBinding{
		UserID: user, AgentID: env.agent.ID, WorkspaceID: env.ws, ProjectID: env.project,
		ChatThreadID: uuid.New(), ChatSessionID: uuid.New(), RunnerID: "runner-1", ExecutionEpoch: epoch,
	}
}

func requireChatErr(t *testing.T, err error, status int, msg string) {
	t.Helper()
	require.Error(t, err, msg)
	var apiErr *apierror.Error
	require.True(t, errors.As(err, &apiErr), "%s: not an apierror: %v", msg, err)
	assert.Equal(t, status, apiErr.Code, msg)
}

func TestChatSessionSvc_IssueRejectsAnIncompleteBinding(t *testing.T) {
	env := newChatSvcEnv(t)
	ctx := context.Background()
	for name, mutate := range map[string]func(*ChatSessionBinding){
		"no user":     func(b *ChatSessionBinding) { b.UserID = uuid.Nil },
		"no agent":    func(b *ChatSessionBinding) { b.AgentID = uuid.Nil },
		"no project":  func(b *ChatSessionBinding) { b.ProjectID = uuid.Nil },
		"no session":  func(b *ChatSessionBinding) { b.ChatSessionID = uuid.Nil },
		"no runner":   func(b *ChatSessionBinding) { b.RunnerID = "  " },
		"neg epoch":   func(b *ChatSessionBinding) { b.ExecutionEpoch = -1 },
		"no thread":   func(b *ChatSessionBinding) { b.ChatThreadID = uuid.Nil },
		"no workspce": func(b *ChatSessionBinding) { b.WorkspaceID = uuid.Nil },
	} {
		b := env.binding(env.owner, 1)
		mutate(&b)
		_, err := env.chat.Issue(ctx, b)
		requireChatErr(t, err, http.StatusBadRequest, name)
	}
}

func TestChatSessionSvc_IssueAuthenticateAndTheResolvedPrincipal(t *testing.T) {
	env := newChatSvcEnv(t)
	ctx := context.Background()
	b := env.binding(env.owner, 2)

	issued, err := env.chat.Issue(ctx, b)
	require.NoError(t, err)
	assert.Contains(t, issued.Token, ChatSessionTokenPrefix)
	assert.WithinDuration(t, time.Now().Add(chatSessionTokenTTL), issued.ExpiresAt, 5*time.Second)

	p, err := env.chat.Authenticate(ctx, issued.Token)
	require.NoError(t, err)
	assert.Equal(t, env.agent.ID, p.Agent.ID)
	assert.Equal(t, env.ws, p.Agent.WorkspaceID)
	assert.Equal(t, chatSessionWorkspaceRole, p.Agent.WorkspaceRole)
	require.NotNil(t, p.Agent.OAuthConnectorUserID, "the user the agent acts for must ride on the principal: that is what clamps its rights")
	assert.Equal(t, env.owner, *p.Agent.OAuthConnectorUserID)
	assert.Equal(t, b.ChatSessionID, p.Token.ChatSessionID)
	assert.Equal(t, int64(2), p.Token.ExecutionEpoch)

	// Only the hash is stored.
	var n int
	require.NoError(t, env.db.Get(&n, `SELECT count(*) FROM chat_session_tokens WHERE token_hash = $1`, issued.Token))
	assert.Zero(t, n, "the bearer itself must never be stored")
}

func TestChatSessionSvc_AuthenticateRefusesWhatIsNotAUsableToken(t *testing.T) {
	env := newChatSvcEnv(t)
	ctx := context.Background()

	_, err := env.chat.Authenticate(ctx, "agk_not-a-chat-token")
	requireChatErr(t, err, http.StatusUnauthorized, "wrong prefix")
	_, err = env.chat.Authenticate(ctx, ChatSessionTokenPrefix+"00000000000000000000000000000000")
	requireChatErr(t, err, http.StatusUnauthorized, "unknown token")

	env.chat.SetTTLForTesting(-time.Second)
	expired, err := env.chat.Issue(ctx, env.binding(env.owner, 1))
	require.NoError(t, err)
	_, err = env.chat.Authenticate(ctx, expired.Token)
	requireChatErr(t, err, http.StatusUnauthorized, "expired token")
}

func TestChatSessionSvc_IssueRefusesWhoIsNotEntitled(t *testing.T) {
	env := newChatSvcEnv(t)
	ctx := context.Background()

	stranger, _ := env.createUser(t, "stranger")
	_, err := env.chat.Issue(ctx, env.binding(stranger, 1))
	requireChatErr(t, err, http.StatusForbidden, "user outside the workspace")

	member, _ := env.createUser(t, "member")
	env.addMember(t, env.ws, member, domain.RoleMember)
	_, err = env.chat.Issue(ctx, env.binding(member, 1))
	requireChatErr(t, err, http.StatusForbidden, "workspace member who is not in the project")

	env.addProjectMember(t, env.project, member, domain.RoleMember)
	_, err = env.chat.Issue(ctx, env.binding(member, 1))
	assert.NoError(t, err, "a project member may")

	admin, _ := env.createUser(t, "admin")
	env.addMember(t, env.ws, admin, domain.RoleAdmin)
	_, err = env.chat.Issue(ctx, env.binding(admin, 1))
	assert.NoError(t, err, "an admin needs no explicit project membership")

	b := env.binding(env.owner, 1)
	b.WorkspaceID = uuid.New()
	_, err = env.chat.Issue(ctx, b)
	requireChatErr(t, err, http.StatusForbidden, "unknown workspace")

	otherWS := env.createWorkspace(t, env.owner)
	b = env.binding(env.owner, 1)
	b.WorkspaceID = otherWS
	_, err = env.chat.Issue(ctx, b)
	requireChatErr(t, err, http.StatusForbidden, "project of another workspace")

	b = env.binding(env.owner, 1)
	b.ProjectID = uuid.New()
	_, err = env.chat.Issue(ctx, b)
	requireChatErr(t, err, http.StatusForbidden, "unknown project")
}

func TestChatSessionSvc_IssueRefusesAnAgentThatIsNotConnectedToTheProject(t *testing.T) {
	env := newChatSvcEnv(t)
	ctx := context.Background()

	b := env.binding(env.owner, 1)
	b.AgentID = uuid.New()
	_, err := env.chat.Issue(ctx, b)
	requireChatErr(t, err, http.StatusForbidden, "unknown agent")

	otherProject := env.createProject(t, env.ws, "agent not in here")
	b = env.binding(env.owner, 1)
	b.ProjectID = otherProject
	_, err = env.chat.Issue(ctx, b)
	requireChatErr(t, err, http.StatusForbidden, "agent is not a member of the project (owner/admin bypass is for humans only)")

	_, err = env.db.Exec(`UPDATE agent_workspace_grants SET revoked_at = NOW() WHERE agent_id = $1 AND workspace_id = $2`, env.agent.ID, env.ws)
	require.NoError(t, err)
	_, err = env.chat.Issue(ctx, env.binding(env.owner, 1))
	requireChatErr(t, err, http.StatusForbidden, "revoked workspace connection")
}

func TestChatSessionSvc_RenewKeepsTheSessionAndRefusesTheWrongHolder(t *testing.T) {
	env := newChatSvcEnv(t)
	ctx := context.Background()
	b := env.binding(env.owner, 4)
	first, err := env.chat.Issue(ctx, b)
	require.NoError(t, err)

	_, err = env.chat.Renew(ctx, uuid.New(), "runner-1", 4)
	requireChatErr(t, err, http.StatusNotFound, "unknown session")
	_, err = env.chat.Renew(ctx, b.ChatSessionID, "runner-2", 4)
	requireChatErr(t, err, http.StatusConflict, "other runner")
	_, err = env.chat.Renew(ctx, b.ChatSessionID, "runner-1", 3)
	requireChatErr(t, err, http.StatusConflict, "other epoch")

	second, err := env.chat.Renew(ctx, b.ChatSessionID, "runner-1", 4)
	require.NoError(t, err)
	assert.NotEqual(t, first.Token, second.Token)
	assert.Equal(t, b, second.Binding, "a renewal never widens or changes the binding")

	_, err = env.chat.Authenticate(ctx, second.Token)
	assert.NoError(t, err, "the renewed token works")
	_, err = env.chat.Authenticate(ctx, first.Token)
	assert.NoError(t, err, "the superseded token still works inside the short overlap")

	// A renewal re-runs the liveness checks: a user removed from the workspace
	// cannot keep the session alive.
	_, err = env.db.Exec(`UPDATE workspaces SET owner_id = $2 WHERE id = $1`, env.ws, env.createOwner(t))
	require.NoError(t, err)
	_, err = env.chat.Renew(ctx, b.ChatSessionID, "runner-1", 4)
	requireChatErr(t, err, http.StatusForbidden, "renewal for a user who is no longer a member")
	_, err = env.chat.Authenticate(ctx, second.Token)
	requireChatErr(t, err, http.StatusUnauthorized, "authentication for a user who is no longer a member")
}

func (env *chatSvcEnv) createOwner(t *testing.T) uuid.UUID {
	t.Helper()
	id, _ := env.createUser(t, "new-owner")
	return id
}

func TestChatSessionSvc_StopEndsTheSessionForGood(t *testing.T) {
	env := newChatSvcEnv(t)
	ctx := context.Background()
	b := env.binding(env.owner, 1)
	issued, err := env.chat.Issue(ctx, b)
	require.NoError(t, err)

	n, err := env.chat.RevokeSession(ctx, b.ChatSessionID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)

	_, err = env.chat.Authenticate(ctx, issued.Token)
	requireChatErr(t, err, http.StatusUnauthorized, "token of a stopped session")
	_, err = env.chat.Renew(ctx, b.ChatSessionID, "runner-1", 1)
	requireChatErr(t, err, http.StatusUnauthorized, "renewing a stopped session")

	// A stop is final for that session id: neither the same epoch nor a higher one
	// brings it back (a restart is a new chat_session_id).
	_, err = env.chat.Issue(ctx, b)
	requireChatErr(t, err, http.StatusConflict, "re-issue at the epoch the stop ended")
	b.ExecutionEpoch = 2
	_, err = env.chat.Issue(ctx, b)
	requireChatErr(t, err, http.StatusConflict, "a stopped session is not revived by a higher epoch")
	restart := b
	restart.ChatSessionID = uuid.New()
	_, err = env.chat.Issue(ctx, restart)
	assert.NoError(t, err, "a restart under a new session id is a fresh session")

	b.ExecutionEpoch = 1
	_, err = env.chat.Issue(ctx, b)
	requireChatErr(t, err, http.StatusConflict, "stale epoch")
}

func TestChatSessionSvc_PurgeExpiredDropsOnlyLongDeadRows(t *testing.T) {
	env := newChatSvcEnv(t)
	ctx := context.Background()
	live, err := env.chat.Issue(ctx, env.binding(env.owner, 1))
	require.NoError(t, err)
	old := env.binding(env.owner, 1)
	oldIssued, err := env.chat.Issue(ctx, old)
	require.NoError(t, err)
	_, err = env.db.Exec(`UPDATE chat_session_tokens SET expires_at = NOW() - INTERVAL '2 days' WHERE id = $1`, oldIssued.TokenID)
	require.NoError(t, err)

	_, err = env.chat.PurgeExpired(ctx)
	require.NoError(t, err)

	var n int
	require.NoError(t, env.db.Get(&n, `SELECT count(*) FROM chat_session_tokens WHERE id = $1`, oldIssued.TokenID))
	assert.Zero(t, n, "a token expired two days ago is purged")
	require.NoError(t, env.db.Get(&n, `SELECT count(*) FROM chat_session_tokens WHERE id = $1`, live.TokenID))
	assert.Equal(t, 1, n, "a live token is kept")
}

func TestChatSessionSvc_AnotherRunnerAtTheSameEpochIsRefused(t *testing.T) {
	env := newChatSvcEnv(t)
	ctx := context.Background()
	b := env.binding(env.owner, 5)
	held, err := env.chat.Issue(ctx, b)
	require.NoError(t, err)

	b.RunnerID = "runner-2"
	_, err = env.chat.Issue(ctx, b)
	requireChatErr(t, err, http.StatusConflict, "second runner at the held epoch")
	_, err = env.chat.Authenticate(ctx, held.Token)
	assert.NoError(t, err, "the holder's token is untouched by the refused issue")

	b.ExecutionEpoch = 6
	_, err = env.chat.Issue(ctx, b)
	assert.NoError(t, err, "the same runner under a newer epoch takes the session")
}
