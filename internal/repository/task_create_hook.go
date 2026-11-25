package repository

import (
	"context"
	"database/sql"

	"github.com/google/uuid"
)

// TaskCreateTxHook runs inside the transaction of TaskRepository.Create, after
// the task row and its task.created audit entry are written and before the
// commit. An error from the hook rolls the whole creation back, and Create
// returns that error unchanged, before any post-commit side effect of the
// service (events, webhooks, notifications) has run.
//
// It is how a caller makes its own row commit atomically with the task: the
// gate-intake receipt, which must exist exactly when the card does.
type TaskCreateTxHook func(ctx context.Context, tx *sql.Tx) error

type taskCreateTxHookKey struct{}

type taskCreateTxHook struct {
	taskID uuid.UUID
	hook   TaskCreateTxHook
}

// WithTaskCreateTxHook returns ctx carrying hook for the creation of the task
// with id taskID only: any other task created with this ctx (a side effect of
// the first one) does not run it.
func WithTaskCreateTxHook(ctx context.Context, taskID uuid.UUID, hook TaskCreateTxHook) context.Context {
	return context.WithValue(ctx, taskCreateTxHookKey{}, taskCreateTxHook{taskID: taskID, hook: hook})
}

// TaskCreateTxHookFor returns the hook ctx carries for taskID, or nil.
func TaskCreateTxHookFor(ctx context.Context, taskID uuid.UUID) TaskCreateTxHook {
	h, ok := ctx.Value(taskCreateTxHookKey{}).(taskCreateTxHook)
	if !ok || h.taskID != taskID {
		return nil
	}
	return h.hook
}
