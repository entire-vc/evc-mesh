package service

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ---------------------------------------------------------------------------
// bcrypt's 72-byte input cap vs. agk_{slug}_{48 hex} agent keys (#9317fcd0).
// Key format overhead is fixed: "agk_" (4) + "_" (1) + 48 hex chars = 53
// bytes, so a slug of 19 chars produces exactly a 72-byte key (still
// bcrypt-legal) and 20 chars produces 73 (the first illegal length) — these
// tests pin that boundary at both the unit (bcryptInput) and integration
// (Register/RotateAPIKey/InviteAgent) levels, not just "long enough to fail".
// ---------------------------------------------------------------------------

func TestBcryptInput(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantDigest bool // true: output must equal agentKeyDigest(input); false: output must equal input verbatim
	}{
		{"empty", "", false},
		{"well under the cap", "agk_acme_deadbeef", false},
		{"exactly at the cap (72 bytes)", strings.Repeat("a", bcryptMaxInputBytes), false},
		{"one byte over the cap (73 bytes)", strings.Repeat("a", bcryptMaxInputBytes+1), true},
		{"well over the cap", strings.Repeat("a", 200), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bcryptInput(tt.input)
			if tt.wantDigest {
				assert.Equal(t, agentKeyDigest(tt.input), string(got),
					"over the cap must transform to the fixed-length digest")
				assert.LessOrEqual(t, len(got), bcryptMaxInputBytes,
					"the whole point: the transformed output must itself be bcrypt-legal")
			} else {
				assert.Equal(t, tt.input, string(got), "at or under the cap must pass through unchanged")
			}
		})
	}
}

// A digest-transformed input must still actually fit inside bcrypt's cap —
// this is the property TestBcryptInput's over-the-cap cases lean on; pinned
// separately so a future change to agentKeyDigest's output encoding (e.g.
// base64 instead of hex) that accidentally grew past 72 bytes would fail
// here specifically, not just look like an unrelated bcrypt error somewhere
// else.
func TestBcryptInput_DigestOutputFitsBcryptCap(t *testing.T) {
	digest := agentKeyDigest(strings.Repeat("x", 500))
	assert.LessOrEqual(t, len(digest), bcryptMaxInputBytes)
}

// slugKeyLen is the deterministic total length of a generated agent key for
// a slug of n characters: "agk_" (4) + slug (n) + "_" (1) + 48 hex chars.
func slugOfKeyLength(totalKeyLen int) string {
	const overhead = len("agk_") + len("_") + 48
	return strings.Repeat("s", totalKeyLen-overhead)
}

// The exact boundary from generateAPIKey's own math: a 19-char slug produces
// a 72-byte key (bcrypt-legal), a 20-char slug produces 73 (the first
// illegal length, and exactly what #9317fcd0 reproduced live).
func TestAgentService_Register_SlugAtBcryptBoundary(t *testing.T) {
	tests := []struct {
		name        string
		totalKeyLen int
	}{
		{"exactly 72 bytes (19-char slug) — must have worked before this fix too", bcryptMaxInputBytes},
		{"73 bytes (20-char slug) — the first length that used to 500", bcryptMaxInputBytes + 1},
		{"27-char slug — the exact live repro from the task (verifier-throwaway-71627c5a)", 53 + 27},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, ws := setupAgentService()
			ws.Slug = slugOfKeyLength(tt.totalKeyLen)

			out, err := svc.Register(context.Background(), RegisterAgentInput{
				WorkspaceID: ws.ID,
				Name:        "Long Slug Agent " + tt.name,
				AgentType:   domain.AgentTypeClaudeCode,
			})
			require.NoError(t, err, "must not 500 regardless of slug length")
			require.NotNil(t, out)
			assert.Len(t, out.APIKey, tt.totalKeyLen)

			// The returned key must actually authenticate, not just generate —
			// a hash that "succeeds" but never verifies would be a silent
			// dead-end worse than the 500 it replaces.
			got, err := svc.Authenticate(context.Background(), ws.Slug, out.APIKey)
			require.NoError(t, err, "the issued key must authenticate")
			assert.Equal(t, out.Agent.ID, got.ID)
		})
	}
}

// Same boundary, for the OTHER key-issuing path bcrypt.GenerateFromPassword
// runs on: RotateAPIKey.
func TestAgentService_RotateAPIKey_LongWorkspaceSlug_DoesNotFail(t *testing.T) {
	svc, agentRepo, ws := setupAgentService()
	ws.Slug = slugOfKeyLength(bcryptMaxInputBytes + 20) // comfortably over the cap

	out, err := svc.Register(context.Background(), RegisterAgentInput{
		WorkspaceID: ws.ID, Name: "Rotator", AgentType: domain.AgentTypeClaudeCode,
	})
	require.NoError(t, err)
	agentRepo.items[out.Agent.ID] = out.Agent

	newKey, err := svc.RotateAPIKey(context.Background(), out.Agent.ID)
	require.NoError(t, err, "rotate must not 500 regardless of slug length")
	assert.NotEqual(t, out.APIKey, newKey)

	got, err := svc.Authenticate(context.Background(), ws.Slug, newKey)
	require.NoError(t, err, "the rotated key must authenticate")
	assert.Equal(t, out.Agent.ID, got.ID)

	// And the superseded key must be dead, same as any other rotation.
	_, err = svc.Authenticate(context.Background(), ws.Slug, out.APIKey)
	require.Error(t, err, "the old key must no longer work")
}

// The third bcrypt.GenerateFromPassword call site: the U3 invite/grant path
// (agentWorkspaceGrantService.InviteAgent), which has NO digest fast path at
// all (AgentWorkspaceGrant carries no APIKeySHA256 column) — so this path
// depends on bcryptInput working correctly end to end even more directly
// than the legacy agents-table path does.
func TestInviteAgent_LongWorkspaceSlug_DoesNotFail(t *testing.T) {
	f, ws, agent := setupGrantSvcFixture()
	ws.Slug = slugOfKeyLength(bcryptMaxInputBytes + 20)

	result, err := f.svc.InviteAgent(context.Background(), ws.ID, agent.ID, domain.RoleMember, uuid.Nil)
	require.NoError(t, err, "invite must not 500 regardless of slug length")
	require.NotNil(t, result)

	// authenticateViaGrant always bcrypt-compares (no fast path for grants) —
	// this is the one call site where a bcryptInput mismatch between generate
	// and compare would be most visible in prod.
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(result.Grant.APIKeyHash), bcryptInput(result.APIKey)),
		"the stored hash must verify against the returned raw key through the same transform Authenticate uses")
}

// Backward-compatibility canary: a hash issued the OLD way — bcrypt over the
// raw key directly, with no transform — for a SHORT (bcrypt-legal) key must
// keep verifying after this fix. Every hash in the fleet today was issued
// this way (workspace slugs have stayed under the cap by convention), so
// this is the one behavior that must not change even by accident — a future
// "simplify bcryptInput to always transform" edit would silently break every
// existing agent's ability to authenticate, and this is the test that would
// catch it.
func TestAgentService_Authenticate_PreFixShortKeyHash_StillVerifies(t *testing.T) {
	svc, agentRepo, ws := setupAgentService()
	rawKey, err := generateAPIKey(ws.Slug) // "acme" — short, unaffected by the cap
	require.NoError(t, err)

	// Simulate a hash written before this fix existed: bcrypt over the raw
	// key, NOT through bcryptInput.
	oldStyleHash, err := bcrypt.GenerateFromPassword([]byte(rawKey), bcryptCost)
	require.NoError(t, err)

	agent := &domain.Agent{
		ID: uuid.New(), WorkspaceID: ws.ID, Name: "Legacy Agent", Slug: "legacy-agent",
		AgentType: domain.AgentTypeClaudeCode, APIKeyHash: string(oldStyleHash),
		APIKeyPrefix: extractPrefix(rawKey, ws.Slug), Status: domain.AgentStatusOffline,
	}
	agentRepo.items[agent.ID] = agent

	got, err := svc.Authenticate(context.Background(), ws.Slug, rawKey)
	require.NoError(t, err, "a pre-fix hash for a bcrypt-legal key must still verify")
	assert.Equal(t, agent.ID, got.ID)
}
