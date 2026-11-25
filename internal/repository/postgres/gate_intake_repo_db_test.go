package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// DB tests of GateIntakeRepo (contract CH→Mesh gate-create/v1,
// docs/gate-intake.md) against a migrated Postgres: the operator lifecycle
// (issue, rotate with and without grace, revoke, re-arm), the binding checks
// that refuse a bad destination at issue time, and the receipt that makes the
// intake idempotent. The end-to-end HTTP behaviour lives in
// internal/handler/gate_intake_db_test.go. Skips without a reachable Postgres
// (agentDigestTestDB, like the other *_db_test.go files here).

type gateIntakeRepoFixture struct {
	db        *sqlx.DB
	repo      *GateIntakeRepo
	wsID      uuid.UUID
	projectID uuid.UUID
	agentID   uuid.UUID
	operator  uuid.UUID
	name      string
}

func newGateIntakeRepoFixture(t *testing.T) *gateIntakeRepoFixture {
	t.Helper()
	db := agentDigestTestDB(t)
	ctx := context.Background()
	wsID := seedGrantWorkspace(t, db)
	agent := seedGrantAgent(t, db, wsID)
	projectID := seedGateIntakeProject(t, db, wsID, true)
	f := &gateIntakeRepoFixture{db: db, repo: NewGateIntakeRepo(db), wsID: wsID, projectID: projectID,
		agentID: agent.ID, operator: uuid.New(), name: "gi-repo-" + uuid.NewString()[:8]}
	t.Cleanup(func() {
		var ids []uuid.UUID
		_ = db.SelectContext(ctx, &ids, `SELECT id FROM gate_intake_principals WHERE workspace_id = $1`, wsID)
		for _, id := range ids {
			_, _ = db.ExecContext(ctx, `DELETE FROM gate_intake_receipts WHERE principal_id = $1`, id)
			_, _ = db.ExecContext(ctx, `DELETE FROM gate_intake_audit WHERE principal_id = $1`, id)
			_, _ = db.ExecContext(ctx, `DELETE FROM gate_intake_principals WHERE id = $1`, id)
		}
		_, _ = db.ExecContext(ctx, `DELETE FROM tasks WHERE project_id IN (SELECT id FROM projects WHERE workspace_id = $1)`, wsID)
		_, _ = db.ExecContext(ctx, `DELETE FROM task_statuses WHERE project_id IN (SELECT id FROM projects WHERE workspace_id = $1)`, wsID)
		_, _ = db.ExecContext(ctx, `DELETE FROM projects WHERE workspace_id = $1`, wsID)
		_, _ = db.ExecContext(ctx, `DELETE FROM agents WHERE workspace_id = $1`, wsID)
	})
	return f
}

// seedGateIntakeProject creates a project in wsID, with a default status when
// withDefault is set, and returns its id.
func seedGateIntakeProject(t *testing.T, db *sqlx.DB, wsID uuid.UUID, withDefault bool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()[:8]
	p := &domain.Project{ID: uuid.New(), WorkspaceID: wsID, Name: "gate " + suffix, Slug: "gate-" + suffix,
		DefaultAssigneeType: domain.DefaultAssigneeNone}
	require.NoError(t, NewProjectRepo(db).Create(ctx, p))
	require.NoError(t, NewTaskStatusRepo(db).Create(ctx, &domain.TaskStatus{ID: uuid.New(), ProjectID: p.ID, Name: "Todo",
		Slug: "todo", Color: "#00FF00", Position: 0, Category: domain.StatusCategoryTodo, IsDefault: withDefault}))
	return p.ID
}

func (f *gateIntakeRepoFixture) binding() GateIntakeBinding {
	return GateIntakeBinding{Name: f.name, WorkspaceID: f.wsID, ProjectID: f.projectID, AssigneeAgentID: f.agentID,
		Labels: []string{"kind:ops", "spark-gate"}, Priority: domain.PriorityHigh, Schema: domain.GateIntakeSchemaContentHub}
}

func newRepoToken(t *testing.T) (tok, hash, prefix string) {
	t.Helper()
	tok, err := domain.NewGateIntakeToken()
	require.NoError(t, err)
	return tok, domain.HashGateIntakeToken(tok), domain.GateIntakeTokenDisplay(tok)
}

func (f *gateIntakeRepoFixture) found(t *testing.T, hash string) *domain.GateIntakePrincipal {
	t.Helper()
	p, err := f.repo.PrincipalByTokenHash(context.Background(), hash)
	require.NoError(t, err)
	return p
}

func requireOperatorErr(t *testing.T, err error, contains string) {
	t.Helper()
	var oe *GateIntakeOperatorError
	require.ErrorAs(t, err, &oe)
	assert.Contains(t, oe.Error(), contains)
}

func TestGateIntakeRepo_Lifecycle(t *testing.T) {
	f := newGateIntakeRepoFixture(t)
	ctx := context.Background()

	missing, err := f.repo.PrincipalByName(ctx, f.name)
	require.NoError(t, err)
	assert.Nil(t, missing, "no principal before the first issue")

	_, h1, p1 := newRepoToken(t)
	p, action, err := f.repo.Issue(ctx, f.operator, f.binding(), h1, p1)
	require.NoError(t, err)
	assert.Equal(t, "issue", action)
	assert.Equal(t, f.name, p.Name)
	assert.Equal(t, f.projectID, p.ProjectID)
	assert.Equal(t, f.agentID, p.AssigneeAgentID)
	assert.Equal(t, []string{"kind:ops", "spark-gate"}, p.Labels)
	assert.Equal(t, domain.PriorityHigh, p.Priority)
	require.NotNil(t, p.TokenPrefix)
	assert.Equal(t, p1, *p.TokenPrefix)
	principalID := p.ID

	got := f.found(t, h1)
	require.NotNil(t, got, "the issued token authenticates")
	assert.Equal(t, principalID, got.ID)
	assert.Nil(t, f.found(t, domain.HashGateIntakeToken("mgi_unknown")), "an unknown hash finds nothing")

	// A live name is never re-issued: its key changes only through Rotate.
	_, hx, px := newRepoToken(t)
	_, _, err = f.repo.Issue(ctx, f.operator, f.binding(), hx, px)
	requireOperatorErr(t, err, "is live")
	assert.Nil(t, f.found(t, hx), "the refused issue wrote nothing")

	// Rotate with grace: both the new and the previous token work.
	_, h2, p2 := newRepoToken(t)
	p, err = f.repo.Rotate(ctx, f.operator, f.name, h2, p2, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, principalID, p.ID, "rotation keeps the principal id (the receipt namespace)")
	require.NotNil(t, p.PreviousValidUntil)
	assert.True(t, p.PreviousValidUntil.After(time.Now().Add(50*time.Minute)))
	assert.NotNil(t, f.found(t, h2))
	assert.NotNil(t, f.found(t, h1), "the previous token works inside its grace")

	// Rotate without grace: only the newest token works.
	_, h3, p3 := newRepoToken(t)
	p, err = f.repo.Rotate(ctx, f.operator, f.name, h3, p3, 0)
	require.NoError(t, err)
	assert.Nil(t, p.PreviousValidUntil)
	assert.NotNil(t, f.found(t, h3))
	assert.Nil(t, f.found(t, h2), "grace 0 ends the previous token at once")
	assert.Nil(t, f.found(t, h1))

	// An expired grace ends the previous token too.
	_, h4, p4 := newRepoToken(t)
	_, err = f.repo.Rotate(ctx, f.operator, f.name, h4, p4, time.Hour)
	require.NoError(t, err)
	_, err = f.db.ExecContext(ctx, `UPDATE gate_intake_principals SET previous_valid_until = now() - interval '1 second' WHERE id = $1`, principalID)
	require.NoError(t, err)
	assert.Nil(t, f.found(t, h3), "a previous token after its grace is refused")
	assert.NotNil(t, f.found(t, h4))

	// Revoke ends every token; a second revoke and a rotate are refused.
	p, err = f.repo.Revoke(ctx, f.operator, f.name, "leaked in a log")
	require.NoError(t, err)
	require.NotNil(t, p.RevokedAt)
	require.NotNil(t, p.RevokeReason)
	assert.Equal(t, "leaked in a log", *p.RevokeReason)
	assert.Nil(t, f.found(t, h4), "a revoked principal does not authenticate")
	_, err = f.repo.Revoke(ctx, f.operator, f.name, "again")
	requireOperatorErr(t, err, "already revoked")
	_, h5, p5 := newRepoToken(t)
	_, err = f.repo.Rotate(ctx, f.operator, f.name, h5, p5, 0)
	requireOperatorErr(t, err, "is revoked")

	// A re-issue must keep the binding the principal had.
	other := f.binding()
	other.Labels = []string{"kind:ops"}
	_, _, err = f.repo.Issue(ctx, f.operator, other, h5, p5)
	requireOperatorErr(t, err, "keeps that binding")
	other = f.binding()
	other.Priority = domain.PriorityLow
	_, _, err = f.repo.Issue(ctx, f.operator, other, h5, p5)
	requireOperatorErr(t, err, "keeps that binding")
	assert.Nil(t, f.found(t, h5))

	// Re-arm: the same id, a fresh token, no revoke marks.
	p, action, err = f.repo.Issue(ctx, f.operator, f.binding(), h5, p5)
	require.NoError(t, err)
	assert.Equal(t, "rearm", action)
	assert.Equal(t, principalID, p.ID)
	assert.Nil(t, p.RevokedAt)
	assert.Nil(t, p.RevokeReason)
	assert.NotNil(t, f.found(t, h5))

	byName, err := f.repo.PrincipalByName(ctx, f.name)
	require.NoError(t, err)
	assert.Equal(t, principalID, byName.ID)

	audit, err := f.repo.Audit(ctx, principalID)
	require.NoError(t, err)
	actions := make([]string, 0, len(audit))
	for _, a := range audit {
		actions = append(actions, a.Action)
		assert.Equal(t, f.operator, a.OperatorID)
	}
	assert.Equal(t, []string{"issue", "rotate", "rotate", "rotate", "revoke", "rearm"}, actions,
		"refused commands leave no audit row")
	require.NotNil(t, audit[0].TokenPrefix)
	assert.Equal(t, p1, *audit[0].TokenPrefix)
	var issued map[string]any
	require.NoError(t, json.Unmarshal(audit[0].Details, &issued))
	assert.Equal(t, f.projectID.String(), issued["project_id"])
	var rotated map[string]any
	require.NoError(t, json.Unmarshal(audit[1].Details, &rotated))
	assert.EqualValues(t, 3600, rotated["grace_seconds"])
	assert.Equal(t, p1, rotated["previous_token_prefix"])
	require.NotNil(t, audit[4].Reason)
	assert.Equal(t, "leaked in a log", *audit[4].Reason)
	assert.Nil(t, audit[4].TokenPrefix, "a revoke carries no token prefix")
	for _, a := range audit {
		for _, h := range []string{h1, h2, h3, h4, h5} {
			assert.NotContains(t, string(a.Details), h, "the audit never holds a token hash")
		}
	}
}

func TestGateIntakeRepo_UnknownPrincipal(t *testing.T) {
	f := newGateIntakeRepoFixture(t)
	ctx := context.Background()
	_, h, p := newRepoToken(t)
	_, err := f.repo.Rotate(ctx, f.operator, f.name, h, p, 0)
	requireOperatorErr(t, err, "no principal")
	_, err = f.repo.Revoke(ctx, f.operator, f.name, "x")
	requireOperatorErr(t, err, "no principal")
}

func TestGateIntakeRepo_IssueRefusesBadBinding(t *testing.T) {
	f := newGateIntakeRepoFixture(t)
	ctx := context.Background()
	otherWS := seedGrantWorkspace(t, f.db)
	foreignProject := seedGateIntakeProject(t, f.db, otherWS, true)
	foreignAgent := seedGrantAgent(t, f.db, otherWS)
	noDefault := seedGateIntakeProject(t, f.db, f.wsID, false)
	t.Cleanup(func() {
		_, _ = f.db.ExecContext(ctx, `DELETE FROM task_statuses WHERE project_id = $1`, foreignProject)
		_, _ = f.db.ExecContext(ctx, `DELETE FROM projects WHERE id = $1`, foreignProject)
		_, _ = f.db.ExecContext(ctx, `DELETE FROM agents WHERE id = $1`, foreignAgent.ID)
	})

	cases := []struct {
		name   string
		mutate func(*GateIntakeBinding)
		want   string
	}{
		{"priority", func(b *GateIntakeBinding) { b.Priority = "critical" }, "is not a task priority"},
		{"schema", func(b *GateIntakeBinding) { b.Schema = "other/v1" }, "schema must be"},
		{"project outside workspace", func(b *GateIntakeBinding) { b.ProjectID = foreignProject }, "is not in workspace"},
		{"agent outside workspace", func(b *GateIntakeBinding) { b.AssigneeAgentID = foreignAgent.ID }, "is not in workspace"},
		{"project without default status", func(b *GateIntakeBinding) { b.ProjectID = noDefault }, "no default task status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := f.binding()
			tc.mutate(&b)
			_, h, p := newRepoToken(t)
			_, _, err := f.repo.Issue(ctx, f.operator, b, h, p)
			requireOperatorErr(t, err, tc.want)
			got, err := f.repo.PrincipalByName(ctx, f.name)
			require.NoError(t, err)
			assert.Nil(t, got, "a refused binding creates no principal")
		})
	}
	for _, pr := range []domain.Priority{domain.PriorityUrgent, domain.PriorityMedium, domain.PriorityLow, domain.PriorityNone} {
		b := f.binding()
		b.Name = "gi-prio-" + uuid.NewString()[:8]
		b.Priority = pr
		_, h, p := newRepoToken(t)
		_, _, err := f.repo.Issue(ctx, f.operator, b, h, p)
		require.NoError(t, err, "priority %s is a valid binding", pr)
	}
}

func TestGateIntakeRepo_Receipt(t *testing.T) {
	f := newGateIntakeRepoFixture(t)
	ctx := context.Background()
	_, h, pfx := newRepoToken(t)
	p, _, err := f.repo.Issue(ctx, f.operator, f.binding(), h, pfx)
	require.NoError(t, err)

	var statusID uuid.UUID
	require.NoError(t, f.db.GetContext(ctx, &statusID, `SELECT id FROM task_statuses WHERE project_id = $1 AND is_default`, f.projectID))
	task := &domain.Task{ID: uuid.New(), ProjectID: f.projectID, StatusID: statusID, Title: "gate card",
		Priority: domain.PriorityHigh, AssigneeType: domain.AssigneeTypeUnassigned, CreatedBy: p.ID, CreatedByType: domain.ActorTypeSystem}
	require.NoError(t, NewTaskRepo(f.db).Create(ctx, task, nil))

	approval := uuid.New()
	rec0, err := f.repo.GetReceipt(ctx, p.ID, approval)
	require.NoError(t, err)
	assert.Nil(t, rec0)
	n, err := f.repo.ReceiptCount(ctx, p.ID)
	require.NoError(t, err)
	assert.Zero(t, n)

	sha := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	rec := &domain.GateIntakeReceipt{PrincipalID: p.ID, ApprovalID: approval, RequestSHA256: sha, TaskID: task.ID,
		Response: json.RawMessage(`{"task_id":"` + task.ID.String() + `"}`), CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	insert := func() bool {
		tx, berr := f.db.BeginTx(ctx, nil)
		require.NoError(t, berr)
		ok, ierr := f.repo.InsertReceiptTx(ctx, tx, rec, p.ID)
		require.NoError(t, ierr)
		require.NoError(t, tx.Commit())
		return ok
	}
	assert.True(t, insert(), "the first receipt is written")
	assert.False(t, insert(), "a second receipt for the same approval writes nothing")

	stored, err := f.repo.GetReceipt(ctx, p.ID, approval)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, task.ID, stored.TaskID)
	assert.Equal(t, sha, stored.RequestSHA256)
	assert.JSONEq(t, string(rec.Response), string(stored.Response))
	n, err = f.repo.ReceiptCount(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	var ref, refHash string
	require.NoError(t, f.db.QueryRowxContext(ctx, `SELECT ref, hash FROM task_external_refs WHERE task_id = $1`, task.ID).Scan(&ref, &refHash))
	assert.Equal(t, domain.GateIntakeExternalRefPrefix+approval.String(), ref)
	assert.Equal(t, sha, refHash)

	// A receipt row the database refuses (bad fingerprint) is an error, not a replay.
	bad := *rec
	bad.ApprovalID = uuid.New()
	bad.RequestSHA256 = "not-hex"
	tx, err := f.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = f.repo.InsertReceiptTx(ctx, tx, &bad, p.ID)
	require.Error(t, err)
	_ = tx.Rollback()

	// The external ref is the card's only one: a second approval bound to the
	// same card fails inside the transaction and leaves no receipt.
	dup := *rec
	dup.ApprovalID = uuid.New()
	tx, err = f.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = f.repo.InsertReceiptTx(ctx, tx, &dup, p.ID)
	require.Error(t, err)
	_ = tx.Rollback()
	gone, err := f.repo.GetReceipt(ctx, p.ID, dup.ApprovalID)
	require.NoError(t, err)
	assert.Nil(t, gone)
}

// A storage failure is returned as is, never mistaken for "no principal" or
// "no receipt" (which would read as 401 or as a new card).
func TestGateIntakeRepo_StorageErrors(t *testing.T) {
	f := newGateIntakeRepoFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, h, p := newRepoToken(t)
	_, err := f.repo.PrincipalByTokenHash(ctx, h)
	require.Error(t, err)
	_, err = f.repo.GetReceipt(ctx, uuid.New(), uuid.New())
	require.Error(t, err)
	_, _, err = f.repo.Issue(ctx, f.operator, f.binding(), h, p)
	require.Error(t, err)
	_, err = f.repo.Rotate(ctx, f.operator, f.name, h, p, 0)
	require.Error(t, err)
	_, err = f.repo.Revoke(ctx, f.operator, f.name, "x")
	require.Error(t, err)
}

// A create-tx hook that refuses (the gate intake's "receipt exists") must roll
// the card's transaction back. Before the fix the refusal returned without
// rollback: the transaction stayed open on its connection, still holding the
// per-project advisory lock of TaskRepo.Create, and every later card of that
// project waited on the lock forever.
func TestTaskRepo_CreateHookRefusalReleasesTheProjectLock_GateIntake(t *testing.T) {
	f := newGateIntakeRepoFixture(t)
	ctx := context.Background()
	repo := NewTaskRepo(f.db)
	var statusID uuid.UUID
	require.NoError(t, f.db.GetContext(ctx, &statusID, `SELECT id FROM task_statuses WHERE project_id = $1 AND is_default`, f.projectID))
	newTask := func() *domain.Task {
		return &domain.Task{ID: uuid.New(), ProjectID: f.projectID, StatusID: statusID, Title: "hook card",
			Priority: domain.PriorityHigh, AssigneeType: domain.AssigneeTypeUnassigned, CreatedBy: f.operator,
			CreatedByType: domain.ActorTypeSystem}
	}

	refused := newTask()
	refusal := errors.New("receipt exists")
	hctx := repository.WithTaskCreateTxHook(ctx, refused.ID, func(context.Context, *sql.Tx) error { return refusal })
	require.ErrorIs(t, repo.Create(hctx, refused, nil), refusal)

	var n int
	require.NoError(t, f.db.GetContext(ctx, &n, `SELECT count(*) FROM tasks WHERE id = $1`, refused.ID))
	assert.Zero(t, n, "the refused card is not committed")

	next := newTask()
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	require.NoError(t, repo.Create(tctx, next, nil), "the next card of the project is not blocked by the refused one")
}
