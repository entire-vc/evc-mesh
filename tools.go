//go:build tools

// This file ensures planned dependencies are tracked in go.mod even before
// they are imported in production code. Remove individual imports as they
// become used in real packages.
package tools

import (
	_ "github.com/jmoiron/sqlx"
	_ "github.com/nats-io/nats.go"
	_ "github.com/pressly/goose/v3"
	_ "github.com/redis/go-redis/v9"
	_ "github.com/stretchr/testify/assert"
)
