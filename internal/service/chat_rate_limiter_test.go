package service

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two limiters on one Redis stand for two API replicas: together they get one
// 20-batch window per turn, not one each.
func TestRedisChatRateLimiter_WindowIsSharedAcrossReplicas(t *testing.T) {
	mr := miniredis.RunT(t)
	a := NewRedisChatRateLimiter(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	b := NewRedisChatRateLimiter(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	ctx, turn, now := context.Background(), uuid.New(), time.Unix(1_800_000_000, 0)

	allowed := 0
	for i := 0; i < 30; i++ {
		l := a
		if i%2 == 1 {
			l = b
		}
		ok, err := l.Allow(ctx, turn, now)
		require.NoError(t, err)
		if ok {
			allowed++
		}
	}
	assert.Equal(t, ChatEventsRatePerSecond, allowed)

	ok, err := a.Allow(ctx, uuid.New(), now)
	require.NoError(t, err)
	assert.True(t, ok, "another turn has its own window")
	ok, err = b.Allow(ctx, turn, now.Add(time.Second))
	require.NoError(t, err)
	assert.True(t, ok, "the next second opens a new window")
}

type failingLimiter struct{}

func (failingLimiter) Allow(context.Context, uuid.UUID, time.Time) (bool, error) {
	return false, assert.AnError
}

func TestChatTurnEventsService_UnreachableLimiterFallsBackToLocalWindow(t *testing.T) {
	s := NewChatTurnEventsService(nil, nil).WithRateLimiter(failingLimiter{})
	ts, turn := &chatTurnState{}, uuid.New()
	allowed := 0
	for i := 0; i < 30; i++ {
		if s.admit(context.Background(), ts, turn) {
			allowed++
		}
	}
	assert.Equal(t, ChatEventsRatePerSecond, allowed)
}
