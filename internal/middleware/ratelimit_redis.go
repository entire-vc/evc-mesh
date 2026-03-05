package middleware

import (
	"context"
	"fmt"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
)

// redisRateLimiter implements a sliding window counter using Redis.
//
// Key pattern : ratelimit:{key}:{window}
//
//	where {window} = Unix timestamp divided by 60 (current minute bucket).
//
// Algorithm   : INCR the counter for the current window bucket, set a 2-minute
//
//	TTL on first write so old buckets are auto-evicted, then compare
//	the returned count against the configured RPM limit.
//
// The INCR+EXPIRE pair is not atomic by default, but race conditions on the
// very first request to a new bucket are benign: at worst two goroutines both
// see count=1 and both set the TTL, which is idempotent. A MULTI/EXEC
// transaction is used to guarantee the TTL is set on the same round-trip
// as the INCR.
type redisRateLimiter struct {
	client *redis.Client
	rpm    int
}

func newRedisRateLimiter(client *redis.Client, rpm int) *redisRateLimiter {
	return &redisRateLimiter{client: client, rpm: rpm}
}

// windowKey returns the Redis key for the given rate-limit identity and the
// current one-minute window bucket.
func (r *redisRateLimiter) windowKey(key string) string {
	window := time.Now().Unix() / 60
	return fmt.Sprintf("ratelimit:%s:%d", key, window)
}

// allow returns true if the request is within the rate limit, and false if it
// has been exceeded. It increments the counter for the current window atomically.
func (r *redisRateLimiter) allow(ctx context.Context, key string) (bool, error) {
	rkey := r.windowKey(key)

	// Use a MULTI/EXEC pipeline so that INCR and EXPIRE are sent in the same
	// round-trip and the TTL is always applied even when the key is brand new.
	var incrCmd *redis.IntCmd
	_, err := r.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		incrCmd = pipe.Incr(ctx, rkey)
		// 2-minute TTL: the previous bucket lingers for an extra minute so that
		// any in-flight requests that fall on minute boundaries are still counted.
		pipe.Expire(ctx, rkey, 2*time.Minute)
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("redis rate limit pipeline: %w", err)
	}

	count := incrCmd.Val()
	return count <= int64(r.rpm), nil
}

// rateLimitRedis returns an Echo middleware that enforces the configured
// per-key rate limit using a Redis sliding window counter. When Redis is
// unavailable the request is allowed through (fail-open) and the error is
// logged to the Echo logger.
func rateLimitRedis(cfg RateLimitConfig) echo.MiddlewareFunc {
	rl := newRedisRateLimiter(cfg.RedisClient, cfg.RPM)

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			key := cfg.KeyFunc(c)
			ctx := c.Request().Context()

			ok, err := rl.allow(ctx, key)
			if err != nil {
				// Fail-open: log the error and let the request through.
				// This avoids a Redis outage taking down the API.
				c.Logger().Errorf("redis rate limiter error (fail-open): %v", err)
				return next(c)
			}

			if !ok {
				// Retry-After: the current window resets at the next minute boundary.
				secondsUntilReset := 60 - (time.Now().Unix() % 60)
				return tooManyRequestsJSON(c, int(secondsUntilReset))
			}

			return next(c)
		}
	}
}
