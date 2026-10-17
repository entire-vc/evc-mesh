package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Same convention as agent_key_workspace_reuse_db_test.go: real Postgres through
// DATABASE_URL, skipped when unreachable. The extension actor and agent key
// services are thin over SQL (token lookup, revoke, grace window), and a mock of
// that SQL would prove nothing about it.

type extActorFixture struct {
	db   *sqlx.DB
	ws   *domain.Workspace
	conn *domain.IntegrationConfig
	svc  ExtensionActorService
}

func newExtActorFixture(t *testing.T) *extActorFixture {
	t.Helper()
	db := agentKeyWsReuseTestDB(t)
	ctx := context.Background()
	ws := &domain.Workspace{ID: uuid.New(), Name: "ext-svc", Slug: "ext-svc-" + uuid.New().String()[:8], OwnerID: agentKeyWsReuseOwner(t, db)}
	require.NoError(t, postgres.NewWorkspaceRepo(db).Create(ctx, ws))
	integ := postgres.NewIntegrationRepo(db)
	conn := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: ws.ID, Provider: domain.IntegrationProviderGitLab, IsActive: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, integ.Upsert(ctx, conn))
	return &extActorFixture{db: db, ws: ws, conn: conn,
		svc: NewExtensionActorService(postgres.NewExtensionActorRepo(db), integ, postgres.NewWorkspaceRepo(db))}
}

func TestCapabilitiesByKind(t *testing.T) {
	got := CapabilitiesByKind([]string{"api:tasks:write", "api:tasks:read", "events:task.created", "junk", ":x", "k:"})
	assert.Equal(t, map[string][]string{"api": {"tasks:read", "tasks:write"}, "events": {"task.created"}}, got)
	assert.Empty(t, CapabilitiesByKind(nil))
}

func TestExtensionPrincipal_HasAPIAndName(t *testing.T) {
	ws := uuid.New()
	p := &ExtensionPrincipal{ExtensionID: "gitlab", WorkspaceID: ws, Capabilities: map[string][]string{"api": {"tasks:read"}}}
	assert.True(t, p.HasAPI("tasks:read"))
	assert.False(t, p.HasAPI("tasks:write"))
	assert.False(t, (&ExtensionPrincipal{}).HasAPI("tasks:read"))
	assert.Equal(t, "ext:gitlab@"+ws.String(), p.Principal())
}

func TestExtensionActorService_IssueAuthenticateRoundTrip(t *testing.T) {
	f := newExtActorFixture(t)
	ctx := context.Background()

	issued, err := f.svc.Issue(ctx, f.ws.ID, f.conn.ID, "gitlab", "GitLab", []string{"api:tasks:read", "events:task.created"})
	require.NoError(t, err)
	assert.Equal(t, "ext:gitlab@"+f.ws.ID.String(), issued.Actor)
	assert.Contains(t, issued.Token, ExtensionTokenPrefix)

	p, err := f.svc.Authenticate(ctx, issued.Token)
	require.NoError(t, err)
	assert.Equal(t, f.ws.ID, p.WorkspaceID)
	assert.Equal(t, issued.ActorID, p.ActorID)
	assert.Equal(t, "GitLab", p.DisplayName)
	assert.True(t, p.HasAPI("tasks:read"))
	assert.Equal(t, []string{"task.created"}, p.Capabilities["events"])

	row, err := f.svc.Get(ctx, f.conn.ID)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, "ext:gitlab@"+f.ws.ID.String(), row.Principal())

	// Re-issuing re-arms the same connection with a new token; the old one dies.
	again, err := f.svc.Issue(ctx, f.ws.ID, f.conn.ID, "gitlab", "GitLab", []string{"api:tasks:read"})
	require.NoError(t, err)
	_, err = f.svc.Authenticate(ctx, issued.Token)
	assert.Error(t, err, "token of the previous issue")
	_, err = f.svc.Authenticate(ctx, again.Token)
	assert.NoError(t, err)
}

func TestExtensionActorService_IssueRejectsForeignOrMissingConnection(t *testing.T) {
	f := newExtActorFixture(t)
	ctx := context.Background()
	_, err := f.svc.Issue(ctx, uuid.New(), f.conn.ID, "gitlab", "GitLab", nil)
	assert.Error(t, err, "connection of another workspace")
	_, err = f.svc.Issue(ctx, f.ws.ID, uuid.New(), "gitlab", "GitLab", nil)
	assert.Error(t, err, "no such connection")
}

func TestExtensionActorService_AuthenticateDenials(t *testing.T) {
	f := newExtActorFixture(t)
	ctx := context.Background()
	issued, err := f.svc.Issue(ctx, f.ws.ID, f.conn.ID, "gitlab", "GitLab", []string{"api:tasks:read"})
	require.NoError(t, err)

	for _, tok := range []string{"", "agk_x", "ext_", "ext_unknown-token"} {
		_, err = f.svc.Authenticate(ctx, tok)
		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr, tok)
		assert.Equal(t, 401, apiErr.Code, tok)
	}

	// Unreadable stored capabilities mean an error, never "all".
	_, err = f.db.Exec(`UPDATE extension_actors SET capabilities = '[1]'::jsonb WHERE connection_id = $1`, f.conn.ID)
	require.NoError(t, err)
	_, err = f.svc.Authenticate(ctx, issued.Token)
	assert.Error(t, err)

	// Workspace gone -> actor gone with it (cascade), token no longer resolves.
	_, err = f.db.Exec(`DELETE FROM integration_configs WHERE id = $1`, f.conn.ID)
	require.NoError(t, err)
	_, err = f.svc.Authenticate(ctx, issued.Token)
	assert.Error(t, err)
}

func TestExtensionActorService_RotateAndRevoke(t *testing.T) {
	f := newExtActorFixture(t)
	ctx := context.Background()

	_, err := f.svc.Rotate(ctx, f.ws.ID, f.conn.ID)
	assert.Error(t, err, "nothing to rotate before issue")

	issued, err := f.svc.Issue(ctx, f.ws.ID, f.conn.ID, "gitlab", "GitLab", []string{"api:tasks:read"})
	require.NoError(t, err)

	rot, err := f.svc.Rotate(ctx, f.ws.ID, f.conn.ID)
	require.NoError(t, err)
	assert.True(t, rot.Rotated)
	assert.Equal(t, ExtensionTokenRotationGrace, rot.Grace)
	_, err = f.svc.Authenticate(ctx, issued.Token)
	assert.NoError(t, err, "old token inside grace")

	_, err = f.svc.Rotate(ctx, uuid.New(), f.conn.ID)
	assert.Error(t, err, "other workspace")

	require.NoError(t, f.svc.Revoke(ctx, f.conn.ID))
	require.NoError(t, f.svc.Revoke(ctx, f.conn.ID), "idempotent")
	for _, tok := range []string{issued.Token, rot.Token} {
		_, err = f.svc.Authenticate(ctx, tok)
		assert.Error(t, err)
	}
	_, err = f.svc.Rotate(ctx, f.ws.ID, f.conn.ID)
	assert.Error(t, err, "revoked actor cannot rotate")

	// Reconnect re-arms it.
	re, err := f.svc.Issue(ctx, f.ws.ID, f.conn.ID, "gitlab", "GitLab", []string{"api:tasks:read"})
	require.NoError(t, err)
	_, err = f.svc.Authenticate(ctx, re.Token)
	assert.NoError(t, err)
}

func TestExtensionActorService_ViolationsCounted(t *testing.T) {
	f := newExtActorFixture(t)
	ctx := context.Background()
	_, err := f.svc.Issue(ctx, f.ws.ID, f.conn.ID, "gitlab", "GitLab", nil)
	require.NoError(t, err)
	f.svc.RecordViolation(ctx, f.conn.ID, "POST /x requires api:tasks:write")
	f.svc.RecordViolation(ctx, f.conn.ID, "GET /y is not available to extensions")
	row, err := f.svc.Get(ctx, f.conn.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), row.ViolationCount)
	assert.Equal(t, "GET /y is not available to extensions", row.LastViolation)
	require.NotNil(t, row.LastViolationAt)

	none, err := f.svc.Get(ctx, uuid.New())
	require.NoError(t, err)
	assert.Nil(t, none)
	f.svc.RecordViolation(ctx, uuid.New(), "unknown connection must not panic or error out")
}

// ---- agent keys ----

type agentKeyFixture struct {
	db      *sqlx.DB
	ws      *domain.Workspace
	agents  AgentService
	keys    AgentKeyService
	agent   *domain.Agent
	oldKey  string
	ctx     context.Context
	byActor uuid.UUID
}

func newAgentKeyFixture(t *testing.T) *agentKeyFixture {
	t.Helper()
	db := agentKeyWsReuseTestDB(t)
	owner := agentKeyWsReuseOwner(t, db)
	wsRepo := postgres.NewWorkspaceRepo(db)
	ws := &domain.Workspace{ID: uuid.New(), Name: "ak-svc", Slug: "ak-svc-" + uuid.New().String()[:8], OwnerID: owner}
	require.NoError(t, wsRepo.Create(context.Background(), ws))

	agentRepo := postgres.NewAgentRepo(db)
	actLog := postgres.NewActivityLogRepo(db)
	userRepo := postgres.NewUserRepo(db)
	agents := NewCachedAgentAuth(NewAgentService(agentRepo, actLog, wsRepo, userRepo), AgentAuthCacheTTL)
	agents.(AgentServiceConfigurable).SetAgentWorkspaceGrantRepo(postgres.NewAgentWorkspaceGrantRepo(db))
	keyRepo := postgres.NewAgentAPIKeyRepo(db)
	agents.(AgentAPIKeyConfigurable).SetAgentAPIKeyRepo(keyRepo)

	by := uuid.New()
	ctx := actorctx.WithActor(context.Background(), by, domain.ActorTypeUser)
	reg, err := agents.Register(ctx, RegisterAgentInput{WorkspaceID: ws.ID, Name: "Key Agent " + uuid.New().String()[:6], AgentType: domain.AgentTypeClaudeCode})
	require.NoError(t, err)
	return &agentKeyFixture{db: db, ws: ws, agents: agents, agent: reg.Agent, oldKey: reg.APIKey, ctx: ctx, byActor: by,
		keys: NewAgentKeyService(agentRepo, wsRepo, keyRepo, actLog, nil, agents.(AgentCacheInvalidator))}
}

func (f *agentKeyFixture) auth(key string) error {
	_, err := f.agents.Authenticate(context.Background(), f.ws.Slug, key)
	return err
}

func TestAgentKeyService_TwoPhase(t *testing.T) {
	f := newAgentKeyFixture(t)
	require.NoError(t, f.auth(f.oldKey))

	issued, err := f.keys.Issue(f.ctx, f.agent.ID, "  rotator  ", time.Hour)
	require.NoError(t, err)
	assert.Equal(t, "rotator", issued.Label)
	require.NotNil(t, issued.ExpiresAt)
	for i := 0; i < 2; i++ { // second pass hits the auth cache
		assert.NoError(t, f.auth(f.oldKey))
		assert.NoError(t, f.auth(issued.APIKey))
	}

	listed, err := f.keys.List(f.ctx, f.agent.ID)
	require.NoError(t, err)
	require.Len(t, listed, 2)
	assert.True(t, listed[0].Primary)
	assert.Nil(t, listed[0].RevokedAt)
	assert.Equal(t, issued.KeyID.String(), listed[1].ID)

	require.NoError(t, f.keys.Revoke(f.ctx, f.agent.ID, domain.AgentPrimaryKeyID))
	assert.Error(t, f.auth(f.oldKey), "old key after revoke, cache must not keep it")
	assert.NoError(t, f.auth(issued.APIKey))

	listed, err = f.keys.List(f.ctx, f.agent.ID)
	require.NoError(t, err)
	assert.NotNil(t, listed[0].RevokedAt, "primary shows as revoked")

	// The last active key cannot be revoked: 409, and it keeps working.
	err = f.keys.Revoke(f.ctx, f.agent.ID, issued.KeyID.String())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "409")
	assert.NoError(t, f.auth(issued.APIKey))
	listed, err = f.keys.List(f.ctx, f.agent.ID)
	require.NoError(t, err)
	assert.Nil(t, listed[1].RevokedAt)
	assert.NotEmpty(t, listed[1].Fingerprint)
	assert.NotNil(t, listed[1].LastUsedAt, "login stamps last_used")

	// Activity trail names both operations and never the key.
	var n int
	require.NoError(t, f.db.Get(&n, `SELECT count(*) FROM activity_log WHERE entity_id = $1 AND action IN ('key_issued','key_revoked')`, f.agent.ID))
	assert.Equal(t, 2, n)
	require.NoError(t, f.db.Get(&n, `SELECT count(*) FROM activity_log WHERE entity_id = $1 AND changes::text LIKE '%' || $2 || '%'`, f.agent.ID, issued.APIKey))
	assert.Zero(t, n, "raw key must not reach the activity log")
}

func TestAgentKeyService_ExpiredKeyIsRejected(t *testing.T) {
	f := newAgentKeyFixture(t)
	issued, err := f.keys.Issue(f.ctx, f.agent.ID, "short", 0)
	require.NoError(t, err)
	require.NoError(t, f.auth(issued.APIKey))
	_, err = f.db.Exec(`UPDATE agent_api_keys SET expires_at = timestamp '2000-01-01' WHERE id = $1`, issued.KeyID)
	require.NoError(t, err)
	f.agents.(AgentCacheInvalidator).InvalidateAgent(f.agent.ID)
	assert.Error(t, f.auth(issued.APIKey))
}

func TestAgentKeyService_Validation(t *testing.T) {
	f := newAgentKeyFixture(t)

	_, err := f.keys.Issue(f.ctx, uuid.New(), "x", 0)
	assert.Error(t, err, "unknown agent")
	long := make([]byte, 129)
	for i := range long {
		long[i] = 'a'
	}
	_, err = f.keys.Issue(f.ctx, f.agent.ID, string(long), 0)
	assert.Error(t, err, "label too long")

	assert.Error(t, f.keys.Revoke(f.ctx, f.agent.ID, "not-a-uuid"))
	assert.Error(t, f.keys.Revoke(f.ctx, f.agent.ID, uuid.New().String()), "unknown key")
	assert.Error(t, f.keys.Revoke(f.ctx, uuid.New(), domain.AgentPrimaryKeyID), "unknown agent")
	_, err = f.keys.List(f.ctx, uuid.New())
	assert.Error(t, err)

	for i := 0; i < maxActiveAgentKeys-1; i++ { // the original key counts as one
		_, err = f.keys.Issue(f.ctx, f.agent.ID, "k", 0)
		require.NoError(t, err)
	}
	_, err = f.keys.Issue(f.ctx, f.agent.ID, "one too many", 0)
	assert.Error(t, err, "active key cap, original included")
}

// A key of agent A in workspace W must not authenticate as another agent, and a
// key that merely shares the workspace prefix with a real one finds nothing.
func TestAgentKeyService_ForeignAndGarbageKeys(t *testing.T) {
	f := newAgentKeyFixture(t)
	other := newAgentKeyFixture(t)
	issued, err := f.keys.Issue(f.ctx, f.agent.ID, "a", 0)
	require.NoError(t, err)

	_, err = other.agents.Authenticate(context.Background(), other.ws.Slug, issued.APIKey)
	assert.Error(t, err, "key of one workspace in another")
	assert.Error(t, f.auth(issued.APIKey+"x"))
	assert.Error(t, f.auth("agk_"+f.ws.Slug+"_nonsense"))

	got, err := f.agents.Authenticate(context.Background(), f.ws.Slug, issued.APIKey)
	require.NoError(t, err)
	assert.Equal(t, f.agent.ID, got.ID)
}

func TestExtensionActorService_SelfRotate(t *testing.T) {
	f := newExtActorFixture(t)
	ctx := context.Background()
	issued, err := f.svc.Issue(ctx, f.ws.ID, f.conn.ID, "gitlab", "GitLab", []string{"api:ext_token:self_rotate"})
	require.NoError(t, err)
	p, err := f.svc.Authenticate(ctx, issued.Token)
	require.NoError(t, err)

	// Refusals before anything changes.
	_, err = f.svc.SelfRotate(ctx, nil, issued.Token, 0)
	assert.Error(t, err, "no principal")
	_, err = f.svc.SelfRotate(ctx, p, "agk_notext", 0)
	assert.Error(t, err, "not an ext_ token")
	_, err = f.svc.SelfRotate(ctx, p, issued.Token, ExtensionSelfRotateMaxGrace+time.Second)
	assert.Error(t, err, "grace above the maximum")
	_, err = f.svc.SelfRotate(ctx, p, "ext_unknown", 0)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 401, apiErr.Code, "token that is neither current nor previous")

	// Default grace; presented token stays valid as previous.
	rot, err := f.svc.SelfRotate(ctx, p, issued.Token, 0)
	require.NoError(t, err)
	assert.True(t, rot.Rotated)
	assert.Equal(t, ExtensionTokenRotationGrace, rot.Grace)
	assert.Equal(t, issued.ActorID, rot.ActorID)
	assert.NotEqual(t, issued.Token, rot.Token)
	_, err = f.svc.Authenticate(ctx, issued.Token)
	assert.NoError(t, err, "presented token inside grace")
	_, err = f.svc.Authenticate(ctx, rot.Token)
	assert.NoError(t, err)

	// Retry with the old (still valid previous) token keeps it valid.
	rot2, err := f.svc.SelfRotate(ctx, p, issued.Token, 10*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, 10*time.Minute, rot2.Grace)
	_, err = f.svc.Authenticate(ctx, issued.Token)
	assert.NoError(t, err, "caller's token survives the retry")
	_, err = f.svc.Authenticate(ctx, rot.Token)
	assert.Error(t, err, "token the caller never saw is dropped")

	// Previous token cannot end the window; the current one can, twice.
	err = f.svc.SelfRevokePrevious(ctx, p, issued.Token)
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 409, apiErr.Code)
	require.NoError(t, f.svc.SelfRevokePrevious(ctx, p, rot2.Token))
	require.NoError(t, f.svc.SelfRevokePrevious(ctx, p, rot2.Token), "idempotent")
	_, err = f.svc.Authenticate(ctx, issued.Token)
	assert.Error(t, err, "old token after revoke")
	_, err = f.svc.Authenticate(ctx, rot2.Token)
	assert.NoError(t, err)

	assert.Error(t, f.svc.SelfRevokePrevious(ctx, nil, rot2.Token))
	assert.Error(t, f.svc.SelfRevokePrevious(ctx, p, "agk_notext"))

	// Revoked actor: nothing rotates.
	require.NoError(t, f.svc.Revoke(ctx, f.conn.ID))
	_, err = f.svc.SelfRotate(ctx, p, rot2.Token, 0)
	assert.Error(t, err)
}
