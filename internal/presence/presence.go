// Package presence tracks which agents currently have open SSE connections.
// It is a separate package so that both the handler and service layers can import
// it without creating a circular dependency.
package presence

import (
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
)

var connections sync.Map // key: uuid.UUID → *int32 (reference count)

// Register increments the SSE connection count for an agent.
func Register(agentID uuid.UUID) {
	for {
		v, _ := connections.LoadOrStore(agentID, new(int32))
		cnt := v.(*int32)
		old := atomic.LoadInt32(cnt)
		if old < 0 {
			// Slot was concurrently zeroed-and-deleted; retry.
			continue
		}
		if atomic.CompareAndSwapInt32(cnt, old, old+1) {
			return
		}
	}
}

// Unregister decrements the connection count and removes the entry when it
// reaches zero.
func Unregister(agentID uuid.UUID) {
	v, ok := connections.Load(agentID)
	if !ok {
		return
	}
	cnt := v.(*int32)
	if atomic.AddInt32(cnt, -1) <= 0 {
		connections.Delete(agentID)
	}
}

// IsConnected returns true if the agent currently has at least one open SSE
// connection.
func IsConnected(agentID uuid.UUID) bool {
	v, ok := connections.Load(agentID)
	if !ok {
		return false
	}
	return atomic.LoadInt32(v.(*int32)) > 0
}
