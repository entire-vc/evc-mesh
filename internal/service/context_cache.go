package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const contextCacheTTL = 60 * time.Second

// ContextCacheInvalidator is a narrow interface used by task_service and
// comment_service to invalidate the task-context cache without importing the
// full ContextCacheService (avoids circular imports between packages).
type ContextCacheInvalidator interface {
	Invalidate(ctx context.Context, taskID uuid.UUID)
}

// ContextCacheService caches serialised JSON responses for the
// GET /tasks/:task_id/context endpoint using Redis.
//
// Redis key pattern: ctx:task:{task_id}
// TTL: 60 seconds.
//
// All methods are safe to call when the receiver is nil — they silently
// become no-ops, so the handler works without a cache.
type ContextCacheService struct {
	client *redis.Client
}

// NewContextCacheService returns a new ContextCacheService backed by the
// given Redis client.
func NewContextCacheService(client *redis.Client) *ContextCacheService {
	return &ContextCacheService{client: client}
}

// key returns the Redis key for the given task ID.
func (c *ContextCacheService) key(taskID uuid.UUID) string {
	return fmt.Sprintf("ctx:task:%s", taskID.String())
}

// Get retrieves cached JSON bytes for the task.
// Returns (data, true) on hit; (nil, false) on miss or any error.
func (c *ContextCacheService) Get(ctx context.Context, taskID uuid.UUID) ([]byte, bool) {
	if c == nil || c.client == nil {
		return nil, false
	}

	data, err := c.client.Get(ctx, c.key(taskID)).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			log.Printf("[context_cache] WARNING: Get task %s: %v", taskID, err)
		}
		return nil, false
	}
	return data, true
}

// Set stores the serialised JSON for the task with a 60-second TTL.
// Errors are logged but never propagated to the caller.
func (c *ContextCacheService) Set(ctx context.Context, taskID uuid.UUID, data []byte) {
	if c == nil || c.client == nil {
		return
	}

	if err := c.client.Set(ctx, c.key(taskID), data, contextCacheTTL).Err(); err != nil {
		log.Printf("[context_cache] WARNING: Set task %s: %v", taskID, err)
	}
}

// Invalidate deletes the cached entry for the task.
// Errors are logged but never propagated.
func (c *ContextCacheService) Invalidate(ctx context.Context, taskID uuid.UUID) {
	if c == nil || c.client == nil {
		return
	}

	if err := c.client.Del(ctx, c.key(taskID)).Err(); err != nil && !errors.Is(err, redis.Nil) {
		log.Printf("[context_cache] WARNING: Invalidate task %s: %v", taskID, err)
	}
}
