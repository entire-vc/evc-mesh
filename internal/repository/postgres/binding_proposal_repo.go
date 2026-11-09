package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// BindingProposalRepo stores domain.BindingProposal rows.
type BindingProposalRepo struct{ db *sqlx.DB }

// NewBindingProposalRepo creates the repo.
func NewBindingProposalRepo(db *sqlx.DB) *BindingProposalRepo { return &BindingProposalRepo{db: db} }

// Create inserts a pending proposal.
func (r *BindingProposalRepo) Create(ctx context.Context, p *domain.StoredBindingProposal) error {
	src, err := json.Marshal(p.Sources)
	if err != nil {
		return err
	}
	return r.db.QueryRowxContext(ctx, `INSERT INTO dependency_binding_proposals
		(id, edge_id, old_condition, new_condition, sources, scope, base_generation, proposed_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING created_at`,
		p.ID, p.EdgeID, p.Old, p.New, src, p.Scope, p.Generation, p.ProposedBy).Scan(&p.CreatedAt)
}

// Get returns the proposal or (nil, nil).
func (r *BindingProposalRepo) Get(ctx context.Context, id uuid.UUID) (*domain.StoredBindingProposal, error) {
	var (
		p   domain.StoredBindingProposal
		src []byte
		by  uuid.NullUUID
		at  sql.NullTime
		st  string
	)
	err := r.db.QueryRowxContext(ctx, `SELECT id, edge_id, old_condition, new_condition, sources, scope,
		base_generation, proposed_by, status, confirmed_by, confirmed_at, created_at
		FROM dependency_binding_proposals WHERE id=$1`, id).
		Scan(&p.ID, &p.EdgeID, &p.Old, &p.New, &src, &p.Scope, &p.Generation, &p.ProposedBy, &st, &by, &at, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(src, &p.Sources); err != nil {
		return nil, err
	}
	p.Applied = st == "applied"
	if by.Valid {
		p.ConfirmedBy = &by.UUID
	}
	if at.Valid {
		p.ConfirmedAt = &at.Time
	}
	return &p, nil
}

// MarkApplied flips pending -> applied exactly once. It reports false when the
// proposal was already applied (replay) or does not exist.
func (r *BindingProposalRepo) MarkApplied(ctx context.Context, id, by uuid.UUID, at time.Time) (bool, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE dependency_binding_proposals
		SET status='applied', confirmed_by=$2, confirmed_at=$3 WHERE id=$1 AND status='pending'`, id, by, at)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// edgeStateQuery lists every edge that feeds the next-action generation of a
// task (the ones it depends on and the ones depending on it) in a stable order.
const edgeStateQuery = `SELECT id, task_id, depends_on_task_id, dependency_type, condition,
	COALESCE(evidence_url, ''), COALESCE(condition_met_at::text, '')
	FROM task_dependencies WHERE task_id = $1 OR depends_on_task_id = $1 ORDER BY id`

// blockerStateQuery summarizes the tasks this task depends on: what the
// next-action generation reads about them besides the edge itself.
const blockerStateQuery = `SELECT t.id, COALESCE(t.status_id::text, ''), t.title,
	COALESCE((SELECT string_agg(l.id::text || ':' || COALESCE(l.status, '') || ':' || l.metadata::text, ',' ORDER BY l.id)
		FROM vcs_links l WHERE l.task_id = t.id), '')
	FROM tasks t WHERE t.id IN (SELECT depends_on_task_id FROM task_dependencies WHERE task_id = $1) ORDER BY t.id`

type edgeStateRow struct {
	ID, TaskID, DependsOn uuid.UUID
	Type, Condition       string
	Evidence, MetAt       string
}

func fingerprintEdges(ctx context.Context, q sqlx.QueryerContext, taskID uuid.UUID, suffix string) (string, error) {
	rows, err := q.QueryxContext(ctx, edgeStateQuery+suffix, taskID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var e edgeStateRow
		if serr := rows.Scan(&e.ID, &e.TaskID, &e.DependsOn, &e.Type, &e.Condition, &e.Evidence, &e.MetAt); serr != nil {
			return "", serr
		}
		lines = append(lines, fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s", e.ID, e.TaskID, e.DependsOn, e.Type, e.Condition, e.Evidence, e.MetAt))
	}
	if rerr := rows.Err(); rerr != nil {
		return "", rerr
	}
	rows.Close()
	// The generation also folds in the blockers' own state (status, name, VCS
	// links with head/verdict/deployment metadata), so the digest covers those
	// too. When the caller locks (suffix != ""), the blocker rows and their VCS
	// links are share-locked until commit.
	lock := ""
	if suffix != "" {
		lock = " FOR SHARE OF t"
	}
	brows, err := q.QueryxContext(ctx, blockerStateQuery+lock, taskID)
	if err != nil {
		return "", err
	}
	defer brows.Close()
	for brows.Next() {
		var id uuid.UUID
		var status, title, links string
		if err := brows.Scan(&id, &status, &title, &links); err != nil {
			return "", err
		}
		lines = append(lines, fmt.Sprintf("blocker|%s|%s|%s|%s", id, status, title, links))
	}
	if err := brows.Err(); err != nil {
		return "", err
	}
	if suffix != "" {
		lr, err := q.QueryxContext(ctx, `SELECT l.id FROM vcs_links l WHERE l.task_id IN
			(SELECT depends_on_task_id FROM task_dependencies WHERE task_id = $1) ORDER BY l.id FOR SHARE`, taskID)
		if err != nil {
			return "", err
		}
		for lr.Next() {
		}
		if err := lr.Err(); err != nil {
			_ = lr.Close()
			return "", err
		}
		_ = lr.Close()
	}
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:]), nil
}

// EdgeStateFingerprint returns a digest of the dependency edges around taskID.
// The confirm handler reads it BEFORE it reads the next-action generation, and
// ApplyConfirm refuses unless the edges still have that exact state, so a change
// that lands after the generation check cannot be overwritten.
func (r *BindingProposalRepo) EdgeStateFingerprint(ctx context.Context, taskID uuid.UUID) (string, error) {
	return fingerprintEdges(ctx, r.db, taskID, "")
}

// ApplyConfirm consumes a pending proposal and writes the edge condition in ONE
// transaction. It locks the task's edges, then the proposal row; it returns
// domain.ErrReplay for an already applied proposal and domain.ErrStaleGeneration
// when the edges changed since expectFingerprint or the edge no longer holds the
// proposal's old condition. Any error rolls everything back: the edge and the
// pending proposal stay as they were, so a retry can still apply it once.
// The confirming actor must still be the task's assignee and no human gate may
// be held when the transaction runs (ErrNotOwner / ErrHeldGate).
// The edge gets the new condition without evidence and is never marked met here.
func (r *BindingProposalRepo) ApplyConfirm(ctx context.Context, proposalID, edgeID, taskID, by uuid.UUID, at time.Time, expectFingerprint string) (*domain.TaskDependency, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// Re-authorize inside the transaction: the handler's task snapshot may be
	// stale, so a reassignment or a human gate raised since then wins. The task
	// row stays locked until commit.
	var assignee uuid.NullUUID
	var gate bool
	if err = tx.QueryRowxContext(ctx, `SELECT assignee_id, human_gate FROM tasks WHERE id = $1 FOR UPDATE`, taskID).Scan(&assignee, &gate); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apierror.NotFound("Task")
		}
		return nil, err
	}
	if !assignee.Valid || assignee.UUID != by {
		return nil, domain.ErrNotOwner
	}
	if gate {
		return nil, domain.ErrHeldGate
	}
	current, err := fingerprintEdges(ctx, tx, taskID, " FOR UPDATE")
	if err != nil {
		return nil, err
	}
	var oldCond, newCond, status string
	err = tx.QueryRowxContext(ctx, `SELECT old_condition, new_condition, status FROM dependency_binding_proposals
		WHERE id = $1 AND edge_id = $2 FOR UPDATE`, proposalID, edgeID).Scan(&oldCond, &newCond, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apierror.NotFound("Binding proposal")
	}
	if err != nil {
		return nil, err
	}
	if status != "pending" {
		return nil, domain.ErrReplay
	}
	if expectFingerprint == "" || current != expectFingerprint {
		return nil, domain.ErrStaleGeneration
	}
	var edgeCond string
	if err := tx.GetContext(ctx, &edgeCond, `SELECT condition FROM task_dependencies WHERE id = $1`, edgeID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apierror.NotFound("Dependency")
		}
		return nil, err
	}
	if edgeCond == "" {
		edgeCond = string(domain.DependencyConditionTaskDone)
	}
	if edgeCond != oldCond {
		return nil, domain.ErrStaleGeneration
	}
	if _, err := tx.ExecContext(ctx, `UPDATE dependency_binding_proposals
		SET status='applied', confirmed_by=$2, confirmed_at=$3 WHERE id=$1`, proposalID, by, at); err != nil {
		return nil, err
	}
	q := `WITH d AS (UPDATE task_dependencies SET condition = $2, evidence_url = NULL, condition_met_at = NULL, evidence_status = 'none', evidence_reason = NULL, evidence_head_sha = NULL, evidence_environment = NULL, evidence_link_id = NULL, evidence_actor_id = NULL, generation = generation + 1 WHERE id = $1 RETURNING *)
		SELECT ` + taskDependencySelectCols + ` FROM d ` + taskDependencyJoins
	var dep domain.TaskDependency
	if err := tx.GetContext(ctx, &dep, q, edgeID, newCond); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &dep, nil
}
