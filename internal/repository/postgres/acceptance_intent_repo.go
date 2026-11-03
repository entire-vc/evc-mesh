package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

const intentCols = `id, task_id, project_id, domain, task_class, operation, from_status_id, from_version, to_status_id, evidence_kind,
 evidence, evidence_hash, deploy_ref, live_ref, result_author_id, acceptor_actor_id, acceptor_actor_type, acceptor_role, policy_version, reason, created_at`

// CreateAcceptanceIntent records the authenticated acceptance of one result.
// The acceptor is the credential actor; nothing in the request names it.
func (r *TransitionReceiptRepo) CreateAcceptanceIntent(ctx context.Context, in domain.AcceptanceIntentInput) (*domain.AcceptanceIntent, error) {
	req := in.Request
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	task, err := lockParkedTask(ctx, tx, in.TaskID)
	if err != nil {
		return nil, err
	}
	policy, err := getPolicy(ctx, tx, task.ProjectID, " FOR SHARE")
	if err != nil {
		return nil, err
	}
	if !policy.Enabled {
		return nil, refuse("transition_policy_disabled", "receipt-bound product done is not enabled for this project")
	}
	if !policy.AcceptorAllowed(in.ActorID) {
		return nil, refuse("acceptor_not_authorized", "the calling credential is not an allowed accepting identity")
	}
	role := domain.AcceptorRoleReviewer
	isLead := policy.ProducerRole(in.ActorID) == domain.ProducerRoleLead
	if isLead {
		role = domain.AcceptorRoleLead // explicit audited lead role, recorded on the intent
	}
	// A lead may accept and execute, but only another identity's result: the
	// author/assignee/checkout holder is refused whatever the role.
	if isResultAuthor(task, in.ActorID) || (!isLead && policy.IsExecutor(in.ActorID)) {
		return nil, refuse("acceptor_not_independent", "the acceptor is the executor or the author of this result")
	}
	if task.Version != *req.ExpectedVersion || task.StatusID != req.ExpectedStatusID {
		return nil, &domain.TaskConflict{CurrentVersion: task.Version, CurrentStatusID: task.StatusID, CurrentUpdatedAt: task.UpdatedAt, CurrentAssigneeID: task.AssigneeID}
	}
	var target struct {
		ProjectID uuid.UUID `db:"project_id"`
		Category  string    `db:"category"`
	}
	if err = tx.GetContext(ctx, &target, `SELECT project_id, category FROM task_statuses WHERE id=$1`, req.ToStatusID); errors.Is(err, sql.ErrNoRows) {
		return nil, apierror.NotFound("TaskStatus")
	} else if err != nil {
		return nil, err
	}
	if target.ProjectID != task.ProjectID || target.Category != string(domain.StatusCategoryDone) {
		return nil, refuse("target_not_done_status", "to_status_id must be a done-category status of the task's project")
	}
	probe := domain.ProductDoneRequest{Class: req.Class, Evidence: req.Evidence, Reason: req.Reason}
	kind, evidence, err := r.buildEvidence(ctx, tx, task.ID, probe, policy)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(evidence)
	it := domain.AcceptanceIntent{
		ID: uuid.New(), TaskID: task.ID, ProjectID: task.ProjectID, Domain: req.Domain, Class: req.Class, Operation: req.Operation,
		FromStatusID: task.StatusID, FromVersion: task.Version, ToStatusID: req.ToStatusID, EvidenceKind: kind, Evidence: evidence,
		EvidenceHash: hex.EncodeToString(sum[:]), DeployRef: req.DeployRef, LiveRef: req.LiveRef, ResultAuthorID: task.AssigneeID,
		AcceptorActorID: in.ActorID, AcceptorActorType: string(in.ActorType), AcceptorRole: role, PolicyVersion: policy.PolicyVersion,
		Reason: req.Reason, CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	if _, err = tx.NamedExecContext(ctx, `INSERT INTO task_acceptance_intents(`+intentCols+`) VALUES(:id,:task_id,:project_id,:domain,:task_class,:operation,
 :from_status_id,:from_version,:to_status_id,:evidence_kind,:evidence,:evidence_hash,:deploy_ref,:live_ref,:result_author_id,:acceptor_actor_id,
 :acceptor_actor_type,:acceptor_role,:policy_version,:reason,:created_at)`, it); err != nil {
		return nil, err
	}
	if cerr := tx.Commit(); cerr != nil {
		return nil, cerr
	}
	return &it, nil
}

// RevokeAcceptanceIntent appends a revocation. Only the acceptor may revoke;
// a revoked intent can no longer back a receipt.
func (r *TransitionReceiptRepo) RevokeAcceptanceIntent(ctx context.Context, taskID, intentID, actor uuid.UUID, reason string) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// The row lock serialises with a product-done commit that consumes this
	// intent: the commit holds FOR SHARE until it ends, so a revocation either
	// lands before the commit looks (and refuses it) or after it finished.
	var acceptor uuid.UUID
	err = tx.GetContext(ctx, &acceptor, `SELECT acceptor_actor_id FROM task_acceptance_intents WHERE id=$1 AND task_id=$2 FOR UPDATE`, intentID, taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return apierror.NotFound("AcceptanceIntent")
	}
	if err != nil {
		return err
	}
	if acceptor != actor {
		return refuse("acceptor_not_authorized", "only the accepting identity may revoke its intent")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_acceptance_intent_revocations(intent_id, revoked_by, reason) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, intentID, actor, reason); err != nil {
		return err
	}
	return tx.Commit()
}

// ListAcceptanceIntents returns a task's intents, oldest first.
func (r *TransitionReceiptRepo) ListAcceptanceIntents(ctx context.Context, taskID uuid.UUID) ([]domain.AcceptanceIntent, error) {
	out := []domain.AcceptanceIntent{}
	err := r.db.SelectContext(ctx, &out, `SELECT `+intentCols+` FROM task_acceptance_intents WHERE task_id=$1 ORDER BY created_at, id`, taskID)
	return out, err
}

// loadIntentForCommit validates the stored intent against the transition being
// committed. Every mismatch refuses before any write.
func loadIntentForCommit(ctx context.Context, tx *sqlx.Tx, task *domain.Task, policy *domain.TransitionReceiptPolicy, in domain.TransitionCommitInput) (*domain.AcceptanceIntent, error) {
	req := in.Request
	if req.AcceptanceIntentID == nil {
		return nil, refuse("acceptance_intent_required", "acceptance_intent_id is required while enforce_done is on")
	}
	var it domain.AcceptanceIntent
	// FOR SHARE until commit: a concurrent revocation must wait for this
	// transition, and the checks below then see every committed revocation.
	err := tx.GetContext(ctx, &it, `SELECT `+intentCols+` FROM task_acceptance_intents WHERE id=$1 FOR SHARE`, *req.AcceptanceIntentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, refuse("acceptance_intent_not_found", "acceptance_intent_id does not name a recorded intent")
	}
	if err != nil {
		return nil, err
	}
	var revoked, used bool
	if rerr := tx.GetContext(ctx, &revoked, `SELECT EXISTS(SELECT 1 FROM task_acceptance_intent_revocations WHERE intent_id=$1)`, it.ID); rerr != nil {
		return nil, rerr
	}
	if uerr := tx.GetContext(ctx, &used, `SELECT EXISTS(SELECT 1 FROM task_transition_receipts WHERE acceptance_intent_id=$1)`, it.ID); uerr != nil {
		return nil, uerr
	}
	switch {
	case it.TaskID != task.ID || it.ProjectID != task.ProjectID:
		return nil, refuse("acceptance_intent_wrong_task", "the intent was recorded for another task")
	case revoked:
		return nil, refuse("acceptance_intent_revoked", "the intent was revoked")
	case used:
		return nil, refuse("acceptance_intent_used", "the intent already backs a receipt")
	case isResultAuthor(task, it.AcceptorActorID):
		return nil, refuse("acceptor_not_independent", "the acceptor is now the assignee or checkout holder of this result")
	case it.Domain != req.Domain || it.Class != req.Class || it.Operation != req.Operation:
		return nil, refuse("acceptance_intent_scope_mismatch", "domain/class/operation differ from the accepted scope")
	case it.FromStatusID != task.StatusID || it.FromVersion != task.Version || it.ToStatusID != req.ToStatusID:
		return nil, refuse("acceptance_intent_stale", "the task moved since the intent was recorded")
	case req.AcceptingActorID != uuid.Nil && req.AcceptingActorID != it.AcceptorActorID:
		return nil, refuse("acceptor_not_authorized", "accepting_actor_id differs from the authenticated acceptor of the intent")
	case !policy.AcceptorAllowed(it.AcceptorActorID):
		return nil, refuse("acceptor_not_authorized", "the acceptor is no longer allowed by the policy")
	case it.AcceptorRole == domain.AcceptorRoleReviewer && (it.AcceptorActorID == in.ActorID || policy.IsExecutor(it.AcceptorActorID)):
		return nil, refuse("acceptor_not_independent", "a reviewer acceptance must come from an identity other than the executor")
	case it.AcceptorRole == domain.AcceptorRoleLead && policy.ProducerRole(it.AcceptorActorID) != domain.ProducerRoleLead:
		return nil, refuse("acceptor_not_authorized", "the lead role is no longer granted by the policy")
	}
	return &it, nil
}

// isResultAuthor reports whether actor is the assignee or the checkout holder
// of the task, i.e. the identity whose result is being accepted.
func isResultAuthor(task *domain.Task, actor uuid.UUID) bool {
	return (task.AssigneeID != nil && *task.AssigneeID == actor) ||
		(task.CheckedOutBy != nil && *task.CheckedOutBy == actor)
}

// GetProductDoneAcceptance returns the consumer-facing acceptance record for the
// task's current state: the newest intent for exactly this status/version that is
// neither revoked nor already consumed. Only an actor bound to a producer by the
// policy may read it, and the view is stamped with that credential and producer.
func (r *TransitionReceiptRepo) GetProductDoneAcceptance(ctx context.Context, taskID, actor uuid.UUID) (map[string]any, error) {
	var task struct {
		ProjectID uuid.UUID `db:"project_id"`
		StatusID  uuid.UUID `db:"status_id"`
		Version   int64     `db:"version"`
	}
	if err := r.db.GetContext(ctx, &task, `SELECT project_id, status_id, version FROM tasks WHERE id=$1 AND deleted_at IS NULL`, taskID); errors.Is(err, sql.ErrNoRows) {
		return nil, apierror.NotFound("Task")
	} else if err != nil {
		return nil, err
	}
	policy, err := getPolicy(ctx, r.db, task.ProjectID, "")
	if err != nil {
		return nil, err
	}
	if !policy.Enabled {
		return nil, refuse("transition_policy_disabled", "receipt-bound product done is not enabled for this project")
	}
	producer := policy.ProducerFor(actor)
	if producer == "" {
		return nil, refuse("producer_not_authorized", "the calling credential is not a product-done producer")
	}
	var it domain.AcceptanceIntent
	err = r.db.GetContext(ctx, &it, `SELECT `+intentCols+` FROM task_acceptance_intents i
 WHERE i.task_id=$1 AND i.from_status_id=$2 AND i.from_version=$3
   AND NOT EXISTS (SELECT 1 FROM task_acceptance_intent_revocations v WHERE v.intent_id=i.id)
   AND NOT EXISTS (SELECT 1 FROM task_transition_receipts t WHERE t.acceptance_intent_id=i.id)
 ORDER BY i.created_at DESC, i.id DESC LIMIT 1`, taskID, task.StatusID, task.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apierror.NotFound("AcceptanceIntent")
	}
	if err != nil {
		return nil, err
	}
	return domain.BuildAcceptanceView(&it, policy.ScopeID, producer, actor)
}
