package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// A streamed agent reply reaches the owner's socket as role "assistant", the
// same value the history endpoint returns.
func TestChatMessagePayload_AgentIsAssistant(t *testing.T) {
	require.Equal(t, "assistant", chatMessagePayload(&postgres.ChatMessageRow{Role: "agent"}).Role)
}
