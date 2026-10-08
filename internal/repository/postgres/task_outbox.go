package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

type TaskOutboxRepo struct{ db *sqlx.DB }

func NewTaskOutboxRepo(db *sqlx.DB) *TaskOutboxRepo { return &TaskOutboxRepo{db: db} }

// DeliverNext holds a row lock through the bounded publish and ack. Concurrent
// processes skip locked candidates; process death releases the lock, leaving
// the immutable ID available for retry. A sink must deduplicate on that ID.
func (r *TaskOutboxRepo) DeliverNext(ctx context.Context, publish func(context.Context, *domain.EventBusMessage) error) (bool, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var event domain.TaskOutboxEvent
	err = tx.GetContext(ctx, &event, `SELECT id,message,attempts,created_at FROM task_event_outbox
  WHERE delivered_at IS NULL AND available_at<=clock_timestamp()
  ORDER BY available_at,created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var msg domain.EventBusMessage
	if err = json.Unmarshal(event.Message, &msg); err != nil {
		return true, fmt.Errorf("decode committed outbox: %w", err)
	}
	if msg.ID != event.ID {
		return true, fmt.Errorf("outbox message ID mismatch")
	}
	pubErr := publish(ctx, &msg)
	if pubErr != nil {
		_, err = tx.ExecContext(ctx, `UPDATE task_event_outbox SET attempts=attempts+1,
   available_at=clock_timestamp()+make_interval(secs=>LEAST(300,power(2,LEAST(attempts+1,8)))::double precision) WHERE id=$1`, event.ID)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE task_event_outbox SET attempts=attempts+1,delivered_at=clock_timestamp() WHERE id=$1`, event.ID)
	}
	if err != nil {
		return true, err
	}
	if err := tx.Commit(); err != nil {
		return true, err
	}
	return true, pubErr
}
