package handler

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sessionUsageFromRequest refuses every malformed turn field with a 400 reason
// naming the field; each refusal has a passing twin so the case proves the
// check, not a broken request.
func TestSessionUsageFromRequest_Refusals(t *testing.T) {
	i64 := func(v int64) *int64 { return &v }
	f64 := func(v float64) *float64 { return &v }
	str := func(v string) *string { return &v }
	long := strings.Repeat("x", maxUsageRefLen+1)

	cases := []struct {
		name    string
		bad     reportSessionRequest
		good    reportSessionRequest
		wantErr string
	}{
		{"tokens_out unknown with a figure",
			reportSessionRequest{TokensOut: i64(5), Unknown: []string{"tokens_out"}},
			reportSessionRequest{Unknown: []string{"tokens_out"}}, "tokens_out is listed as unknown"},
		{"estimated_cost unknown with a figure",
			reportSessionRequest{EstimatedCost: f64(0.1), Unknown: []string{"estimated_cost"}},
			reportSessionRequest{Unknown: []string{"estimated_cost"}}, "estimated_cost is listed as unknown"},
		{"cache_read unknown with a figure",
			reportSessionRequest{CacheRead: i64(3), Unknown: []string{"cache_read"}},
			reportSessionRequest{Unknown: []string{"cache_read"}}, "cache_read is listed as unknown"},
		{"cache_write unknown with a figure",
			reportSessionRequest{CacheWrite: i64(3), Unknown: []string{"cache_write"}},
			reportSessionRequest{Unknown: []string{"cache_write"}}, "cache_write is listed as unknown"},
		{"turn_id too long",
			reportSessionRequest{TurnID: str(long)},
			reportSessionRequest{TurnID: str(long[:maxUsageRefLen])}, "turn_id is longer"},
		{"provider too long",
			reportSessionRequest{Provider: str(strings.Repeat("p", 65))},
			reportSessionRequest{Provider: str(strings.Repeat("p", 64))}, "provider is longer"},
		{"account_ref too long",
			reportSessionRequest{AccountRef: str(long)},
			reportSessionRequest{AccountRef: str(long[:maxUsageRefLen])}, "account_ref is longer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sessionUsageFromRequest(tc.bad)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)

			u, err := sessionUsageFromRequest(tc.good)
			require.NoError(t, err)
			require.NotNil(t, u.Turn)
		})
	}
}

// A measure declared unknown is NULL in the journal and flags the session.
func TestSessionUsageFromRequest_UnknownMeasuresAreNull(t *testing.T) {
	u, err := sessionUsageFromRequest(reportSessionRequest{
		Unknown: []string{"tokens_out", "estimated_cost", "cache_write"},
	})
	require.NoError(t, err)
	assert.True(t, u.TokensOutUnknown)
	assert.True(t, u.CostUnknown)
	assert.Nil(t, u.Turn.TokensOut)
	assert.Nil(t, u.Turn.CostReported)
	assert.Nil(t, u.Turn.CacheWrite)
	assert.Equal(t, "", u.Turn.Provider, "no provider sent: derived from the model in the database")
}
