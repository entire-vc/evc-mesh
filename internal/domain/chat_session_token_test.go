package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestChatSessionToken_IsUsable(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Minute), now.Add(time.Minute)

	assert.True(t, (&ChatSessionToken{ExpiresAt: future}).IsUsable(now))
	assert.False(t, (&ChatSessionToken{ExpiresAt: past}).IsUsable(now), "expired")
	assert.False(t, (&ChatSessionToken{ExpiresAt: future, RevokedAt: &past}).IsUsable(now), "revoked")
	assert.True(t, (&ChatSessionToken{ExpiresAt: future, RevokedAt: &future}).IsUsable(now), "revocation not yet effective")
}

func TestChatSessionToken_SessionEnded(t *testing.T) {
	now := time.Now()
	superseded, stop := ChatSessionRevokedSuperseded, ChatSessionRevokedStop

	assert.False(t, (&ChatSessionToken{}).SessionEnded(), "live token")
	assert.False(t, (&ChatSessionToken{RevokedAt: &now, RevokedReason: &superseded}).SessionEnded(), "renewal keeps the session")
	assert.True(t, (&ChatSessionToken{RevokedAt: &now, RevokedReason: &stop}).SessionEnded())
	assert.True(t, (&ChatSessionToken{RevokedAt: &now}).SessionEnded(), "no reason ends the session")
}
