package handler

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// A message sent while the runner is parked in claim must wake it at once, not
// on the 5 s safety-net tick: the hint is the warm-turn latency budget.
func TestChatRunnerClaim_SentMessageWakesParkedClaim(t *testing.T) {
	env := newAgentChatEnv(t)
	_, rdb := newSSEMiniredis(t)
	env.h.WithRunnerHints(rdb)
	thread := env.createThread(t, env.userA)

	claim := NewChatClaimHandler(postgres.NewChatRunnerRepo(env.db), rdb, nil)
	c, rec := runnerCtx(http.MethodGet, "/runner/chat/claim?agents="+env.agent.String()+"&timeout=30", "", env.agent)
	c.Set(mw.ContextKeyWorkspaceID, env.ws)

	done := make(chan time.Duration, 1)
	start := time.Now()
	go func() {
		_ = claim.Claim(c)
		done <- time.Since(start)
	}()
	time.Sleep(300 * time.Millisecond) // parked: subscribed and found nothing

	status, out := env.send(t, env.userA, thread, "key-hint-0001", "Ответь одним словом: ок")
	require.Equal(t, http.StatusAccepted, status, "%v", out)
	sent := time.Since(start)

	select {
	case took := <-done:
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Less(t, took-sent, time.Second, "the claim returns on the hint, not on the %s tick", chatClaimPollRedis)
		assert.Contains(t, rec.Body.String(), "Ответь одним словом")
	case <-time.After(chatClaimPollRedis + 2*time.Second):
		t.Fatal("parked claim did not return")
	}
}

// A turn already queued when the claim starts is handed out by the first read.
func TestChatRunnerClaim_QueuedTurnReturnsImmediately(t *testing.T) {
	env := newAgentChatEnv(t)
	_, rdb := newSSEMiniredis(t)
	thread := env.createThread(t, env.userA)
	status, out := env.send(t, env.userA, thread, "key-hint-0002", "ok?")
	require.Equal(t, http.StatusAccepted, status, "%v", out)

	claim := NewChatClaimHandler(postgres.NewChatRunnerRepo(env.db), rdb, nil)
	c, rec := runnerCtx(http.MethodGet, "/runner/chat/claim?agents="+env.agent.String()+"&timeout=30", "", env.agent)
	c.Set(mw.ContextKeyWorkspaceID, env.ws)

	start := time.Now()
	require.NoError(t, claim.Claim(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Less(t, time.Since(start), time.Second)
}
