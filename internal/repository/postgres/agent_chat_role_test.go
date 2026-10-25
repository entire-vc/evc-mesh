package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The web client and the agent-chat e2e harness wait for role "assistant";
// the table stores 'agent'. Red control: returning row.Role unmapped in
// toMessage makes this fail with "agent".
func TestChatMessageRole_AgentIsAssistantOnTheWire(t *testing.T) {
	for stored, want := range map[string]string{"agent": "assistant", "user": "user", "system": "system"} {
		require.Equal(t, want, chatMessageRow{Role: stored}.toMessage().Role, stored)
	}
}
