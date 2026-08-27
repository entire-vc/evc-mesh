//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// FindStaleUnleasedInProgress carries three predicates that unit tests cannot
// reach — they live in SQL, and the service-layer mock re-implements them, so a
// unit test proves only that the mock agrees with itself. Each one is exercised
// here against a real Postgres with its own control row:
//
//	no lease at all      → returned      (the defect: #8e2e1c0e, 245h idle on prod)
//	live lease           → NOT returned  (must not rob an agent mid-work)
//	expired lease        → NOT returned  (phase 1 owns that case; no double-move)
//	idle < grace         → NOT returned  (a just-released card is not stolen)
//	not in_progress      → NOT returned  (todo/backlog are already circulating)
//
// Every negative is a row that differs from the positive in exactly one field,
// so a predicate that silently stopped filtering would fail here rather than
// widen the sweep unnoticed.
//
// Mutation-verified 2026-08-27, and one result is worth recording because it is
// NOT what it looks like: removing `checked_out_by IS NULL` alone leaves this
// test GREEN, and so does removing `checkout_expires IS NULL` alone. The two are
// redundant — every writer in the codebase sets and clears the whole checkout
// quartet together, so a leased row always trips both and either guard alone
// suffices. Only dropping BOTH turns this test red, which is the honest control
// and the one that was run. Do not read the live-lease case as proof that one
// specific predicate is doing the work; it proves the conjunction is.
// Dropping the grace window, and dropping the in_progress filter, each go red on
// their own.
func TestFindStaleUnleasedInProgress_Predicates(t *testing.T) {
	db := testDB(t)
	repo := NewTaskRepo(db)
	ctx := context.Background()

	ws, proj, todoStatus := createTestProject(t, db)

	// checked_out_by carries an FK to agents, so the "live lease" control needs a
	// real holder — a random uuid is rejected outright.
	holder := uuid.New()
	_, err := db.ExecContext(ctx,
		`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix)
		 VALUES ($1, $2, 'Lease Holder', $3, 'not-a-real-hash', 'test')`,
		holder, ws.ID, "holder-"+holder.String()[:8])
	require.NoError(t, err)

	// createTestProject gives a todo status; add an in_progress one alongside it.
	inProg := &domain.TaskStatus{
		ID: uuid.New(), ProjectID: proj.ID, Name: "In Progress", Slug: "in-progress",
		Color: "#0000FF", Position: 1, Category: domain.StatusCategoryInProgress,
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO task_statuses (id, project_id, name, slug, color, position, category, is_default)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,false)`,
		inProg.ID, inProg.ProjectID, inProg.Name, inProg.Slug, inProg.Color, inProg.Position, inProg.Category)
	require.NoError(t, err)

	stale := time.Now().Add(-8 * time.Hour)
	fresh := time.Now().Add(-2 * time.Minute)
	past := time.Now().Add(-3 * time.Hour)
	future := time.Now().Add(90 * time.Minute)

	// mk inserts a task and then forces updated_at AND the checkout columns.
	//
	// The UPDATE is load-bearing, not tidiness: TaskRepo.Create's INSERT does not
	// name checked_out_by / checkout_expires at all, so setting them on the struct
	// leaves them NULL in the row. Building the "live lease" control that way makes
	// it a second unleased row wearing the label of a leased one — a control that
	// cannot fail, which is worse than no control. Caught by this test failing on
	// its first run.
	mk := func(statusID uuid.UUID, title string, by *uuid.UUID, exp *time.Time, updated time.Time) uuid.UUID {
		task := makeTestTask(proj.ID, statusID, title)
		require.NoError(t, repo.Create(ctx, task))
		_, err := db.ExecContext(ctx,
			`UPDATE tasks SET updated_at=$1, checked_out_by=$2, checkout_expires=$3 WHERE id=$4`,
			updated, by, exp, task.ID)
		require.NoError(t, err)

		// Assert the fixture actually took, so a future change to Create or to the
		// schema cannot quietly turn these control rows back into vacuous ones.
		var gotBy *uuid.UUID
		var gotExp *time.Time
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT checked_out_by, checkout_expires FROM tasks WHERE id=$1`, task.ID).Scan(&gotBy, &gotExp))
		require.Equal(t, by == nil, gotBy == nil, "fixture did not persist checked_out_by for %q", title)
		require.Equal(t, exp == nil, gotExp == nil, "fixture did not persist checkout_expires for %q", title)

		return task.ID
	}

	wantID := mk(inProg.ID, "unleased, idle — MUST be returned", nil, nil, stale)
	liveLeaseID := mk(inProg.ID, "live lease — agent is working", &holder, &future, stale)
	expiredLeaseID := mk(inProg.ID, "expired lease — phase 1 owns this", &holder, &past, stale)
	freshID := mk(inProg.ID, "unleased but only just released", nil, nil, fresh)
	todoID := mk(todoStatus.ID, "todo — already circulating", nil, nil, stale)

	got, err := repo.FindStaleUnleasedInProgress(ctx, 120*time.Minute)
	require.NoError(t, err)

	found := make(map[uuid.UUID]bool, len(got))
	for _, task := range got {
		found[task.ID] = true
	}

	require.True(t, found[wantID],
		"the unleased idle in_progress task was NOT returned — this is the defect the query exists to fix")
	require.False(t, found[liveLeaseID],
		"a task under a LIVE checkout was returned — the sweep would rob an agent mid-work")
	require.False(t, found[expiredLeaseID],
		"a task with an EXPIRED lease was returned — phase 1 already handles it, this would double-move")
	require.False(t, found[freshID],
		"a task idle for less than the grace window was returned — the grace parameter is not being applied")
	require.False(t, found[todoID],
		"a todo task was returned — the in_progress category filter is not being applied")
}

// The grace argument must actually reach the query. Same row, two windows: with a
// grace wider than the row's idle time it must disappear from the result. Without
// this, a hardcoded interval would satisfy the test above.
func TestFindStaleUnleasedInProgress_GraceIsApplied(t *testing.T) {
	db := testDB(t)
	repo := NewTaskRepo(db)
	ctx := context.Background()

	_, proj, _ := createTestProject(t, db)
	inProg := &domain.TaskStatus{
		ID: uuid.New(), ProjectID: proj.ID, Name: "In Progress", Slug: "in-progress",
		Color: "#0000FF", Position: 1, Category: domain.StatusCategoryInProgress,
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO task_statuses (id, project_id, name, slug, color, position, category, is_default)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,false)`,
		inProg.ID, inProg.ProjectID, inProg.Name, inProg.Slug, inProg.Color, inProg.Position, inProg.Category)
	require.NoError(t, err)

	task := makeTestTask(proj.ID, inProg.ID, "idle for three hours")
	require.NoError(t, repo.Create(ctx, task))
	_, err = db.ExecContext(ctx, `UPDATE tasks SET updated_at=$1 WHERE id=$2`, time.Now().Add(-3*time.Hour), task.ID)
	require.NoError(t, err)

	contains := func(d time.Duration) bool {
		got, err := repo.FindStaleUnleasedInProgress(ctx, d)
		require.NoError(t, err)
		for _, x := range got {
			if x.ID == task.ID {
				return true
			}
		}
		return false
	}

	require.True(t, contains(2*time.Hour), "idle 3h with a 2h grace should be returned")
	require.False(t, contains(4*time.Hour), "idle 3h with a 4h grace must NOT be returned — the grace argument is ignored")
}
