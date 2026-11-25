package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
)

func TestTaskCreateTxHookFor(t *testing.T) {
	taskID := uuid.New()
	called := false
	ctx := WithTaskCreateTxHook(context.Background(), taskID, func(context.Context, *sql.Tx) error {
		called = true
		return nil
	})

	if TaskCreateTxHookFor(context.Background(), taskID) != nil {
		t.Fatal("a context without a hook returns none")
	}
	if TaskCreateTxHookFor(ctx, uuid.New()) != nil {
		t.Fatal("the hook belongs to its own task only: a nested create of another task must not run it")
	}
	h := TaskCreateTxHookFor(ctx, taskID)
	if h == nil {
		t.Fatal("the hook of the task is returned")
	}
	if err := h(ctx, nil); err != nil || !called {
		t.Fatalf("the returned hook is the registered one: err=%v called=%v", err, called)
	}
}
