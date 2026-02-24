package eventbus

import "time"

// EventBusConfig holds configuration for the NATS JetStream event bus.
type EventBusConfig struct {
	NATSUrl           string
	RedisAddr         string
	RedisPassword     string
	RedisDB           int
	NATSReplicas      int           // default 1
	StreamMaxAge      time.Duration // default 30 days
	StreamMaxBytes    int64         // default 10 GB
	MaxMsgSize        int32         // default 256 KB
	CleanupInterval   time.Duration // default 1h
	CleanupBatchSize  int           // default 1000
	PGWriterBatchSize int           // default 100
	PGWriterInterval  time.Duration // default 5s
}

// DefaultConfig returns an EventBusConfig with sensible defaults.
func DefaultConfig() EventBusConfig {
	return EventBusConfig{
		NATSUrl:           "nats://localhost:4223",
		RedisAddr:         "localhost:6383",
		RedisPassword:     "",
		RedisDB:           0,
		NATSReplicas:      1,
		StreamMaxAge:      30 * 24 * time.Hour,
		StreamMaxBytes:    10 * 1024 * 1024 * 1024, // 10 GB
		MaxMsgSize:        256 * 1024,               // 256 KB
		CleanupInterval:   1 * time.Hour,
		CleanupBatchSize:  1000,
		PGWriterBatchSize: 100,
		PGWriterInterval:  5 * time.Second,
	}
}

// applyDefaults fills in zero-value fields with defaults.
func (c *EventBusConfig) applyDefaults() {
	d := DefaultConfig()
	if c.NATSReplicas <= 0 {
		c.NATSReplicas = d.NATSReplicas
	}
	if c.StreamMaxAge <= 0 {
		c.StreamMaxAge = d.StreamMaxAge
	}
	if c.StreamMaxBytes <= 0 {
		c.StreamMaxBytes = d.StreamMaxBytes
	}
	if c.MaxMsgSize <= 0 {
		c.MaxMsgSize = d.MaxMsgSize
	}
	if c.CleanupInterval <= 0 {
		c.CleanupInterval = d.CleanupInterval
	}
	if c.CleanupBatchSize <= 0 {
		c.CleanupBatchSize = d.CleanupBatchSize
	}
	if c.PGWriterBatchSize <= 0 {
		c.PGWriterBatchSize = d.PGWriterBatchSize
	}
	if c.PGWriterInterval <= 0 {
		c.PGWriterInterval = d.PGWriterInterval
	}
}
