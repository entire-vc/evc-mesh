package service

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// ChatRunnerChannelPrefix is the Mesh-internal Redis channel that wakes parked
// runner long-polls (runner protocol §4.1). The runner never talks to Redis.
const ChatRunnerChannelPrefix = "runner-chat:"

// Hint kinds on the channel.
const (
	ChatHintTurn    = "turn"
	ChatHintCancel  = "cancel"
	ChatHintCommand = "command"
)

func ChatRunnerChannel(agentID uuid.UUID) string { return ChatRunnerChannelPrefix + agentID.String() }

// PublishChatRunnerHint tells parked claims of the agent to look at the
// database. The hint is lossy by design: the claim always re-reads the tables,
// so a nil client or a failed publish costs latency, never correctness.
func PublishChatRunnerHint(ctx context.Context, rdb *redis.Client, agentID uuid.UUID, kind string, id uuid.UUID) {
	if rdb == nil {
		return
	}
	payload, err := json.Marshal(map[string]string{"kind": kind, "id": id.String()})
	if err != nil {
		return
	}
	_ = rdb.Publish(ctx, ChatRunnerChannel(agentID), payload).Err()
}
