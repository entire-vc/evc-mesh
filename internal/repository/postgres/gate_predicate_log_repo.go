package postgres

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	pkgmetrics "github.com/entire-vc/evc-mesh/pkg/metrics"
)

// GatePredicateLogRepo records every predicate evaluation behind gate arming
// (task #5d3dc714). Append-only by construction: there is no Update and no Delete, so a
// refusal cannot be edited into an allow after the fact.
type GatePredicateLogRepo struct {
	db *sqlx.DB
}

func NewGatePredicateLogRepo(db *sqlx.DB) *GatePredicateLogRepo {
	return &GatePredicateLogRepo{db: db}
}

// Record writes one evaluation. Every column of the predicate is stored, on allowed
// evaluations too — the acceptance question is a ratio, and a table holding only
// refusals has no denominator.
func (r *GatePredicateLogRepo) Record(ctx context.Context, e *domain.GatePredicateLogEntry) error {
	const q = `
		INSERT INTO gate_predicate_log (
			id, task_id, actor_id, actor_type, outcome,
			credential_exists, reversible, blocked_by_other_task, customer_visible_now,
			credential_reason, reversible_reason, blocked_reason, customer_reason,
			source, created_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9,
			$10, $11, $12, $13,
			$14, $15
		)`
	dbStart := time.Now()
	_, err := r.db.ExecContext(ctx, q,
		e.ID, e.TaskID, e.ActorID, string(e.ActorType), string(e.Outcome),
		e.CredentialExists, e.Reversible, e.BlockedByOtherTask, e.CustomerVisibleNow,
		e.CredentialReason, e.ReversibleReason, e.BlockedReason, e.CustomerReason,
		string(e.Source), e.CreatedAt,
	)
	pkgmetrics.RecordDBQuery("gate_predicate_log.record", time.Since(dbStart))
	return err
}

// CountByOutcome returns how many evaluations of each outcome happened since `since`.
// This is the query the card's acceptance asks for — the ratio of refusals to successful
// arms — and it lives here rather than in a dashboard so it can be re-run verbatim.
func (r *GatePredicateLogRepo) CountByOutcome(ctx context.Context, since time.Time) (map[domain.GatePredicateOutcome]int, error) {
	const q = `
		SELECT outcome, count(*) FROM gate_predicate_log
		 WHERE created_at >= $1 GROUP BY outcome`
	rows, err := r.db.QueryContext(ctx, q, since)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make(map[domain.GatePredicateOutcome]int)
	for rows.Next() {
		var outcome string
		var n int
		if err := rows.Scan(&outcome, &n); err != nil {
			return nil, err
		}
		out[domain.GatePredicateOutcome(outcome)] = n
	}
	return out, rows.Err()
}
