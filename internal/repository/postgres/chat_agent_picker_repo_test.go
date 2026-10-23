package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestChatPresence(t *testing.T) {
	cases := []struct {
		name           string
		live           bool
		running, limit int
		want           string
	}{
		{"lapsed lease", false, 0, 3, ChatPresenceOffline},
		{"lapsed lease at limit stays offline", false, 3, 3, ChatPresenceOffline},
		{"live idle", true, 0, 3, ChatPresenceOnline},
		{"live below limit", true, 2, 3, ChatPresenceOnline},
		{"live at limit", true, 3, 3, ChatPresenceBusy},
		{"live over limit", true, 4, 3, ChatPresenceBusy},
		{"no limit never busy", true, 50, 0, ChatPresenceOnline},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, chatPresence(tc.live, tc.running, tc.limit))
		})
	}
}

func TestChatColorToken_StableAndInRange(t *testing.T) {
	id := uuid.New()
	require.Equal(t, chatColorToken(id), chatColorToken(id))
	for i := 0; i < 200; i++ {
		require.Regexp(t, `^avatar-[1-8]$`, chatColorToken(uuid.New()))
	}
}

func TestToChatAgents(t *testing.T) {
	id := uuid.New()
	got := toChatAgents([]chatAgentRow{
		{AgentID: id, Name: "a", Limit: 2, Live: true, Running: 2},
		{AgentID: uuid.New(), Name: "b", Limit: 2, Live: false, Running: 1},
	})
	require.Len(t, got, 2)
	require.Equal(t, ChatPresenceBusy, got[0].Presence)
	require.Equal(t, 2, got[0].ActiveSessions)
	require.Equal(t, chatColorToken(id), got[0].ColorToken)
	require.Equal(t, ChatPresenceOffline, got[1].Presence)
	require.Empty(t, toChatAgents(nil), "no rows is an empty list, not nil")
	require.NotNil(t, toChatAgents(nil))
}

func TestChatAgentPickerRepo_RefusesNonUsers(t *testing.T) {
	repo := NewChatAgentPickerRepo(nil) // refused before any query runs
	for name, actor := range map[string]UserRuntimeActor{
		"agent key": {UserID: uuid.New(), AgentID: uuid.New()},
		"connector": {UserID: uuid.New(), Connector: true},
		"anonymous": {},
	} {
		_, err := repo.List(context.Background(), actor, uuid.New())
		require.Error(t, err, name)
	}
}
