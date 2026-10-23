package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestAccessLogLines(t *testing.T) {
	fast := accessLogLines("GET", "/api/v1/tasks", 200, 42*time.Millisecond)
	assert.Equal(t, []string{"GET /api/v1/tasks -> 200 42ms"}, fast)

	slow := accessLogLines("POST", "/api/v1/tasks/x/checkout", 409, 2500*time.Millisecond)
	assert.Len(t, slow, 2)
	assert.Equal(t, "POST /api/v1/tasks/x/checkout -> 409 2500ms", slow[0])
	assert.Contains(t, slow[1], "WARN slow request")
	assert.Contains(t, slow[1], "2500ms")

	assert.Len(t, accessLogLines("GET", "/", 200, slowRequestThreshold), 1, "exactly at the threshold is not slow")
}
