package service

import (
	"context"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// ChatRateLimiter decides whether one more event batch of a turn fits into the
// current one-second window.
type ChatRateLimiter interface {
	Allow(ctx context.Context, turn uuid.UUID, now time.Time) (bool, error)
}

type redisChatRateLimiter struct{ rdb *redis.Client }

// NewRedisChatRateLimiter counts batches in Redis so the 20/s per-turn limit is
// the same whichever API replica receives the batch.
func NewRedisChatRateLimiter(rdb *redis.Client) ChatRateLimiter {
	return &redisChatRateLimiter{rdb: rdb}
}

func (r *redisChatRateLimiter) Allow(ctx context.Context, turn uuid.UUID, now time.Time) (bool, error) {
	key := "chat:rate:" + turn.String() + ":" + strconv.FormatInt(now.Unix(), 10)
	pipe := r.rdb.Pipeline()
	n := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, 3*time.Second)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, err
	}
	return n.Val() <= ChatEventsRatePerSecond, nil
}
