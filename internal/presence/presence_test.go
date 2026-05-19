package presence

import (
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestRegisterUnregister(t *testing.T) {
	id := uuid.New()

	assert.False(t, IsConnected(id))

	Register(id)
	assert.True(t, IsConnected(id))

	Unregister(id)
	assert.False(t, IsConnected(id))
}

func TestRefcountSemantics(t *testing.T) {
	id := uuid.New()

	// Two parallel connections.
	Register(id)
	Register(id)
	assert.True(t, IsConnected(id))

	// First disconnect — still connected.
	Unregister(id)
	assert.True(t, IsConnected(id))

	// Second disconnect — now offline.
	Unregister(id)
	assert.False(t, IsConnected(id))
}

func TestUnregisterUnknown(t *testing.T) {
	id := uuid.New()
	// Should not panic on unknown ID.
	assert.NotPanics(t, func() { Unregister(id) })
}

func TestParallelConnections(t *testing.T) {
	id := uuid.New()
	const n = 50

	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			Register(id)
		}()
	}
	wg.Wait()

	assert.True(t, IsConnected(id))

	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			Unregister(id)
		}()
	}
	wg.Wait()

	assert.False(t, IsConnected(id))
}
