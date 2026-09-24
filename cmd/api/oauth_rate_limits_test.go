package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/entire-vc/evc-mesh/internal/config"
)

// token/revoke are sized by their own setting (#23579e6b), not by APIRPM; the
// other three endpoints keep the budgets they had.
func TestOAuthRateLimits_TokenUsesItsOwnSetting(t *testing.T) {
	cfg := &config.Config{RateLimit: config.RateLimitConfig{
		Enabled: true, AuthRPM: 5, RefreshRPM: 60, APIRPM: 600, OAuthTokenRPM: 2400,
	}}

	got := oauthRateLimits(cfg, true, nil)

	assert.Equal(t, 2400, got.Token, "token/revoke must read OAuthTokenRPM, not APIRPM")
	assert.Equal(t, 5, got.Register)
	assert.Equal(t, 60, got.Authorize)
	assert.Equal(t, 5, got.AuthorizeNewClient)
	assert.True(t, got.Enabled)
	assert.True(t, got.IPTrusted)
	assert.Nil(t, got.Redis)
}
