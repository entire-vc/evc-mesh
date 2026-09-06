//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
)

// These two tests exist because neither change they cover can be proved by a
// mock. The progress signal and the URL match are both SQL, and a mock that
// mirrors SQL only ever proves the mirror. They call the real repository methods
// against a real Postgres so the thing under test is the query that ships.

// ── fixtures ──────────────────────────────────────────────────────────────

type midPipelineFixture struct {
	db         *sqlx.DB
	workspace  uuid.UUID
	project    uuid.UUID
	inProgress uuid.UUID
}

func newMidPipelineFixture(t *testing.T) *midPipelineFixture {
	t.Helper()
	db := testDB(t)
	ctx := context.Background()

	ws := uuid.New()
	_, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug, owner_id, created_at, updated_at)
		 VALUES ($1, 'mp-gate', $2, $3, now(), now())`,
		ws, "mp-gate-"+ws.String()[:8], uuid.New())
	require.NoError(t, err)

	proj := uuid.New()
	_, err = db.ExecContext(ctx,
		`INSERT INTO projects (id, workspace_id, name, slug, created_at, updated_at)
		 VALUES ($1, $2, 'mp-gate', $3, now(), now())`,
		proj, ws, "mp-gate-"+proj.String()[:8])
	require.NoError(t, err)

	st := uuid.New()
	_, err = db.ExecContext(ctx,
		`INSERT INTO task_statuses (id, project_id, name, slug, category, position)
		 VALUES ($1, $2, 'In Progress', 'in_progress', 'in_progress', 1)`,
		st, proj)
	require.NoError(t, err)

	// tasks.task_number is NOT NULL with no default; a sequence keeps each
	// fixture row unique without the test having to track a counter.
	_, err = db.ExecContext(ctx, `CREATE SEQUENCE IF NOT EXISTS mp_gate_task_number`)
	require.NoError(t, err)

	return &midPipelineFixture{db: db, workspace: ws, project: proj, inProgress: st}
}

// insertUnleasedTask creates an in_progress task with no checkout whose
// tasks.updated_at is `staleFor` in the past.
func (f *midPipelineFixture) insertUnleasedTask(t *testing.T, staleFor time.Duration) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.db.ExecContext(context.Background(),
		`INSERT INTO tasks (id, project_id, status_id, title, task_number, created_by, position, created_at, updated_at)
		 VALUES ($1, $2, $3, 'stalled card', nextval('mp_gate_task_number'), $4, 1,
		         now() - $5::interval, now() - $5::interval)`,
		id, f.project, f.inProgress, uuid.New(), intervalArg(staleFor))
	require.NoError(t, err)
	return id
}

func (f *midPipelineFixture) insertComment(t *testing.T, taskID uuid.UUID, body string, age time.Duration) {
	t.Helper()
	_, err := f.db.ExecContext(context.Background(),
		`INSERT INTO comments (id, task_id, author_id, author_type, body, created_at)
		 VALUES ($1, $2, $3, 'agent', $4, now() - $5::interval)`,
		uuid.New(), taskID, uuid.New(), body, intervalArg(age))
	require.NoError(t, err)
}

// ── the progress signal ───────────────────────────────────────────────────

// The failure this query change exists to prevent, driven end to end.
//
// add_comment does not touch tasks.updated_at — only a status change does. So an
// agent that reports progress the way the workflow tells it to (a comment every
// few minutes, status unchanged because the work is unfinished) looks identical
// to a dead session under an updated_at-only test, and the sweep takes the card
// off a lane that is actively working it.
//
// This is the whole point of the change and the case a mock cannot express,
// because the mock has no comments table to join.
func TestFindStaleUnleasedInProgress_RecentCommentCountsAsProgress(t *testing.T) {
	f := newMidPipelineFixture(t)
	repo := NewTaskRepo(f.db)
	ctx := context.Background()

	// updated_at is 5 hours stale on BOTH, which is what an updated_at-only
	// query sees. The only difference is the comment.
	silent := f.insertUnleasedTask(t, 5*time.Hour)
	talking := f.insertUnleasedTask(t, 5*time.Hour)
	f.insertComment(t, talking, "всё ещё работаю, разбираю миграцию", 3*time.Minute)

	got, err := repo.FindStaleUnleasedInProgress(ctx, 2*time.Hour)
	require.NoError(t, err)

	found := map[uuid.UUID]bool{}
	for _, task := range got {
		found[task.ID] = true
	}

	require.True(t, found[silent],
		"a genuinely silent unleased card was NOT swept — the sweep stopped doing its job")
	require.False(t, found[talking],
		"the sweep took a card whose agent commented 3 minutes ago; under an updated_at-only "+
			"test these two cards are indistinguishable, which is exactly the bug")
}

// The negative control on the control: an OLD comment must not rescue a card.
// Without this, "any comment ever" would exempt every card forever and the sweep
// would be dead rather than merely wrong.
func TestFindStaleUnleasedInProgress_OldCommentDoesNotRescue(t *testing.T) {
	f := newMidPipelineFixture(t)
	repo := NewTaskRepo(f.db)

	id := f.insertUnleasedTask(t, 5*time.Hour)
	f.insertComment(t, id, "начал работу", 4*time.Hour)

	got, err := repo.FindStaleUnleasedInProgress(context.Background(), 2*time.Hour)
	require.NoError(t, err)

	var found bool
	for _, task := range got {
		if task.ID == id {
			found = true
		}
	}
	require.True(t, found,
		"a card whose only comment is 4h old was exempted — the freshness join has become "+
			"'has ever been commented on', which would disable the sweep entirely")
}

// ── the URL match ─────────────────────────────────────────────────────────

func TestHasCommentWithURL(t *testing.T) {
	f := newMidPipelineFixture(t)
	repo := NewCommentRepo(f.db)
	ctx := context.Background()

	cases := []struct {
		name string
		body string
		want bool
	}{
		{"plain prose", "сделал, всё работает, тесты прошли", false},
		{"bare scheme, nothing after it", "see https:// for details", false},
		{"http url", "лог: http://ci.internal/job/9", true},
		{"https url", "MR https://git.entire.host/entire-vc/evc-mesh/-/merge_requests/12", true},
		{"uppercase scheme", "proof at HTTPS://EXAMPLE.COM/x", true},
		{"url on a later line", "готово\n\nпруф: https://x.test/y", true},
		// The reason the pattern requires a non-word char before the scheme:
		// otherwise any word ending in the letters "http" would match.
		{"scheme glued to a preceding word", "nothttps://x.test", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := f.insertUnleasedTask(t, time.Minute)
			f.insertComment(t, id, tc.body, time.Minute)

			got, err := repo.HasCommentWithURL(ctx, id)
			require.NoError(t, err)
			require.Equal(t, tc.want, got, "body %q", tc.body)
		})
	}
}

// A task with no comments at all must answer false, not error.
func TestHasCommentWithURL_NoComments(t *testing.T) {
	f := newMidPipelineFixture(t)
	repo := NewCommentRepo(f.db)

	id := f.insertUnleasedTask(t, time.Minute)
	got, err := repo.HasCommentWithURL(context.Background(), id)
	require.NoError(t, err)
	require.False(t, got)
}

// Only ONE of several comments needs the URL.
func TestHasCommentWithURL_AnyOneCommentSuffices(t *testing.T) {
	f := newMidPipelineFixture(t)
	repo := NewCommentRepo(f.db)

	id := f.insertUnleasedTask(t, time.Minute)
	f.insertComment(t, id, "начал", 10*time.Minute)
	f.insertComment(t, id, "разбираюсь", 5*time.Minute)
	f.insertComment(t, id, "готово: https://git.entire.host/x/-/merge_requests/1", time.Minute)

	got, err := repo.HasCommentWithURL(context.Background(), id)
	require.NoError(t, err)
	require.True(t, got)
}

func intervalArg(d time.Duration) string {
	return d.String()
}
