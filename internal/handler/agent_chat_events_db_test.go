package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

type recordedWS struct {
	channel string
	event   string
}

type recordingWS struct {
	mu   sync.Mutex
	sent []recordedWS
}

func (r *recordingWS) Publish(_ context.Context, channel string, event any) error {
	raw, _ := json.Marshal(event)
	var head struct {
		Event string `json:"event"`
	}
	_ = json.Unmarshal(raw, &head)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, recordedWS{channel: channel, event: head.Event})
	return nil
}

func (r *recordingWS) take() []recordedWS {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.sent
	r.sent = nil
	return out
}

type countingSeq struct{ n atomic.Int64 }

func (s *countingSeq) NextSeq(context.Context, uuid.UUID) (int64, error) { return s.n.Add(1), nil }

// An API action in thread A publishes to ws:user:<A> and to no other channel.
func TestAgentChatEvents_OnlyOwnerChannel(t *testing.T) {
	env := newAgentChatEnv(t)
	ws := &recordingWS{}
	pub := service.NewChatEventPublisher(ws, &countingSeq{})
	env.h.WithEvents(pub)
	delHandler := NewChatThreadDeleteHandler(postgres.NewChatThreadDeleteRepo(env.db, nil)).WithEvents(pub)

	owner := "ws:user:" + env.userA.String()
	strangers := []string{"ws:user:" + env.userB.String(), "ws:user:" + env.owner.String()}

	status, created := env.call(t, env.h.CreateThread, http.MethodPost, fmt.Sprintf(`{"agent_id":%q}`, env.agent), env.userA, env.proj())
	require.Equal(t, http.StatusCreated, status)
	thread := created["id"].(string)
	status, _ = env.call(t, env.h.RenameThread, http.MethodPatch, `{"title":"plan"}`, env.userA, env.thr(thread))
	require.Equal(t, http.StatusOK, status)
	status, _ = env.send(t, env.userA, thread, "key-events-1", "hello")
	require.Equal(t, http.StatusAccepted, status)
	status, _ = env.call(t, env.h.StopTurn, http.MethodPost, "", env.userA, env.thr(thread))
	require.Equal(t, http.StatusOK, status)
	status, _ = env.call(t, delHandler.Delete, http.MethodDelete, "", env.userA, env.thr(thread))
	require.Equal(t, http.StatusNoContent, status)

	var got []string
	for _, e := range ws.take() {
		require.Equal(t, owner, e.channel, "event %s left the owner's channel", e.event)
		got = append(got, e.event)
	}
	require.Equal(t, []string{
		service.ChatEventThreadChanged, service.ChatEventThreadChanged,
		service.ChatEventMessageCreated, service.ChatEventTurnState,
		service.ChatEventTurnState, service.ChatEventThreadChanged,
	}, got)

	// A stranger's attempt on the same thread publishes nothing at all.
	other := env.createThread(t, env.userA)
	ws.take()
	status, _ = env.call(t, env.h.RenameThread, http.MethodPatch, `{"title":"x"}`, env.userB, env.thr(other))
	require.Equal(t, http.StatusNotFound, status)
	status, _ = env.send(t, env.userB, other, "key-events-2", "hi")
	require.Equal(t, http.StatusNotFound, status)
	status, _ = env.call(t, delHandler.Delete, http.MethodDelete, "", env.userB, env.thr(other))
	require.Equal(t, http.StatusNotFound, status)
	require.Empty(t, ws.take(), "a refused request must publish nothing; channels of strangers: %v", strangers)
}
