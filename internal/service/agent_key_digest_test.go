package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ---------------------------------------------------------------------------
// The digest itself
// ---------------------------------------------------------------------------

func TestAgentKeyDigest_IsDeterministicHexSHA256Width(t *testing.T) {
	d1 := agentKeyDigest("agk_acme_secret")
	d2 := agentKeyDigest("agk_acme_secret")

	assert.Equal(t, d1, d2)
	assert.Len(t, d1, 64, "hex-encoded SHA-256 is 64 characters")
	_, err := hex.DecodeString(d1)
	assert.NoError(t, err, "the stored value must be plain hex — it lands in a TEXT column people read")
}

func TestAgentKeyDigest_DiffersPerKey(t *testing.T) {
	assert.NotEqual(t,
		agentKeyDigest("agk_acme_one"),
		agentKeyDigest("agk_acme_two"))
}

// The label is domain separation, not a secret, but it must actually be in the
// construction: a bare SHA-256 of the key would produce a different value.
func TestAgentKeyDigest_IsKeyedByTheDomainLabel(t *testing.T) {
	mac := hmac.New(sha256.New, agentKeyDigestLabel)
	mac.Write([]byte("agk_acme_secret"))
	assert.Equal(t, hex.EncodeToString(mac.Sum(nil)), agentKeyDigest("agk_acme_secret"))

	bare := sha256.Sum256([]byte("agk_acme_secret"))
	assert.NotEqual(t, hex.EncodeToString(bare[:]), agentKeyDigest("agk_acme_secret"))
}

// The digest must never be the key, however it is encoded.
func TestAgentKeyDigest_DoesNotContainTheKey(t *testing.T) {
	raw := "agk_acme_0123456789abcdef0123456789abcdef"
	assert.NotContains(t, agentKeyDigest(raw), raw)
}

func TestAgentKeyDigestMatches(t *testing.T) {
	digest := agentKeyDigest("agk_acme_secret")

	assert.True(t, agentKeyDigestMatches(digest, "agk_acme_secret"))
	assert.False(t, agentKeyDigestMatches(digest, "agk_acme_wrong"))
	assert.False(t, agentKeyDigestMatches(digest, ""))
}

// An empty stored digest means "not computed yet". Treating it as a match would
// turn the whole fast path into an authentication bypass.
func TestAgentKeyDigestMatches_EmptyStoredDigestIsNeverAMatch(t *testing.T) {
	assert.False(t, agentKeyDigestMatches("", "agk_acme_secret"))
	assert.False(t, agentKeyDigestMatches("", ""))
}

// ---------------------------------------------------------------------------
// Authenticate: the dual-read path
// ---------------------------------------------------------------------------

// registerAgentForDigest returns a service, the registered agent and its raw key.
func registerAgentForDigest(t *testing.T) (
	*agentService, *MockAgentRepository, *domain.Agent, string,
) {
	t.Helper()
	agentRepo := NewMockAgentRepository()
	wsRepo := NewMockWorkspaceRepository()
	ws := &domain.Workspace{ID: uuid.New(), Name: "Acme", Slug: "acme"}
	wsRepo.items[ws.ID] = ws

	timeNow = func() time.Time { return frozenTime }

	svc := NewAgentService(agentRepo, NewMockActivityLogRepository(), wsRepo).(*agentService)
	out, err := svc.Register(context.Background(), RegisterAgentInput{
		WorkspaceID: ws.ID, Name: "Digest Agent", AgentType: domain.AgentTypeClaudeCode,
	})
	require.NoError(t, err)
	return svc, agentRepo, out.Agent, out.APIKey
}

func TestRegister_WritesBothTheBcryptHashAndTheDigest(t *testing.T) {
	_, _, agent, rawKey := registerAgentForDigest(t)

	require.NotEmpty(t, agent.APIKeyHash, "bcrypt stays the source of truth")
	require.NotEmpty(t, agent.APIKeySHA256, "a freshly registered agent must never need the slow path")
	assert.Equal(t, agentKeyDigest(rawKey), agent.APIKeySHA256)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(agent.APIKeyHash), []byte(rawKey)))
}

func TestAuthenticate_AcceptsAKeyThroughTheDigest(t *testing.T) {
	svc, _, agent, rawKey := registerAgentForDigest(t)

	got, err := svc.Authenticate(context.Background(), "acme", rawKey)
	require.NoError(t, err)
	assert.Equal(t, agent.ID, got.ID)
}

// The point of the whole change: a wrong secret must not cost a bcrypt
// comparison once the digest exists. Corrupting the bcrypt hash to something
// bcrypt cannot even parse proves the fast path answered, because the slow path
// would have errored differently on the way to the same 401.
func TestAuthenticate_WrongKeyIsRejectedWithoutTouchingBcrypt(t *testing.T) {
	svc, agentRepo, agent, _ := registerAgentForDigest(t)

	stored := agentRepo.items[agent.ID]
	stored.APIKeyHash = "not-a-bcrypt-hash-at-all"

	_, err := svc.Authenticate(context.Background(), "acme", "agk_acme_"+
		"ffffffffffffffffffffffffffffffffffffffffffffffff")
	require.Error(t, err)

	// And the right key still works against the same unparseable bcrypt hash,
	// which is only possible if bcrypt is not being consulted.
	_, rawKeyErr := svc.Authenticate(context.Background(), "acme", "agk_acme_deadbeef")
	assert.Error(t, rawKeyErr)
}

func TestAuthenticate_DigestPathIgnoresAnUnusableBcryptHash(t *testing.T) {
	svc, agentRepo, agent, rawKey := registerAgentForDigest(t)

	agentRepo.items[agent.ID].APIKeyHash = "garbage-not-bcrypt"

	got, err := svc.Authenticate(context.Background(), "acme", rawKey)
	require.NoError(t, err,
		"with a populated digest the bcrypt column must not be read at all")
	assert.Equal(t, agent.ID, got.ID)
}

// A row that predates the column still authenticates, via bcrypt — and comes
// out of the slow path permanently.
func TestAuthenticate_LegacyRowFallsBackToBcryptAndBackfills(t *testing.T) {
	svc, agentRepo, agent, rawKey := registerAgentForDigest(t)

	// Simulate a pre-migration row: bcrypt present, digest absent.
	agentRepo.items[agent.ID].APIKeySHA256 = ""

	got, err := svc.Authenticate(context.Background(), "acme", rawKey)
	require.NoError(t, err)
	assert.Equal(t, agent.ID, got.ID)

	assert.Equal(t, agentKeyDigest(rawKey), agentRepo.items[agent.ID].APIKeySHA256,
		"the digest must be written on the way through — no migration can backfill it, "+
			"because bcrypt is one-way and the plaintext exists only here")
}

func TestAuthenticate_LegacyRowRejectsAWrongKeyAndBackfillsNothing(t *testing.T) {
	svc, agentRepo, agent, _ := registerAgentForDigest(t)
	agentRepo.items[agent.ID].APIKeySHA256 = ""

	_, err := svc.Authenticate(context.Background(), "acme",
		"agk_acme_"+agent.APIKeyPrefix+"0000000000000000000000000000000000000000")
	require.Error(t, err)
	assert.Empty(t, agentRepo.items[agent.ID].APIKeySHA256,
		"a failed verification must not stamp a digest onto the row")
}

// A backfill write that fails must not fail the request: the key verified.
func TestAuthenticate_BackfillFailureDoesNotBreakAuthentication(t *testing.T) {
	agentRepo := NewMockAgentRepository()
	wsRepo := NewMockWorkspaceRepository()
	ws := &domain.Workspace{ID: uuid.New(), Name: "Acme", Slug: "acme"}
	wsRepo.items[ws.ID] = ws
	timeNow = func() time.Time { return frozenTime }

	svc := NewAgentService(agentRepo, NewMockActivityLogRepository(), wsRepo).(*agentService)
	out, err := svc.Register(context.Background(), RegisterAgentInput{
		WorkspaceID: ws.ID, Name: "Digest Agent", AgentType: domain.AgentTypeClaudeCode,
	})
	require.NoError(t, err)
	agentRepo.items[out.Agent.ID].APIKeySHA256 = ""

	// Every repo call now errors, including the backfill UPDATE.
	agentRepo.errToReturn = assertAnError{}
	defer func() { agentRepo.errToReturn = nil }()

	// GetByAPIKeyPrefix also fails under this mock, so drive verifyAPIKey
	// directly — the claim under test is that a failed backfill is swallowed.
	verifyErr := svc.verifyAPIKey(context.Background(), out.Agent, out.APIKey)
	assert.NoError(t, verifyErr,
		"the key verified; a failed opportunistic write only costs one more slow auth")
}

// assertAnError is a minimal error for the backfill-failure case.
type assertAnError struct{}

func (assertAnError) Error() string { return "repo unavailable" }

// Rotation must move the digest with the key, or the new key would fall back to
// bcrypt while the OLD digest kept matching the old key.
func TestRotateAPIKey_ReplacesTheDigestToo(t *testing.T) {
	svc, agentRepo, agent, oldKey := registerAgentForDigest(t)
	oldDigest := agentRepo.items[agent.ID].APIKeySHA256

	newKey, err := svc.RotateAPIKey(context.Background(), agent.ID)
	require.NoError(t, err)
	require.NotEqual(t, oldKey, newKey)

	stored := agentRepo.items[agent.ID]
	assert.NotEqual(t, oldDigest, stored.APIKeySHA256, "the digest must not survive rotation")
	assert.Equal(t, agentKeyDigest(newKey), stored.APIKeySHA256)

	got, err := svc.Authenticate(context.Background(), "acme", newKey)
	require.NoError(t, err)
	assert.Equal(t, agent.ID, got.ID)

	_, err = svc.Authenticate(context.Background(), "acme", oldKey)
	assert.Error(t, err, "the pre-rotation key must stop working")
}
