//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// insertAgent creates an agent of the fixture's workspace; task_waits.agent_id is a
// foreign key to it.
func (f *midPipelineFixture) insertAgent(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.db.ExecContext(context.Background(),
		`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix)
		 VALUES ($1, $2, 'waiter', $3, 'fixture', 'test')`, id, f.workspace, id.String())
	require.NoError(t, err)
	return id
}

// assign makes agentID the card's assignee: only the assignee's wait holds a card.
func (f *midPipelineFixture) assign(t *testing.T, taskID, agentID uuid.UUID) {
	t.Helper()
	_, err := f.db.ExecContext(context.Background(),
		`UPDATE tasks SET assignee_id = $2, assignee_type = 'agent' WHERE id = $1`, taskID, agentID)
	require.NoError(t, err)
}

// insertTimeWait records the row `wait_for on=time` writes: an active time wait of
// agent on taskID, declared 50 h ago, for an event `until` from now.
func (f *midPipelineFixture) insertTimeWait(t *testing.T, taskID, agent uuid.UUID, until time.Duration) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.db.ExecContext(context.Background(),
		`INSERT INTO task_waits (id, task_id, agent_id, kind, until_at, event_ref, evidence_source,
		                         evidence_why, until_at_offset, evidence_status, created_at)
		 VALUES ($1, $2, $3, 'time', now() + $4::interval, 'cba43bbb-replay', 'https://ci.example/schedule',
		         'the scheduled event fires then', 'Z', 'unverified', now() - interval '50 hours')`,
		id, taskID, agent, intervalArg(until))
	require.NoError(t, err)
	return id
}

// insertMRWait records the row `wait_for on=mr until=merged` writes.
func (f *midPipelineFixture) insertMRWait(t *testing.T, taskID, agent uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.db.ExecContext(context.Background(),
		`INSERT INTO task_waits (id, task_id, agent_id, kind, provider, project_path, mr_iid, until_state, created_at)
		 VALUES ($1, $2, $3, 'mr', 'gitlab', 'entire-vc/evc-mesh', 1187, 'merged', now() - interval '50 hours')`,
		id, taskID, agent)
	require.NoError(t, err)
	return id
}

func (f *midPipelineFixture) resolveWait(t *testing.T, waitID uuid.UUID) {
	t.Helper()
	_, err := f.db.ExecContext(context.Background(),
		`UPDATE task_waits SET resolved_at = now(), resolution = 'cancelled' WHERE id = $1`, waitID)
	require.NoError(t, err)
}

// An active task_waits row is what says a card waits; the runtime_state text is not.
//
// `wait_for on=time` wrote runtime_state.ref = "wait:<id>", which the sweep
// treats as kept by the wait. The Runner republishes runtime_state on every pass and
// rewrote the ref to "time". Its lane tick then hung for 22 min, the state's
// updated_at passed RuntimeStateStaleAfter, and the sweep parked the card in backlog
// with its wait still active: the wait was cancelled and the card started 10 h before
// its event.
func TestFindStaleUnleasedInProgress_ActiveWaitIsAuthoritative(t *testing.T) {
	f := newMidPipelineFixture(t)
	repo := NewTaskRepo(f.db)
	owner := f.insertAgent(t)
	other := f.insertAgent(t)

	// The Runner's last pass was before the 15 min window: the lane tick hung.
	hung := domain.RuntimeStateStaleAfter + 5*time.Minute
	ahead := 20 * time.Hour

	cases := []struct {
		name  string
		setup func(id uuid.UUID)
		swept bool
	}{
		// ── held: the assignee's wait is active, whatever the runner last wrote ──
		{"cba43bbb replay: time wait, ref rewritten to time, tick hung", func(id uuid.UUID) {
			f.assign(t, id, owner)
			f.insertTimeWait(t, id, owner, ahead)
			f.setRuntimeState(t, id, "waiting", "time", hung, &ahead)
		}, false},
		{"23fee43b replay: mr wait, ref rewritten to mr:, tick hung", func(id uuid.UUID) {
			f.assign(t, id, owner)
			f.insertMRWait(t, id, owner)
			f.setRuntimeState(t, id, "waiting", "mr:entire-vc/evc-mesh!1187", hung, nil)
		}, false},
		{"active wait, the runner cleared runtime_state", func(id uuid.UUID) {
			f.assign(t, id, owner)
			f.insertMRWait(t, id, owner)
		}, false},
		{"active wait, the runner left a stale working state", func(id uuid.UUID) {
			f.assign(t, id, owner)
			f.insertMRWait(t, id, owner)
			f.setRuntimeState(t, id, "working", "", hung, nil)
		}, false},
		{"time wait just past its deadline: the controller has the window to wake it", func(id uuid.UUID) {
			f.assign(t, id, owner)
			f.insertTimeWait(t, id, owner, -time.Minute)
		}, false},

		// ── swept: the same card without an active wait of its assignee ─────
		{"positive control: same card, no wait row", func(id uuid.UUID) {
			f.assign(t, id, owner)
			f.setRuntimeState(t, id, "waiting", "time", hung, &ahead)
		}, true},
		{"same card, its wait already ended", func(id uuid.UUID) {
			f.assign(t, id, owner)
			f.resolveWait(t, f.insertTimeWait(t, id, owner, ahead))
			f.setRuntimeState(t, id, "waiting", "time", hung, &ahead)
		}, true},
		{"active wait of an agent the card was taken from", func(id uuid.UUID) {
			f.assign(t, id, owner)
			f.insertMRWait(t, id, other)
			f.setRuntimeState(t, id, "waiting", "mr:entire-vc/evc-mesh!1187", hung, nil)
		}, true},
		{"active wait on an unassigned card", func(id uuid.UUID) {
			f.insertMRWait(t, id, owner)
		}, true},
		{"time wait long past its deadline: nobody acted on it", func(id uuid.UUID) {
			f.assign(t, id, owner)
			f.insertTimeWait(t, id, owner, -(domain.RuntimeStateStaleAfter + time.Hour))
		}, true},
	}

	ids := make(map[string]uuid.UUID, len(cases))
	for _, tc := range cases {
		id := f.insertUnleasedTask(t, 60*time.Hour)
		tc.setup(id)
		ids[tc.name] = id
	}

	got, err := repo.FindStaleUnleasedInProgress(context.Background(), 2*time.Hour)
	require.NoError(t, err)
	found := map[uuid.UUID]bool{}
	for _, task := range got {
		found[task.ID] = true
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.swept, found[ids[tc.name]])
		})
	}
}

// The read path reports the same thing the sweep decides: a waiting card with an
// active wait is not stale because its lane tick is late (criterion 4 of an internal ticket:
// list_tasks runtime_kind=waiting shows such a card with stale=false).
func TestRuntimeState_ActiveWaitIsNotStale(t *testing.T) {
	f := newMidPipelineFixture(t)
	repo := NewTaskRepo(f.db)
	ctx := context.Background()
	agent := f.insertAgent(t)

	hung := domain.RuntimeStateStaleAfter + 5*time.Minute
	ahead := 20 * time.Hour

	waiting := f.insertUnleasedTask(t, 60*time.Hour)
	f.assign(t, waiting, agent)
	f.insertTimeWait(t, waiting, agent, ahead)
	f.setRuntimeState(t, waiting, "waiting", "time", hung, &ahead)

	orphan := f.insertUnleasedTask(t, 60*time.Hour)
	f.assign(t, orphan, agent)
	f.setRuntimeState(t, orphan, "waiting", "time", hung, &ahead)

	ended := f.insertUnleasedTask(t, 60*time.Hour)
	f.assign(t, ended, agent)
	f.resolveWait(t, f.insertTimeWait(t, ended, agent, ahead))
	f.setRuntimeState(t, ended, "waiting", "time", hung, &ahead)

	taken := f.insertUnleasedTask(t, 60*time.Hour)
	f.assign(t, taken, agent)
	f.insertTimeWait(t, taken, f.insertAgent(t), ahead)
	f.setRuntimeState(t, taken, "waiting", "time", hung, &ahead)

	// Inside the deadline grace the sweep leaves the card alone, so the read must
	// not call it stale either; past the grace both give it up.
	justPast, longPast := -time.Minute, -(domain.RuntimeStateStaleAfter + time.Hour)
	grace := f.insertUnleasedTask(t, 60*time.Hour)
	f.assign(t, grace, agent)
	f.insertTimeWait(t, grace, agent, justPast)
	f.setRuntimeState(t, grace, "waiting", "time", hung, &justPast)

	expired := f.insertUnleasedTask(t, 60*time.Hour)
	f.assign(t, expired, agent)
	f.insertTimeWait(t, expired, agent, longPast)
	f.setRuntimeState(t, expired, "waiting", "time", hung, &longPast)

	staleOf := func(t *testing.T, task *domain.Task) bool {
		t.Helper()
		require.NotNil(t, task.RuntimeState)
		require.Equal(t, domain.RuntimeKindWaiting, task.RuntimeState.Kind)
		return task.RuntimeState.Stale
	}

	t.Run("GetByID", func(t *testing.T) {
		for id, wantStale := range map[uuid.UUID]bool{waiting: false, orphan: true, ended: true, taken: true, grace: false, expired: true} {
			task, err := repo.GetByID(ctx, id)
			require.NoError(t, err)
			require.Equal(t, wantStale, staleOf(t, task), "task %s", id)
		}
	})

	t.Run("List runtime_kind=waiting", func(t *testing.T) {
		kind := domain.RuntimeKindWaiting
		page, err := repo.List(ctx, f.project, repository.TaskFilter{RuntimeKind: &kind},
			pagination.Params{Page: 1, PageSize: 50})
		require.NoError(t, err)
		got := map[uuid.UUID]bool{}
		for i := range page.Items {
			got[page.Items[i].ID] = staleOf(t, &page.Items[i])
		}
		require.Equal(t, map[uuid.UUID]bool{waiting: false, orphan: true, ended: true, taken: true, grace: false, expired: true}, got)
	})
}
