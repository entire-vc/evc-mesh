package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestOAuthClient_IsCIMD(t *testing.T) {
	assert.True(t, (&OAuthClient{RegistrationType: "cimd"}).IsCIMD())
	assert.False(t, (&OAuthClient{RegistrationType: "dcr"}).IsCIMD())
	assert.False(t, (&OAuthClient{}).IsCIMD())
}

func TestOAuthAuthorizationCode_IsUsable(t *testing.T) {
	now := time.Now()
	used := now.Add(-time.Second)
	assert.True(t, (&OAuthAuthorizationCode{ExpiresAt: now.Add(time.Minute)}).IsUsable(now))
	assert.False(t, (&OAuthAuthorizationCode{ExpiresAt: now.Add(-time.Minute)}).IsUsable(now), "expired")
	assert.False(t, (&OAuthAuthorizationCode{ExpiresAt: now}).IsUsable(now), "expiry instant itself is not usable")
	assert.False(t, (&OAuthAuthorizationCode{ExpiresAt: now.Add(time.Minute), UsedAt: &used}).IsUsable(now), "already used = replay")
}

func TestOAuthGrant_IsRevoked(t *testing.T) {
	at := time.Now()
	assert.False(t, (&OAuthGrant{}).IsRevoked())
	assert.True(t, (&OAuthGrant{RevokedAt: &at}).IsRevoked())
}

func TestOAuthToken_IsUsable(t *testing.T) {
	now := time.Now()
	revoked := now.Add(-time.Second)
	assert.True(t, (&OAuthToken{ExpiresAt: now.Add(time.Hour)}).IsUsable(now))
	assert.False(t, (&OAuthToken{ExpiresAt: now.Add(-time.Hour)}).IsUsable(now), "expired")
	assert.False(t, (&OAuthToken{ExpiresAt: now}).IsUsable(now), "expiry instant itself is not usable")
	assert.False(t, (&OAuthToken{ExpiresAt: now.Add(time.Hour), RevokedAt: &revoked}).IsUsable(now), "revoked")
}
