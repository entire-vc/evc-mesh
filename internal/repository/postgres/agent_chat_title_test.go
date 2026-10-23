package postgres

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgentChatAutoTitle(t *testing.T) {
	require.Equal(t, "hello world", ChatAutoTitle("  hello \n\n world  "), "whitespace collapses to one line")
	require.Equal(t, "", ChatAutoTitle("   \n "))

	long := strings.Repeat("ё", 100)
	got := ChatAutoTitle(long)
	require.Equal(t, 60, len([]rune(got)), "cut at 60 characters, not bytes")
	require.True(t, strings.HasPrefix(long, got))

	exact := strings.Repeat("a", 60)
	require.Equal(t, exact, ChatAutoTitle(exact))
}

func TestAgentChatCursorRoundTrip(t *testing.T) {
	_, _, err := decodeChatCursor("###")
	require.Error(t, err)
	_, _, err = decodeChatCursor("")
	require.Error(t, err)
}
