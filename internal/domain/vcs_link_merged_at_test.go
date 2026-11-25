package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ParseVCSProviderTime: any RFC 3339 offset in, UTC out; empty,
// unparseable or zero input stays unknown (nil), never 0001-01-01 or now.
func TestParseVCSProviderTime(t *testing.T) {
	want := time.Date(2026, 11, 20, 15, 3, 4, 512000000, time.UTC)
	for _, raw := range []string{"2026-11-20T15:03:04.512Z", "2026-11-20T18:03:04.512+03:00", " 2026-11-20T10:03:04.512-05:00 \n"} {
		got := ParseVCSProviderTime(raw)
		require.NotNil(t, got, raw)
		assert.True(t, got.Equal(want), raw)
		assert.Equal(t, time.UTC, got.Location(), raw)
	}
	for _, raw := range []string{"", "   ", "yesterday", "2026-11-20", "0001-01-01T00:00:00Z"} {
		assert.Nil(t, ParseVCSProviderTime(raw), "%q is unknown", raw)
	}
}
