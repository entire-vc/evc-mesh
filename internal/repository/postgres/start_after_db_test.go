package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// What only the table and the row lock can guarantee for start_after: a date
// never lands without its origin, and compare-clear removes exactly the
// schedule that was read — not a newer one, not twice. Runs when a Postgres
// is reachable (deliveryOutcomeTestDB skips loudly otherwise).

type startAfterFixture struct {
	*typedOutcomeFixture
	repo  *TaskRepo
	agent context.Context
	human context.Context
}

func newStartAfterFixture(t *testing.T) *startAfterFixture {
	f := newTypedOutcomeFixture(t)
	return &startAfterFixture{
		typedOutcomeFixture: f, repo: NewTaskRepo(f.db),
		agent: actorctx.WithActor(context.Background(), f.agent, domain.ActorTypeAgent),
		human: actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeUser),
	}
}

func (f *startAfterFixture) get(t *testing.T) *domain.Task {
	t.Helper()
	task, err := f.repo.GetByID(context.Background(), f.task)
	require.NoError(t, err)
	require.NotNil(t, task)
	return task
}

// set writes a new date the way the service does after validation.
func (f *startAfterFixture) set(t *testing.T, ctx context.Context, at time.Time, origin string) *domain.Task {
	t.Helper()
	task := f.get(t)
	actor, kind := actorctx.FromContext(ctx)
	r := &domain.StartAfterReceipt{ID: uuid.New(), TaskID: task.ID, Op: "set", StartAfter: at, Origin: origin, ActorID: actor, ActorType: kind}
	if origin == domain.StartAfterAgentEvidence {
		s := func(v string) *string { return &v }
		r.SourceOffset, r.EventRef, r.Source = s("Z"), s("nightly-export"), s("https://ci.example/schedules/1")
		r.Why, r.EvidenceStatus = s("the export publishes at this time"), s(domain.TimeEvidenceUnverified)
	}
	task.StartAfter, task.StartAfterOrigin, task.StartAfterReceiptID, task.StartAfterSet = &at, &origin, &r.ID, r
	require.NoError(t, f.repo.Update(ctx, task))
	return f.get(t)
}

func clearOf(task *domain.Task, key string) domain.StartAfterCompareClear {
	at, v := *task.StartAfter, task.Version
	return domain.StartAfterCompareClear{
		ExpectedStartAfter: &at, ExpectedVersion: &v, ExpectedOrigin: task.StartAfterOrigin,
		ScheduleReceiptID: task.StartAfterReceiptID, IdempotencyKey: key,
	}
}

func (f *startAfterFixture) clears(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, f.db.Get(&n, `SELECT count(*) FROM start_after_receipts WHERE task_id = $1 AND op = 'clear'`, f.task))
	return n
}

func TestStartAfter_SetLeavesReceipt_DB(t *testing.T) {
	f := newStartAfterFixture(t)
	at := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Microsecond)
	task := f.set(t, f.agent, at, domain.StartAfterAgentEvidence)

	assert.Equal(t, domain.StartAfterAgentEvidence, *task.StartAfterOrigin)
	receipts, err := f.repo.ListStartAfterReceipts(context.Background(), f.task)
	require.NoError(t, err)
	require.Len(t, receipts, 1)
	assert.Equal(t, *task.StartAfterReceiptID, receipts[0].ID)
	assert.Equal(t, "nightly-export", *receipts[0].EventRef)
	assert.Equal(t, task.Version, *receipts[0].TaskVersion)

	// A writer that loaded the task without provenance (a list row) and saves
	// it unchanged keeps the stored origin and receipt.
	bare := f.get(t)
	bare.StartAfterOrigin, bare.StartAfterReceiptID = nil, nil
	bare.Title = "renamed"
	require.NoError(t, f.repo.Update(context.Background(), bare))
	after := f.get(t)
	assert.Equal(t, domain.StartAfterAgentEvidence, *after.StartAfterOrigin)
	assert.Equal(t, *task.StartAfterReceiptID, *after.StartAfterReceiptID)
}

func TestStartAfter_BypassRefusedByTable_DB(t *testing.T) {
	f := newStartAfterFixture(t)
	_, err := f.db.Exec(`UPDATE tasks SET start_after = now() + interval '1 day' WHERE id = $1`, f.task)
	require.Error(t, err, "a date without origin")
	assert.Contains(t, err.Error(), "tasks_start_after_origin_pair")

	_, err = f.db.Exec(`UPDATE tasks SET start_after = now() + interval '1 day', start_after_origin = 'agent_evidence' WHERE id = $1`, f.task)
	require.Error(t, err, "an agent date without receipt")
	assert.Contains(t, err.Error(), "tasks_start_after_agent_receipt")

	task := f.get(t)
	assert.Nil(t, task.StartAfter, "nothing was written")
}

// Receipts are history: neither the evidence of a set nor the record of a
// clear can be rewritten or removed while the task lives; deleting the task
// still takes them along.
func TestStartAfter_ReceiptsAppendOnly_DB(t *testing.T) {
	f := newStartAfterFixture(t)
	task := f.set(t, f.agent, time.Now().Add(48*time.Hour).UTC().Truncate(time.Microsecond), domain.StartAfterAgentEvidence)
	id := *task.StartAfterReceiptID

	_, err := f.db.Exec(`UPDATE start_after_receipts SET event_ref = 'rewritten' WHERE id = $1`, id)
	require.Error(t, err, "a receipt cannot be edited")
	assert.Contains(t, err.Error(), "start_after_receipts is append-only")
	_, err = f.db.Exec(`DELETE FROM start_after_receipts WHERE id = $1`, id)
	require.Error(t, err, "a receipt cannot be deleted while its task lives")
	assert.Contains(t, err.Error(), "start_after_receipts is append-only")

	_, err = f.db.Exec(`DELETE FROM tasks WHERE id = $1`, f.task)
	require.NoError(t, err, "deleting the task cascades to its receipts")
	var n int
	require.NoError(t, f.db.Get(&n, `SELECT count(*) FROM start_after_receipts WHERE task_id = $1`, f.task))
	assert.Zero(t, n)
}

// A person changes the date after the agent read it: the agent's stale clear
// is a conflict and the person's date stays.
func TestStartAfter_CompareClearAfterHumanChange_DB(t *testing.T) {
	f := newStartAfterFixture(t)
	read := f.set(t, f.agent, time.Now().Add(24*time.Hour).UTC().Truncate(time.Microsecond), domain.StartAfterAgentEvidence)
	humanAt := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Microsecond)
	f.set(t, f.human, humanAt, domain.StartAfterHumanExplicit)

	_, err := f.repo.CompareClearStartAfter(f.agent, f.task, clearOf(read, "k1"))
	var conflict *domain.StartAfterConflict
	require.ErrorAs(t, err, &conflict)
	assert.True(t, humanAt.Equal(*conflict.CurrentStartAfter))
	assert.Equal(t, domain.StartAfterHumanExplicit, *conflict.CurrentOrigin)

	now := f.get(t)
	assert.True(t, humanAt.Equal(*now.StartAfter), "the person's date is preserved")
	assert.Equal(t, 0, f.clears(t), "a conflict leaves no receipt")
}

// ABA: the same instant comes back as a new schedule; the old read must not
// clear it.
func TestStartAfter_CompareClearABA_DB(t *testing.T) {
	f := newStartAfterFixture(t)
	at := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Microsecond)
	read := f.set(t, f.agent, at, domain.StartAfterAgentEvidence)
	f.set(t, f.agent, at.Add(time.Hour), domain.StartAfterAgentEvidence)
	again := f.set(t, f.agent, at, domain.StartAfterAgentEvidence)
	require.True(t, at.Equal(*again.StartAfter))

	_, err := f.repo.CompareClearStartAfter(f.agent, f.task, clearOf(read, "k1"))
	var conflict *domain.StartAfterConflict
	require.ErrorAs(t, err, &conflict, "same instant, new version and receipt")
	assert.Equal(t, again.Version, conflict.CurrentVersion)

	// Only the version moved (an unrelated edit): still a conflict.
	stale := clearOf(again, "k2")
	edit := f.get(t)
	edit.Title = "touched"
	require.NoError(t, f.repo.Update(context.Background(), edit))
	_, err = f.repo.CompareClearStartAfter(f.agent, f.task, stale)
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, 0, f.clears(t))
}

func TestStartAfter_CompareClearParallel_DB(t *testing.T) {
	f := newStartAfterFixture(t)
	read := f.set(t, f.agent, time.Now().Add(24*time.Hour).UTC().Truncate(time.Microsecond), domain.StartAfterAgentEvidence)

	var wg sync.WaitGroup
	results := make([]error, 2)
	for i, key := range []string{"runner-a", "runner-b"} {
		wg.Add(1)
		go func(i int, key string) {
			defer wg.Done()
			_, results[i] = f.repo.CompareClearStartAfter(f.agent, f.task, clearOf(read, key))
		}(i, key)
	}
	wg.Wait()
	var ok, conflicts int
	for _, err := range results {
		var conflict *domain.StartAfterConflict
		switch {
		case err == nil:
			ok++
		case errors.As(err, &conflict):
			conflicts++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	assert.Equal(t, 1, ok)
	assert.Equal(t, 1, conflicts)
	assert.Equal(t, 1, f.clears(t), "two parallel clears, one audit receipt")
	assert.Nil(t, f.get(t).StartAfter)
}

func TestStartAfter_CompareClearIdempotent_DB(t *testing.T) {
	f := newStartAfterFixture(t)
	read := f.set(t, f.agent, time.Now().Add(24*time.Hour).UTC().Truncate(time.Microsecond), domain.StartAfterAgentEvidence)

	var wg sync.WaitGroup
	results := make([]*domain.StartAfterClearResult, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = f.repo.CompareClearStartAfter(f.agent, f.task, clearOf(read, "same-key"))
		}(i)
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	assert.Equal(t, results[0].Receipt.ID, results[1].Receipt.ID, "the repeat returns the first receipt")
	assert.True(t, results[0].Replayed != results[1].Replayed, "exactly one of them is the replay")
	assert.Equal(t, 1, f.clears(t))
	assert.Equal(t, *read.StartAfterReceiptID, *results[0].Receipt.ClearedReceiptID)
}

func TestStartAfter_AgentCannotClearHumanDate_DB(t *testing.T) {
	f := newStartAfterFixture(t)
	read := f.set(t, f.human, time.Now().Add(24*time.Hour).UTC().Truncate(time.Microsecond), domain.StartAfterHumanExplicit)
	_, err := f.repo.CompareClearStartAfter(f.agent, f.task, clearOf(read, "k1"))
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 403, apiErr.Code)
	assert.NotNil(t, f.get(t).StartAfter)

	res, err := f.repo.CompareClearStartAfter(f.human, f.task, clearOf(read, "k2"))
	require.NoError(t, err, "the person clears their own date")
	assert.Equal(t, domain.StartAfterHumanExplicit, res.Receipt.Origin)
}

func TestStartAfter_CompareClearLegacy_DB(t *testing.T) {
	f := newStartAfterFixture(t)
	_, err := f.db.Exec(`UPDATE tasks SET start_after = now() + interval '1 day', start_after_origin = 'legacy_unknown' WHERE id = $1`, f.task)
	require.NoError(t, err)
	read := f.get(t)
	require.Nil(t, read.StartAfterReceiptID)

	res, err := f.repo.CompareClearStartAfter(f.agent, f.task, clearOf(read, "legacy-1"))
	require.NoError(t, err)
	assert.Equal(t, domain.StartAfterLegacyUnknown, res.Receipt.Origin)
	assert.Nil(t, res.Receipt.ClearedReceiptID)
	after := f.get(t)
	assert.Nil(t, after.StartAfter)
	assert.Nil(t, after.StartAfterOrigin)

	// Already null: a new request is a conflict, never a second "cleared".
	_, err = f.repo.CompareClearStartAfter(f.agent, f.task, clearOf(read, "legacy-2"))
	var conflict *domain.StartAfterConflict
	require.ErrorAs(t, err, &conflict)
	assert.Nil(t, conflict.CurrentStartAfter)
}

func TestStartAfter_CreateWritesReceipt_DB(t *testing.T) {
	f := newStartAfterFixture(t)
	parent := f.get(t)
	at := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Microsecond)
	origin := domain.StartAfterHumanExplicit
	receipt := &domain.StartAfterReceipt{ID: uuid.New(), Op: "set", StartAfter: at, Origin: origin, ActorID: uuid.New(), ActorType: domain.ActorTypeUser}
	task := &domain.Task{ID: uuid.New(), ProjectID: parent.ProjectID, StatusID: parent.StatusID, Title: "scheduled", Priority: domain.PriorityMedium, AssigneeType: domain.AssigneeTypeUnassigned,
		CreatedBy: receipt.ActorID, CreatedByType: domain.ActorTypeUser, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		StartAfter: &at, StartAfterOrigin: &origin, StartAfterReceiptID: &receipt.ID, StartAfterSet: receipt}
	receipt.TaskID = task.ID
	require.NoError(t, f.repo.Create(context.Background(), task, nil))

	receipts, err := f.repo.ListStartAfterReceipts(context.Background(), task.ID)
	require.NoError(t, err)
	require.Len(t, receipts, 1, "the new card's date has its receipt")
	assert.Equal(t, receipt.ID, receipts[0].ID)
	got, err := f.repo.GetByID(context.Background(), task.ID)
	require.NoError(t, err)
	assert.Equal(t, receipt.ID, *got.StartAfterReceiptID)
}

// A Mesh receipt as evidence must exist in the workspace and must not be the
// writer's own comment.
func TestStartAfter_EvidenceSourceReceipt_DB(t *testing.T) {
	f := newStartAfterFixture(t)
	var ws uuid.UUID
	require.NoError(t, f.db.Get(&ws, `SELECT p.workspace_id FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.id = $1`, f.task))
	notes := NewTurnCommandRepo(f.db)
	own, err := notes.AddNote(context.Background(), f.task, f.typedOutcomeFixture.agent, domain.ActorTypeAgent, "the export is at 06:00")
	require.NoError(t, err)
	other, err := notes.AddNote(context.Background(), f.task, f.agent2, domain.ActorTypeAgent, "the export is at 06:00")
	require.NoError(t, err)

	code := func(err error) string {
		var oe *domain.OutcomeError
		if errors.As(err, &oe) {
			return oe.Code
		}
		return ""
	}
	assert.Equal(t, domain.CodeMissingEvidence, code(f.repo.CheckStartAfterEvidenceSource(context.Background(), ws, "comment:"+own.String(), f.typedOutcomeFixture.agent)), "own comment")
	assert.Equal(t, domain.CodeMissingEvidence, code(f.repo.CheckStartAfterEvidenceSource(context.Background(), uuid.New(), "comment:"+other.String(), f.typedOutcomeFixture.agent)), "another workspace")
	assert.Equal(t, domain.CodeMissingEvidence, code(f.repo.CheckStartAfterEvidenceSource(context.Background(), ws, "artifact:"+uuid.NewString(), f.typedOutcomeFixture.agent)), "no such artifact")
	assert.NoError(t, f.repo.CheckStartAfterEvidenceSource(context.Background(), ws, "comment:"+other.String(), f.typedOutcomeFixture.agent))
	assert.NoError(t, f.repo.CheckStartAfterEvidenceSource(context.Background(), ws, "https://ci.example/s", f.typedOutcomeFixture.agent))
}

func TestStartAfter_CompareClearNeedsActorAndTask_DB(t *testing.T) {
	f := newStartAfterFixture(t)
	read := f.set(t, f.agent, time.Now().Add(24*time.Hour).UTC().Truncate(time.Microsecond), domain.StartAfterAgentEvidence)
	_, err := f.repo.CompareClearStartAfter(context.Background(), f.task, clearOf(read, "k1"))
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 403, apiErr.Code, "an anonymous clear")

	_, err = f.repo.CompareClearStartAfter(f.agent, uuid.New(), clearOf(read, "k1"))
	require.Error(t, err, "a missing card")
	assert.NotNil(t, f.get(t).StartAfter)
}

// A person's PATCH null is audited like a compare-clear.
func TestStartAfter_HumanUpdateClearLeavesReceipt_DB(t *testing.T) {
	f := newStartAfterFixture(t)
	read := f.set(t, f.human, time.Now().Add(24*time.Hour).UTC().Truncate(time.Microsecond), domain.StartAfterHumanExplicit)
	actor, kind := actorctx.FromContext(f.human)
	key, reason := "patch:"+uuid.NewString(), "cleared by the person"
	task := f.get(t)
	task.StartAfterSet = &domain.StartAfterReceipt{ID: uuid.New(), TaskID: task.ID, Op: "clear", StartAfter: *read.StartAfter,
		Origin: domain.StartAfterHumanExplicit, ActorID: actor, ActorType: kind, ClearedReceiptID: read.StartAfterReceiptID,
		IdempotencyKey: &key, Reason: &reason}
	task.StartAfter, task.StartAfterOrigin, task.StartAfterReceiptID = nil, nil, nil
	require.NoError(t, f.repo.Update(f.human, task))

	after := f.get(t)
	assert.Nil(t, after.StartAfter)
	assert.Nil(t, after.StartAfterOrigin)
	assert.Nil(t, after.StartAfterReceiptID)
	assert.Equal(t, 1, f.clears(t))
	receipts, err := f.repo.ListStartAfterReceipts(context.Background(), f.task)
	require.NoError(t, err)
	last := receipts[len(receipts)-1]
	assert.Equal(t, "clear", last.Op)
	assert.Equal(t, *read.StartAfterReceiptID, *last.ClearedReceiptID)
	assert.Equal(t, after.Version, *last.TaskVersion)
}
