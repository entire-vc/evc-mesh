package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
)

// SchedulerStateRepo implements repository.SchedulerStateRepository over the small
// scheduler_job_runs table — see its migration for why this exists as a table of its
// own rather than piggybacking on some other domain's rows.
type SchedulerStateRepo struct {
	db *sqlx.DB
}

func NewSchedulerStateRepo(db *sqlx.DB) *SchedulerStateRepo {
	return &SchedulerStateRepo{db: db}
}

// GetLastRun returns jobName's last recorded run time, or nil if it has never run.
func (r *SchedulerStateRepo) GetLastRun(ctx context.Context, jobName string) (*time.Time, error) {
	var lastRunAt time.Time
	err := r.db.GetContext(ctx, &lastRunAt,
		`SELECT last_run_at FROM scheduler_job_runs WHERE job_name = $1`,
		jobName,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &lastRunAt, nil
}

// SetLastRun upserts jobName's last-run watermark to at.
func (r *SchedulerStateRepo) SetLastRun(ctx context.Context, jobName string, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO scheduler_job_runs (job_name, last_run_at, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (job_name) DO UPDATE
		SET last_run_at = EXCLUDED.last_run_at, updated_at = NOW()`,
		jobName, at,
	)
	return err
}
