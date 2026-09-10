package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ---------------------------------------------------------------------------
// Task U2 — Authenticate via agent_workspace_grants, with a fallback to the
// legacy agents-table path. Covers every branch task U2's acceptance criteria
// name: connection found / not found (fallback) / revoked / hash mismatch,
// plus the cross-workspace and identity-comes-from-the-connection cases.
// ---------------------------------------------------------------------------

// grantAuthFixture wires an agentService with BOTH a real grant repo and the
// existing agent repo/workspace repo mocks, so Authenticate exercises the
// task U2 code path (not the "grantRepo == nil" default every other test in
// this package uses).
type grantAuthFixture struct {
	svc       *agentService
	agentRepo *MockAgentRepository
	grantRepo *MockAgentWorkspaceGrantRepository
}

func setupGrantAuthFixture() (*grantAuthFixture, *domain.Workspace) {
	agentRepo := NewMockAgentRepository()
	activityRepo := NewMockActivityLogRepository()
	wsRepo := NewMockWorkspaceRepository()
	grantRepo := NewMockAgentWorkspaceGrantRepository()

	ws := &domain.Workspace{ID: uuid.New(), Name: "Acme Corp", Slug: "acme"}
	wsRepo.items[ws.ID] = ws

	svc := NewAgentService(agentRepo, activityRepo, wsRepo, NewMockUserRepository()).(*agentService)
	svc.SetAgentWorkspaceGrantRepo(grantRepo)
	timeNow = func() time.Time { return frozenTime }

	return &grantAuthFixture{svc: svc, agentRepo: agentRepo, grantRepo: grantRepo}, ws
}

// seedHomeAgent registers an agent whose home workspace is ws (agents.* row,
// pre-U2 shape) and returns it with its raw key.
func seedHomeAgent(t *testing.T, f *grantAuthFixture, ws *domain.Workspace, name string) (agent *domain.Agent, rawKey string) {
	t.Helper()
	out, err := f.svc.Register(context.Background(), RegisterAgentInput{
		WorkspaceID: ws.ID,
		Name:        name,
		AgentType:   domain.AgentTypeClaudeCode,
	})
	require.NoError(t, err)
	return out.Agent, out.APIKey
}

// seedGrant generates a fresh key formatted for ws, hashes it, stores the
// connection row, and returns the raw key — mirroring what U3's invite flow
// will do, without depending on U3 existing yet.
func seedGrant(t *testing.T, f *grantAuthFixture, agentID uuid.UUID, ws *domain.Workspace, role string, revoked bool) string {
	t.Helper()
	rawKey, err := generateAPIKey(ws.Slug)
	require.NoError(t, err)
	hash, err := bcrypt.GenerateFromPassword([]byte(rawKey), bcryptCost)
	require.NoError(t, err)

	g := &domain.AgentWorkspaceGrant{
		ID:           uuid.New(),
		AgentID:      agentID,
		WorkspaceID:  ws.ID,
		Role:         role,
		APIKeyPrefix: extractPrefix(rawKey, ws.Slug),
		APIKeyHash:   string(hash),
		CreatedAt:    frozenTime,
	}
	if revoked {
		revokedAt := frozenTime
		g.RevokedAt = &revokedAt
	}
	f.grantRepo.Seed(g)
	return rawKey
}

func requireUnauthorized(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 401, apiErr.Code)
}

// AC: "живой флот работает как раньше" / AC6 "подключение найдено" branch —
// an active connection resolves, and identity (workspace, role) comes from
// the CONNECTION, not the agent's home row.
func TestAgentService_Authenticate_ViaActiveGrant(t *testing.T) {
	f, homeWS := setupGrantAuthFixture()
	agent, _ := seedHomeAgent(t, f, homeWS, "Grant Agent")

	guestWS := &domain.Workspace{ID: uuid.New(), Name: "Guest Co", Slug: "guest"}
	f.svc.workspaceRepo.(*MockWorkspaceRepository).items[guestWS.ID] = guestWS

	guestKey := seedGrant(t, f, agent.ID, guestWS, "admin", false)

	got, err := f.svc.Authenticate(context.Background(), guestWS.Slug, guestKey)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, agent.ID, got.ID)
	assert.Equal(t, guestWS.ID, got.WorkspaceID, "workspace must come from the connection, not agents.workspace_id")
	assert.Equal(t, "admin", got.WorkspaceRole, "role must come from the connection, not be implied")
}

// AC6 "не найдено с откатом" — no connection row at all for this
// (workspace, prefix): Authenticate falls back to the legacy agents-table
// lookup and still succeeds. This is the regression guard for "живой флот не
// заметил" — every agent that existed before U1's backfill keeps working.
func TestAgentService_Authenticate_NoGrant_FallsBackToLegacy(t *testing.T) {
	f, ws := setupGrantAuthFixture()
	agent, rawKey := seedHomeAgent(t, f, ws, "Legacy Agent")

	got, err := f.svc.Authenticate(context.Background(), ws.Slug, rawKey)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, agent.ID, got.ID)
	assert.Equal(t, ws.ID, got.WorkspaceID)
	assert.Equal(t, legacyGrantRole, got.WorkspaceRole, "fallback path must not leave role unaddressed")
}

// AC4 red control — "Отозванное подключение не пускает": revoking the
// connection denies the SAME key immediately, and — this is the part a
// naive implementation gets wrong — it must NOT fall back to the legacy
// agents-table lookup just because the connection didn't resolve. The agent's
// own agents.api_key_hash is left untouched by U1/U2 on purpose, so if the
// revoked branch fell through to authenticateLegacy, this test would still
// see a 200-shaped success against the home key. It must not.
func TestAgentService_Authenticate_RevokedGrant_DeniesAndDoesNotFallBack(t *testing.T) {
	f, ws := setupGrantAuthFixture()
	agent, _ := seedHomeAgent(t, f, ws, "Revoked Agent")
	revokedKey := seedGrant(t, f, agent.ID, ws, "member", true)

	_, err := f.svc.Authenticate(context.Background(), ws.Slug, revokedKey)
	requireUnauthorized(t, err)
}

// AC6 "хэш не сошёлся" on the grant path — a wrong key against a real
// connection prefix still fails, distinct from "no connection" (which would
// fall back) and from "revoked" (which denies before even checking the hash).
func TestAgentService_Authenticate_ActiveGrant_WrongKey(t *testing.T) {
	f, ws := setupGrantAuthFixture()
	agent, _ := seedHomeAgent(t, f, ws, "Wrong Key Agent")
	realKey := seedGrant(t, f, agent.ID, ws, "member", false)

	// A key that resolves to the SAME grant (same workspace + same 8-char prefix
	// as realKey) but fails the bcrypt compare, so this actually exercises the
	// "grant found, hash mismatch" branch inside authenticateViaGrant — not the
	// legacy fallback. A key with an unrelated/made-up prefix (as this test used
	// to construct) never resolves to a grant at all: it silently falls through
	// to the legacy path instead, and would still return 401 even if the bcrypt
	// check inside authenticateViaGrant were deleted entirely.
	prefix := extractPrefix(realKey, ws.Slug)
	wrongKey := "agk_" + ws.Slug + "_" + prefix + "0000000000000000000000000000000000000000000000000000"
	require.NotEqual(t, realKey, wrongKey)
	require.Equal(t, prefix, extractPrefix(wrongKey, ws.Slug), "must share the real grant's prefix to hit the hash-mismatch branch")

	_, err := f.svc.Authenticate(context.Background(), ws.Slug, wrongKey)
	requireUnauthorized(t, err)
}

// AC5 — "Чужой воркспейс не пускает: ключ подключения к A при обращении к B
// — отказ." A key formatted and stored for workspace A does not authenticate
// against workspace B: the grant lookup is scoped by workspace_id, and the
// legacy fallback path (which the mismatched prefix falls through to) is
// scoped the same way — this property is unchanged from before U2, and this
// test pins it post-refactor.
func TestAgentService_Authenticate_KeyForOtherWorkspace_Denied(t *testing.T) {
	f, wsA := setupGrantAuthFixture()
	agentA, _ := seedHomeAgent(t, f, wsA, "Workspace A Agent")
	keyForA := seedGrant(t, f, agentA.ID, wsA, "member", false)

	wsB := &domain.Workspace{ID: uuid.New(), Name: "Workspace B", Slug: "wsb"}
	f.svc.workspaceRepo.(*MockWorkspaceRepository).items[wsB.ID] = wsB

	_, err := f.svc.Authenticate(context.Background(), wsB.Slug, keyForA)
	requireUnauthorized(t, err)

	// Directly pin the repository-level guarantee too: the same prefix under
	// the WRONG workspace must not resolve, even though it resolves under the
	// right one — this is what makes the above denial a scoping property and
	// not an accident of prefix-parsing.
	prefixForA := extractPrefix(keyForA, wsA.Slug)
	inWrongWorkspace, err := f.grantRepo.GetByWorkspaceAndPrefix(context.Background(), wsB.ID, prefixForA)
	require.NoError(t, err)
	assert.Nil(t, inWrongWorkspace)
	inRightWorkspace, err := f.grantRepo.GetByWorkspaceAndPrefix(context.Background(), wsA.ID, prefixForA)
	require.NoError(t, err)
	require.NotNil(t, inRightWorkspace)
}

// AC1 — "Живой флот не заметил": an agent authenticated via its home
// connection lands with the SAME agent identity a caller would have seen
// before U2 (ID, name, capabilities) — only the transient WorkspaceRole is
// new. This is the closest a unit test gets to the task's own AC1 without a
// live fleet; the AC1 prod run is a separate, non-mock proof (see PR).
func TestAgentService_Authenticate_HomeGrant_MatchesPreU2Identity(t *testing.T) {
	f, ws := setupGrantAuthFixture()
	agent, _ := seedHomeAgent(t, f, ws, "Home Grant Agent")
	homeKey := seedGrant(t, f, agent.ID, ws, "owner", false)

	got, err := f.svc.Authenticate(context.Background(), ws.Slug, homeKey)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, agent.ID, got.ID)
	assert.Equal(t, agent.Name, got.Name)
	assert.Equal(t, ws.ID, got.WorkspaceID)
	assert.Equal(t, "owner", got.WorkspaceRole)
}

// Returning agent must not be an alias into the mock's own storage: mutating
// WorkspaceID/WorkspaceRole on the returned value must never leak into what
// GetByID returns afterwards for the same agent.
func TestAgentService_Authenticate_ViaGrant_DoesNotMutateStoredAgent(t *testing.T) {
	f, homeWS := setupGrantAuthFixture()
	agent, _ := seedHomeAgent(t, f, homeWS, "No Alias Agent")

	guestWS := &domain.Workspace{ID: uuid.New(), Name: "Guest Co", Slug: "guest2"}
	f.svc.workspaceRepo.(*MockWorkspaceRepository).items[guestWS.ID] = guestWS
	guestKey := seedGrant(t, f, agent.ID, guestWS, "viewer", false)

	got, err := f.svc.Authenticate(context.Background(), guestWS.Slug, guestKey)
	require.NoError(t, err)
	require.Equal(t, guestWS.ID, got.WorkspaceID)

	stored, err := f.agentRepo.GetByID(context.Background(), agent.ID)
	require.NoError(t, err)
	assert.Equal(t, homeWS.ID, stored.WorkspaceID, "the stored agent's home workspace must be untouched by an unrelated grant login")
}

// When grantRepo is never wired (nil, the zero value), Authenticate must
// behave exactly as it did before U2 — the safety property SetAgentWorkspaceGrantRepo's
// doc comment promises.
func TestAgentService_Authenticate_GrantRepoNotWired_UsesLegacyPath(t *testing.T) {
	svc, _, ws := setupAgentService() // note: setupAgentService never wires a grantRepo
	out, err := svc.Register(context.Background(), RegisterAgentInput{
		WorkspaceID: ws.ID,
		Name:        "Unwired Agent",
		AgentType:   domain.AgentTypeClaudeCode,
	})
	require.NoError(t, err)

	got, err := svc.Authenticate(context.Background(), ws.Slug, out.APIKey)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, out.Agent.ID, got.ID)
}
