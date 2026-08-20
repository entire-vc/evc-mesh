package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
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

// sameePrefixWrongKey returns a key that resolves to the SAME agent as rawKey —
// identical prefix — but carries a different secret.
//
// The prefix matters more than it looks. An earlier version of the test below
// used a key of all "f"s, whose prefix matched no agent at all, so
// GetByAPIKeyPrefix returned nil and Authenticate answered 401 from the lookup
// without ever reaching the verification branch. The test asserted an error and
// passed for the wrong reason; CI's diff-coverage gate is what caught it, by
// reporting the rejection branch as unexecuted.
func samePrefixWrongKey(rawKey string) string {
	// extractPrefix takes the first apiKeyPrefixLen chars after "agk_{slug}_",
	// so keeping that head and replacing the tail lands on the same agent.
	head := rawKey[:len("agk_acme_")+apiKeyPrefixLen]
	return head + strings.Repeat("0", len(rawKey)-len(head))
}

// The point of the whole change: once the digest exists, a wrong secret must be
// rejected without a bcrypt comparison.
//
// This is set up so the two paths give OPPOSITE answers, which is the only way
// to tell which one decided. The stored bcrypt hash is a valid hash OF THE WRONG
// KEY, so bcrypt would accept it; the digest is of the real key, so the digest
// rejects it. A 401 therefore proves the digest answered — had the code fallen
// through to bcrypt, this call would have succeeded.
func TestAuthenticate_WrongKeyIsRejectedWithoutTouchingBcrypt(t *testing.T) {
	svc, agentRepo, agent, rawKey := registerAgentForDigest(t)

	wrongKey := samePrefixWrongKey(rawKey)
	require.NotEqual(t, rawKey, wrongKey)
	require.Equal(t, extractPrefix(rawKey, "acme"), extractPrefix(wrongKey, "acme"),
		"the wrong key must resolve to the same agent, or this tests the lookup and not the branch")

	bcryptOfWrongKey, err := bcrypt.GenerateFromPassword([]byte(wrongKey), bcryptCost)
	require.NoError(t, err)
	agentRepo.items[agent.ID].APIKeyHash = string(bcryptOfWrongKey)

	_, err = svc.Authenticate(context.Background(), "acme", wrongKey)
	require.Error(t, err,
		"bcrypt would have accepted this key; rejecting it is what proves the digest decided")
}

// The mirror image, and the same trick in reverse: the stored bcrypt hash is a
// valid hash of a DIFFERENT key, so bcrypt would reject the real key. Accepting
// it proves bcrypt was never consulted.
func TestAuthenticate_DigestPathIsNotBackedByBcrypt(t *testing.T) {
	svc, agentRepo, agent, rawKey := registerAgentForDigest(t)

	bcryptOfSomethingElse, err := bcrypt.GenerateFromPassword(
		[]byte(samePrefixWrongKey(rawKey)), bcryptCost)
	require.NoError(t, err)
	agentRepo.items[agent.ID].APIKeyHash = string(bcryptOfSomethingElse)

	got, err := svc.Authenticate(context.Background(), "acme", rawKey)
	require.NoError(t, err,
		"with a populated digest the bcrypt column must not be read at all")
	assert.Equal(t, agent.ID, got.ID)
}

// A digest that is present but does not match must not fall through to bcrypt
// "just in case" — that would reopen the CPU-burn the digest closes.
func TestAuthenticate_MismatchedDigestDoesNotFallBackToBcrypt(t *testing.T) {
	svc, agentRepo, agent, rawKey := registerAgentForDigest(t)

	// Digest of some other key, bcrypt hash of the REAL key: bcrypt would say
	// yes, the digest says no. The digest must win.
	agentRepo.items[agent.ID].APIKeySHA256 = agentKeyDigest("agk_acme_something-else")

	_, err := svc.Authenticate(context.Background(), "acme", rawKey)
	require.Error(t, err,
		"a stale or wrong digest must reject outright; falling back to bcrypt would mean "+
			"an attacker can still force the slow path on every request")

	assert.NotEqual(t, agentKeyDigest(rawKey), agentRepo.items[agent.ID].APIKeySHA256,
		"and the rejected attempt must not have rewritten the digest — only the bcrypt "+
			"path is allowed to backfill")
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
