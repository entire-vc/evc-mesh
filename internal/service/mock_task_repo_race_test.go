package service

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// GetByID resolves RuntimeState in place on the shared *Task, so concurrent
// readers must be serialized by the mock. Under -race this fails if the lookup
// goes back to an RLock.
func TestMockTaskRepository_GetByID_ConcurrentReadsDoNotRace(t *testing.T) {
	repo := NewMockTaskRepository()
	id := uuid.New()
	repo.items[id] = &domain.Task{ID: id}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if got, err := repo.GetByID(context.Background(), id); err != nil || got == nil {
					t.Errorf("GetByID: got=%v err=%v", got, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
