package service

import (
	"context"
	"encoding/json"

	"github.com/redis/go-redis/v9"
)

type redisWSPublisher struct {
	rdb *redis.Client
}

// NewRedisWSPublisher returns a WSPublisher that serialises events as JSON
// and publishes them to Redis pub/sub channels consumed by the WS hub.
func NewRedisWSPublisher(rdb *redis.Client) WSPublisher {
	return &redisWSPublisher{rdb: rdb}
}

func (p *redisWSPublisher) Publish(ctx context.Context, channel string, event any) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return p.rdb.Publish(ctx, channel, string(payload)).Err()
}
