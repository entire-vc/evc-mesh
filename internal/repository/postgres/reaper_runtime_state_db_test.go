//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// setRuntimeState writes tasks.runtime_state as the PUT handler stores it: kind,
// since, optional ref/until, and the server-stamped updated_at `refreshed` ago.
func (f *midPipelineFixture) setRuntimeState(t *testing.T, taskID uuid.UUID, kind, ref string, refreshed time.Duration, until *time.Duration) {
	t.Helper()
	now := time.Now().UTC()
	state := map[string]any{
		"kind":       kind,
		"since":      now.Add(-72 * time.Hour).Format(time.RFC3339Nano),
		"updated_at": now.Add(-refreshed).Format(time.RFC3339Nano),
		"lane":       "Developer",
	}
	if ref != "" {
		state["ref"] = ref
	}
	if until != nil {
		state["until"] = now.Add(*until).Format(time.RFC3339Nano)
	}
	raw, err := json.Marshal(state)
	require.NoError(t, err)
	_, err = f.db.ExecContext(context.Background(), `UPDATE tasks SET runtime_state = $2 WHERE id = $1`, taskID, raw)
	require.NoError(t, err)
}

// The controller's own statement decides whether an unleased in_progress card is
// waiting; comment text does not.
//
// A card waited on another card, its runner republished
// runtime_state{kind:"waiting"} every pass, and an informational comment from
// another lane became the card's latest comment. The sweep read only that latest
// comment, found no marker line, and parked the card in backlog with a due_date a
// day out: the standing wait was lost and a calendar wake invented. The same
// happened to any wait older than LiveWaitMarkerMaxAge, whatever it waited for.
func TestFindStaleUnleasedInProgress_LiveRuntimeStateIsAuthoritative(t *testing.T) {
	f := newMidPipelineFixture(t)
	repo := NewTaskRepo(f.db)

	fresh := time.Minute
	old := domain.RuntimeStateStaleAfter + 5*time.Minute
	past, future := -time.Hour, time.Hour
	fyi := "FYI: параллельно обновил зависимость, на вашу карточку не влияет"

	cases := []struct {
		name  string
		setup func(id uuid.UUID)
		swept bool
	}{
		// ── held: the controller says so, and its statement is live ──────────
		{"runtime wait replay: waiting, then an FYI comment from another lane becomes the latest", func(id uuid.UUID) {
			f.insertCommentAs(t, id, "agent", "готово к ревью\n⏳ WAIT card:#00000000", 30*time.Hour)
			f.insertCommentAs(t, id, "agent", fyi, 3*time.Hour)
			f.setRuntimeState(t, id, "waiting", "card:#00000000", fresh, nil)
		}, false},
		{"waiting for longer than the 48h marker cap", func(id uuid.UUID) {
			f.insertCommentAs(t, id, "agent", "⏳ WAIT mr:example/product!42", LiveWaitMarkerMaxAge+24*time.Hour)
			f.setRuntimeState(t, id, "waiting", "mr:example/product!42", fresh, nil)
		}, false},
		{"waiting with no comment at all", func(id uuid.UUID) {
			f.setRuntimeState(t, id, "waiting", "card:#00000000", fresh, nil)
		}, false},
		{"queued: wake condition met, waits for a slot", func(id uuid.UUID) {
			f.insertCommentAs(t, id, "agent", fyi, 3*time.Hour)
			f.setRuntimeState(t, id, "queued", "", fresh, nil)
		}, false},
		{"working: a turn runs between two checkouts", func(id uuid.UUID) {
			f.setRuntimeState(t, id, "working", "", fresh, nil)
		}, false},
		{"time wait whose deadline is still ahead", func(id uuid.UUID) {
			f.setRuntimeState(t, id, "waiting", "time", fresh, &future)
		}, false},
		{"state written from a declared wait is kept by the wait, not by refreshes", func(id uuid.UUID) {
			f.setRuntimeState(t, id, "waiting", domain.WaitRuntimeRefPrefix+uuid.NewString(), 40*time.Hour, nil)
		}, false},

		// ── swept: nobody live claims the card ──────────────────────────────
		{"no runtime state and no marker", func(id uuid.UUID) {
			f.insertCommentAs(t, id, "agent", fyi, 3*time.Hour)
		}, true},
		{"waiting, but the controller stopped refreshing it (runner gone)", func(id uuid.UUID) {
			f.insertCommentAs(t, id, "agent", fyi, 3*time.Hour)
			f.setRuntimeState(t, id, "waiting", "card:#00000000", old, nil)
		}, true},
		{"waiting past its own until", func(id uuid.UUID) {
			f.setRuntimeState(t, id, "waiting", "time", fresh, &past)
		}, true},
		{"declared-wait state past its own until", func(id uuid.UUID) {
			f.setRuntimeState(t, id, "waiting", domain.WaitRuntimeRefPrefix+uuid.NewString(), 40*time.Hour, &past)
		}, true},
		{"stopped: nobody is going to move it", func(id uuid.UUID) {
			f.setRuntimeState(t, id, "stopped", "", fresh, nil)
		}, true},
		{"waiting without a server stamp claims nothing", func(id uuid.UUID) {
			_, err := f.db.ExecContext(context.Background(),
				`UPDATE tasks SET runtime_state = '{"kind":"waiting","ref":"card:#00000000"}' WHERE id = $1`, id)
			require.NoError(t, err)
		}, true},

		// ── the text fallback still serves drivers that publish no state ─────
		{"no runtime state, live marker in the latest comment", func(id uuid.UUID) {
			f.insertCommentAs(t, id, "agent", "⏳ WAIT card:#00000000", 3*time.Hour)
		}, false},
		{"stale runtime state, live marker in the latest comment", func(id uuid.UUID) {
			f.insertCommentAs(t, id, "agent", "⏳ WAIT card:#00000000", 3*time.Hour)
			f.setRuntimeState(t, id, "waiting", "card:#00000000", old, nil)
		}, false},
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
