//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ExtendCheckoutsOnHeartbeat's per-project gating lives entirely in a SQL
// join against project_rules.config (JSONB), which no mock can meaningfully
// fake — a mock that "gates" would just be re-implementing the same decision
// in Go and testing that Go against itself. This exercises the real query
// against a real Postgres.

type heartbeatExtendFixture struct {
	db        *sqlx.DB
	workspace uuid.UUID
}

func newHeartbeatExtendFixture(t *testing.T) *heartbeatExtendFixture {
	t.Helper()
	f := &heartbeatExtendFixture{db: testDB(t), workspace: uuid.New()}
	_, err := f.db.ExecContext(context.Background(),
		`INSERT INTO workspaces (id, name, slug, owner_id, created_at, updated_at)
		 VALUES ($1, 'hb-extend', $2, $3, now(), now())`,
		f.workspace, "hb-extend-"+f.workspace.String()[:8], uuid.New())
	require.NoError(t, err)
	return f
}

// agent inserts a minimal real agent row — checked_out_by carries an FK to
// agents(id), so a bare uuid.New() is rejected outright.
func (f *heartbeatExtendFixture) agent(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.db.ExecContext(context.Background(),
		`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix)
		 VALUES ($1, $2, 'HB Extend Agent', $3, 'not-a-real-hash', 'test')`,
		id, f.workspace, "hb-extend-agent-"+id.String()[:8])
	require.NoError(t, err)
	return id
}

// project creates a workspace + project, optionally with a project_rules
// workflow row carrying the given mid_pipeline JSON (pass "" for no row at
// all — the fail-closed "never configured" case).
func (f *heartbeatExtendFixture) project(t *testing.T, midPipelineJSON string) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	proj := uuid.New()
	_, err := f.db.ExecContext(ctx,
		`INSERT INTO projects (id, workspace_id, name, slug, created_at, updated_at)
		 VALUES ($1, $2, 'hb-extend', $3, now(), now())`,
		proj, f.workspace, "hb-extend-"+proj.String()[:8])
	require.NoError(t, err)

	if midPipelineJSON != "" {
		_, err = f.db.ExecContext(ctx,
			`INSERT INTO project_rules (id, project_id, rule_type, config, enforcement_mode, created_at, updated_at)
			 VALUES ($1, $2, 'workflow', $3::jsonb, 'advisory', now(), now())`,
			uuid.New(), proj, `{"mid_pipeline":`+midPipelineJSON+`}`)
		require.NoError(t, err)
	}

	_, err = f.db.ExecContext(ctx, `CREATE SEQUENCE IF NOT EXISTS hb_extend_task_number`)
	require.NoError(t, err)

	return proj
}

// checkedOutTask inserts a task checked out by agentID, expiring at expiresIn
// from now (negative = already expired, to prove the reaper's own claim on it
// does not block a heartbeat from reclaiming it).
func (f *heartbeatExtendFixture) checkedOutTask(t *testing.T, projectID, agentID uuid.UUID, expiresIn time.Duration) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.db.ExecContext(context.Background(),
		`INSERT INTO tasks (id, project_id, status_id, title, task_number, created_by, position,
		                     created_at, updated_at, checked_out_by, checkout_token, checkout_expires)
		 SELECT $1, $2, ts.id, 'checked-out card', nextval('hb_extend_task_number'), $3, 1,
		        now(), now(), $3, $4, now() + $5::interval
		 FROM task_statuses ts WHERE ts.project_id = $2 LIMIT 1`,
		id, projectID, agentID, uuid.New(), expiresIn.String())
	if err != nil {
		// No task_statuses row for this project yet — create a minimal one
		// (in_progress, matching what a real checkout lives in) and retry.
		require.NoError(t, err, "checkedOutTask requires a task_statuses row to exist first; call withStatus(t) before this")
	}
	return id
}

func (f *heartbeatExtendFixture) withStatus(t *testing.T, projectID uuid.UUID) {
	t.Helper()
	_, err := f.db.ExecContext(context.Background(),
		`INSERT INTO task_statuses (id, project_id, name, slug, category, position)
		 VALUES ($1, $2, 'In Progress', 'in_progress', 'in_progress', 1)`,
		uuid.New(), projectID)
	require.NoError(t, err)
}

func (f *heartbeatExtendFixture) checkoutExpires(t *testing.T, taskID uuid.UUID) *time.Time {
	t.Helper()
	var expires *time.Time
	err := f.db.GetContext(context.Background(), &expires,
		`SELECT checkout_expires FROM tasks WHERE id = $1`, taskID)
	require.NoError(t, err)
	return expires
}

// ── the actual gate ──────────────────────────────────────────────────────

func TestExtendCheckoutsOnHeartbeat_ProjectOptedIn_Extends(t *testing.T) {
	f := newHeartbeatExtendFixture(t)
	repo := NewTaskRepo(f.db)
	agentID := f.agent(t)

	proj := f.project(t, `{"heartbeat_extends_checkout":true}`)
	f.withStatus(t, proj)
	// Already expired (per the reaper's own claim) — heartbeat must still
	// win the race and extend it, since heartbeat and the reaper are both
	// reading/writing the same row and this is the case that matters: an
	// agent whose session is genuinely alive right up to (or past) the wire.
	taskID := f.checkedOutTask(t, proj, agentID, -1*time.Minute)

	before := f.checkoutExpires(t, taskID)
	require.NotNil(t, before)

	n, err := repo.ExtendCheckoutsOnHeartbeat(context.Background(), agentID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	after := f.checkoutExpires(t, taskID)
	require.NotNil(t, after)
	assert.True(t, after.After(*before),
		"checkout_expires must move forward: before=%v after=%v", before, after)
	// Default extension is 30 minutes; allow slack for test execution time.
	assert.WithinDuration(t, time.Now().Add(30*time.Minute), *after, 2*time.Minute)
}

func TestExtendCheckoutsOnHeartbeat_ProjectNotConfigured_DoesNotExtend(t *testing.T) {
	f := newHeartbeatExtendFixture(t)
	repo := NewTaskRepo(f.db)
	agentID := f.agent(t)

	// No project_rules row at all — the fail-closed "never opted in" case,
	// which must behave identically to explicitly disabled.
	proj := f.project(t, "")
	f.withStatus(t, proj)
	taskID := f.checkedOutTask(t, proj, agentID, 10*time.Minute)

	before := f.checkoutExpires(t, taskID)

	n, err := repo.ExtendCheckoutsOnHeartbeat(context.Background(), agentID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)

	after := f.checkoutExpires(t, taskID)
	assert.Equal(t, before.UTC(), after.UTC(), "checkout_expires must be untouched when the project never opted in")
}

func TestExtendCheckoutsOnHeartbeat_ProjectExplicitlyOptedOut_DoesNotExtend(t *testing.T) {
	f := newHeartbeatExtendFixture(t)
	repo := NewTaskRepo(f.db)
	agentID := f.agent(t)

	// A project WITH a mid_pipeline block (e.g. it uses the other flags from
	// the sibling gates) but heartbeat_extends_checkout absent/false — proves
	// the query keys on this ONE field, not "any mid_pipeline block at all".
	proj := f.project(t, `{"review_evidence_strict":true,"auto_park_stalled":true}`)
	f.withStatus(t, proj)
	taskID := f.checkedOutTask(t, proj, agentID, 10*time.Minute)

	before := f.checkoutExpires(t, taskID)

	n, err := repo.ExtendCheckoutsOnHeartbeat(context.Background(), agentID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)

	after := f.checkoutExpires(t, taskID)
	assert.Equal(t, before.UTC(), after.UTC())
}

func TestExtendCheckoutsOnHeartbeat_CustomExtendMinutes_Honored(t *testing.T) {
	f := newHeartbeatExtendFixture(t)
	repo := NewTaskRepo(f.db)
	agentID := f.agent(t)

	proj := f.project(t, `{"heartbeat_extends_checkout":true,"heartbeat_checkout_extend_minutes":90}`)
	f.withStatus(t, proj)
	taskID := f.checkedOutTask(t, proj, agentID, 5*time.Minute)

	n, err := repo.ExtendCheckoutsOnHeartbeat(context.Background(), agentID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	after := f.checkoutExpires(t, taskID)
	require.NotNil(t, after)
	assert.WithinDuration(t, time.Now().Add(90*time.Minute), *after, 2*time.Minute,
		"custom heartbeat_checkout_extend_minutes=90 must be honored, not the default 30")
}

func TestExtendCheckoutsOnHeartbeat_OtherAgentsCheckoutUntouched(t *testing.T) {
	f := newHeartbeatExtendFixture(t)
	repo := NewTaskRepo(f.db)
	agentID := f.agent(t)
	otherAgentID := f.agent(t)

	proj := f.project(t, `{"heartbeat_extends_checkout":true}`)
	f.withStatus(t, proj)
	mine := f.checkedOutTask(t, proj, agentID, 10*time.Minute)
	theirs := f.checkedOutTask(t, proj, otherAgentID, 10*time.Minute)
	theirsBefore := f.checkoutExpires(t, theirs)

	n, err := repo.ExtendCheckoutsOnHeartbeat(context.Background(), agentID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "must extend exactly the calling agent's own checkout")

	mineAfter := f.checkoutExpires(t, mine)
	require.NotNil(t, mineAfter)
	theirsAfter := f.checkoutExpires(t, theirs)
	assert.Equal(t, theirsBefore.UTC(), theirsAfter.UTC(),
		"a heartbeat from one agent must never touch another agent's checkout")
}

func TestExtendCheckoutsOnHeartbeat_NoLiveCheckout_ReturnsZero(t *testing.T) {
	f := newHeartbeatExtendFixture(t)
	repo := NewTaskRepo(f.db)

	n, err := repo.ExtendCheckoutsOnHeartbeat(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.Equal(t, int64(0), n, "an agent holding no checkout at all must be a normal, non-error zero")
}
