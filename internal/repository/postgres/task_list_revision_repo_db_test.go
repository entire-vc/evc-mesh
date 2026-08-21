//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// These tests prove the DB-trigger mechanism installed by migration
// 20260821004 (ADR-0004) independently of any Go code ever calling an
// "increment" method directly -- there is no such method (see
// repository.TaskListRevisionRepository's doc comment): the counter moves
// entirely because of AFTER INSERT/UPDATE/DELETE triggers on
// tasks/artifacts/vcs_links. Every test below writes through the ordinary
// repo methods the rest of the codebase already uses and observes the
// counter exclusively through TaskListRevisionRepo.GetRevision -- the same
// path subtask #6's handler will use.

func newTaskListRevisionTestRepo(t *testing.T) (*TaskListRevisionRepo, *TaskRepo) {
	t.Helper()
	db := testDB(t)
	return NewTaskListRevisionRepo(db), NewTaskRepo(db)
}

// makeTestTask builds a minimal valid domain.Task for a given project/status,
// ready for repo.Create.
func makeTestTask(projID, statusID uuid.UUID, title string) *domain.Task {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &domain.Task{
		ID:            uuid.New(),
		ProjectID:     projID,
		StatusID:      statusID,
		Title:         title,
		Description:   "task_list_revision trigger test",
		AssigneeType:  domain.AssigneeTypeUnassigned,
		Priority:      domain.PriorityMedium,
		Position:      1.0,
		CustomFields:  json.RawMessage(`{}`),
		Labels:        pq.StringArray{},
		CreatedBy:     uuid.New(),
		CreatedByType: domain.ActorTypeUser,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}

// ---------------------------------------------------------------------------
// AC #2 -- cross-project negative control (the "target vs. control row" ask).
// Mutations scoped to one project must never move a different project's
// counter. Two entirely separate projects (separate workspaces, even) stand
// in for "target" and "control" -- the strongest form of "different
// product/project" available in this schema.
// ---------------------------------------------------------------------------

func TestTaskListRevisionRepo_CrossProjectIsolation_NegativeControl(t *testing.T) {
	revRepo, taskRepo := newTaskListRevisionTestRepo(t)
	db := testDB(t)
	ctx := context.Background()

	_, targetProj, targetStatus := createTestProject(t, db)
	_, controlProj, controlStatus := createTestProject(t, db)

	// Before anything: neither project has a task_list_revisions row yet
	// (both projects were created AFTER this migration's backfill ran), so
	// GetRevision's zero-row default applies to both.
	targetBefore, err := revRepo.GetRevision(ctx, targetProj.ID)
	require.NoError(t, err)
	controlBefore, err := revRepo.GetRevision(ctx, controlProj.ID)
	require.NoError(t, err)
	t.Logf("BEFORE: target=%d control=%d", targetBefore, controlBefore)
	assert.Equal(t, int64(0), targetBefore)
	assert.Equal(t, int64(0), controlBefore)

	// Mutate ONLY the target project: create two tasks.
	require.NoError(t, taskRepo.Create(ctx, makeTestTask(targetProj.ID, targetStatus.ID, "target task 1")))
	require.NoError(t, taskRepo.Create(ctx, makeTestTask(targetProj.ID, targetStatus.ID, "target task 2")))

	targetAfterMutation, err := revRepo.GetRevision(ctx, targetProj.ID)
	require.NoError(t, err)
	controlAfterMutation, err := revRepo.GetRevision(ctx, controlProj.ID)
	require.NoError(t, err)
	t.Logf("AFTER target-only mutation: target=%d control=%d", targetAfterMutation, controlAfterMutation)

	assert.Equal(t, int64(2), targetAfterMutation, "target must have bumped once per task created in it")
	assert.Equal(t, controlBefore, controlAfterMutation, "control row must be byte-for-byte untouched by a different project's writes")

	// Now mutate ONLY the control project, to prove the isolation holds in
	// both directions, not just "control happens to start at 0 and stay 0".
	require.NoError(t, taskRepo.Create(ctx, makeTestTask(controlProj.ID, controlStatus.ID, "control task 1")))

	targetAfterControlMutation, err := revRepo.GetRevision(ctx, targetProj.ID)
	require.NoError(t, err)
	controlAfterControlMutation, err := revRepo.GetRevision(ctx, controlProj.ID)
	require.NoError(t, err)
	t.Logf("AFTER control-only mutation: target=%d control=%d", targetAfterControlMutation, controlAfterControlMutation)

	assert.Equal(t, targetAfterMutation, targetAfterControlMutation, "target must be untouched by control's own write")
	assert.Equal(t, int64(1), controlAfterControlMutation, "control must have bumped exactly once for its own task")
}

// ---------------------------------------------------------------------------
// AC #3 -- one test per mutation event named in ADR-0004 Decision 1.
// ---------------------------------------------------------------------------

// #1 task create.
func TestTaskListRevisionRepo_Bump_OnTaskCreate(t *testing.T) {
	revRepo, taskRepo := newTaskListRevisionTestRepo(t)
	db := testDB(t)
	ctx := context.Background()
	_, proj, status := createTestProject(t, db)

	before, err := revRepo.GetRevision(ctx, proj.ID)
	require.NoError(t, err)

	require.NoError(t, taskRepo.Create(ctx, makeTestTask(proj.ID, status.ID, "created task")))

	after, err := revRepo.GetRevision(ctx, proj.ID)
	require.NoError(t, err)
	t.Logf("task create: before=%d after=%d", before, after)
	assert.Equal(t, before+1, after)
}

// #2 task field update (title -- representative of the generic field-update path).
func TestTaskListRevisionRepo_Bump_OnTaskFieldUpdate(t *testing.T) {
	revRepo, taskRepo := newTaskListRevisionTestRepo(t)
	db := testDB(t)
	ctx := context.Background()
	_, proj, status := createTestProject(t, db)

	task := makeTestTask(proj.ID, status.ID, "before update")
	require.NoError(t, taskRepo.Create(ctx, task))

	before, err := revRepo.GetRevision(ctx, proj.ID)
	require.NoError(t, err)

	task.Title = "after update"
	task.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, taskRepo.Update(ctx, task))

	after, err := revRepo.GetRevision(ctx, proj.ID)
	require.NoError(t, err)
	t.Logf("task field update: before=%d after=%d", before, after)
	assert.Equal(t, before+1, after)
}

// #3 task move (status_id change).
func TestTaskListRevisionRepo_Bump_OnTaskMove(t *testing.T) {
	revRepo, taskRepo := newTaskListRevisionTestRepo(t)
	db := testDB(t)
	ctx := context.Background()
	_, proj, openStatus := createTestProject(t, db)

	doneStatus := &domain.TaskStatus{
		ID: uuid.New(), ProjectID: proj.ID, Name: "Done", Slug: "done",
		Color: "#0000FF", Position: 1, Category: domain.StatusCategoryDone,
		AutoTransition: json.RawMessage(`{}`),
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO task_statuses (id, project_id, name, slug, color, position, category, is_default, auto_transition) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		doneStatus.ID, doneStatus.ProjectID, doneStatus.Name, doneStatus.Slug, doneStatus.Color, doneStatus.Position, doneStatus.Category, false, doneStatus.AutoTransition,
	)
	require.NoError(t, err)

	task := makeTestTask(proj.ID, openStatus.ID, "moving task")
	require.NoError(t, taskRepo.Create(ctx, task))

	before, err := revRepo.GetRevision(ctx, proj.ID)
	require.NoError(t, err)

	task.StatusID = doneStatus.ID
	task.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, taskRepo.Update(ctx, task))

	after, err := revRepo.GetRevision(ctx, proj.ID)
	require.NoError(t, err)
	t.Logf("task move (status change): before=%d after=%d", before, after)
	assert.Equal(t, before+1, after)
}

// #4 task soft-delete.
func TestTaskListRevisionRepo_Bump_OnTaskDelete(t *testing.T) {
	revRepo, taskRepo := newTaskListRevisionTestRepo(t)
	db := testDB(t)
	ctx := context.Background()
	_, proj, status := createTestProject(t, db)

	task := makeTestTask(proj.ID, status.ID, "to be deleted")
	require.NoError(t, taskRepo.Create(ctx, task))

	before, err := revRepo.GetRevision(ctx, proj.ID)
	require.NoError(t, err)

	require.NoError(t, taskRepo.Delete(ctx, task.ID))

	after, err := revRepo.GetRevision(ctx, proj.ID)
	require.NoError(t, err)
	t.Logf("task soft-delete: before=%d after=%d", before, after)
	assert.Equal(t, before+1, after)

	// Confirm it really is a soft delete (deleted_at set, row still present)
	// and not a hard DELETE that the trigger would have missed.
	got, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Nil(t, got, "GetByID hides soft-deleted rows, but the row itself must still exist for the trigger to have fired on UPDATE")
	var deletedAt *time.Time
	require.NoError(t, db.GetContext(ctx, &deletedAt, `SELECT deleted_at FROM tasks WHERE id = $1`, task.ID))
	assert.NotNil(t, deletedAt, "soft-delete must be an UPDATE ... SET deleted_at, not a hard DELETE")
}

// #5 label attach/detach (Labels field via the same Update path).
func TestTaskListRevisionRepo_Bump_OnLabelAttachDetach(t *testing.T) {
	revRepo, taskRepo := newTaskListRevisionTestRepo(t)
	db := testDB(t)
	ctx := context.Background()
	_, proj, status := createTestProject(t, db)

	task := makeTestTask(proj.ID, status.ID, "label test task")
	task.Labels = pq.StringArray{}
	require.NoError(t, taskRepo.Create(ctx, task))

	beforeAttach, err := revRepo.GetRevision(ctx, proj.ID)
	require.NoError(t, err)

	// Attach.
	task.Labels = pq.StringArray{"urgent", "mesh"}
	task.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, taskRepo.Update(ctx, task))

	afterAttach, err := revRepo.GetRevision(ctx, proj.ID)
	require.NoError(t, err)
	t.Logf("label attach: before=%d after=%d", beforeAttach, afterAttach)
	assert.Equal(t, beforeAttach+1, afterAttach)

	// Detach.
	task.Labels = pq.StringArray{"mesh"}
	task.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, taskRepo.Update(ctx, task))

	afterDetach, err := revRepo.GetRevision(ctx, proj.ID)
	require.NoError(t, err)
	t.Logf("label detach: before=%d after=%d", afterAttach, afterDetach)
	assert.Equal(t, afterAttach+1, afterDetach)
}

// #8/#9 artifact create/delete.
func TestTaskListRevisionRepo_Bump_OnArtifactCreateAndDelete(t *testing.T) {
	revRepo, taskRepo := newTaskListRevisionTestRepo(t)
	db := testDB(t)
	ctx := context.Background()
	_, proj, status := createTestProject(t, db)
	artifactRepo := NewArtifactRepo(db)

	task := makeTestTask(proj.ID, status.ID, "artifact host task")
	require.NoError(t, taskRepo.Create(ctx, task))

	beforeCreate, err := revRepo.GetRevision(ctx, proj.ID)
	require.NoError(t, err)

	artifact := &domain.Artifact{
		ID: uuid.New(), TaskID: task.ID, Name: "report.txt",
		ArtifactType: domain.ArtifactTypeReport, MimeType: "text/plain",
		StorageKey: "test/report.txt", SizeBytes: 42, ChecksumSHA256: "deadbeef",
		Metadata: json.RawMessage(`{}`), UploadedBy: uuid.New(),
		UploadedByType: domain.UploaderTypeAgent, CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	require.NoError(t, artifactRepo.Create(ctx, artifact))

	afterCreate, err := revRepo.GetRevision(ctx, proj.ID)
	require.NoError(t, err)
	t.Logf("artifact create: before=%d after=%d", beforeCreate, afterCreate)
	assert.Equal(t, beforeCreate+1, afterCreate, "artifact INSERT has no write to tasks -- needs its own trigger")

	require.NoError(t, artifactRepo.Delete(ctx, artifact.ID))

	afterDelete, err := revRepo.GetRevision(ctx, proj.ID)
	require.NoError(t, err)
	t.Logf("artifact delete: before=%d after=%d", afterCreate, afterDelete)
	assert.Equal(t, afterCreate+1, afterDelete)
}

// #11 vcs_link create/delete.
func TestTaskListRevisionRepo_Bump_OnVCSLinkCreateAndDelete(t *testing.T) {
	revRepo, taskRepo := newTaskListRevisionTestRepo(t)
	db := testDB(t)
	ctx := context.Background()
	_, proj, status := createTestProject(t, db)
	vcsRepo := NewVCSLinkRepo(db)

	task := makeTestTask(proj.ID, status.ID, "vcs link host task")
	require.NoError(t, taskRepo.Create(ctx, task))

	beforeCreate, err := revRepo.GetRevision(ctx, proj.ID)
	require.NoError(t, err)

	link := &domain.VCSLink{
		ID: uuid.New(), TaskID: task.ID, Provider: domain.VCSProviderGitHub,
		LinkType: domain.VCSLinkTypePR, ExternalID: "123",
		URL: "https://github.com/entire-vc/evc-mesh/pull/123", Title: "test PR",
		Status: domain.VCSLinkStatusOpen, Metadata: json.RawMessage(`{}`),
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	require.NoError(t, vcsRepo.Create(ctx, link))

	afterCreate, err := revRepo.GetRevision(ctx, proj.ID)
	require.NoError(t, err)
	t.Logf("vcs_link create: before=%d after=%d", beforeCreate, afterCreate)
	assert.Equal(t, beforeCreate+1, afterCreate, "vcs_link INSERT has no write to tasks -- needs its own trigger")

	require.NoError(t, vcsRepo.Delete(ctx, link.ID))

	afterDelete, err := revRepo.GetRevision(ctx, proj.ID)
	require.NoError(t, err)
	t.Logf("vcs_link delete: before=%d after=%d", afterCreate, afterDelete)
	assert.Equal(t, afterCreate+1, afterDelete)
}

// #12 comment add/edit -- ADR Decision 1 explicitly EXCLUDES this (a
// list_tasks row carries no comment-derived field, so a comment can never
// change what any list_tasks caller sees). This is a deliberate NEGATIVE
// control proving the trigger set does NOT fire on comments -- if it did,
// every comment in a project would invalidate every open cursor in that
// project for zero payoff, which is exactly what the ADR argues against.
func TestTaskListRevisionRepo_NoBump_OnCommentAddOrEdit(t *testing.T) {
	revRepo, taskRepo := newTaskListRevisionTestRepo(t)
	db := testDB(t)
	ctx := context.Background()
	_, proj, status := createTestProject(t, db)
	commentRepo := NewCommentRepo(db)

	task := makeTestTask(proj.ID, status.ID, "comment host task")
	require.NoError(t, taskRepo.Create(ctx, task))

	beforeComment, err := revRepo.GetRevision(ctx, proj.ID)
	require.NoError(t, err)

	comment := &domain.Comment{
		ID: uuid.New(), TaskID: task.ID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeUser,
		Body: "first comment", Metadata: json.RawMessage(`{}`),
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond), UpdatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	require.NoError(t, commentRepo.Create(ctx, comment))

	afterCreate, err := revRepo.GetRevision(ctx, proj.ID)
	require.NoError(t, err)
	t.Logf("comment add (must NOT bump): before=%d after=%d", beforeComment, afterCreate)
	assert.Equal(t, beforeComment, afterCreate, "ADR-0004 Decision 1 row #12: comment add must NOT bump the counter")

	comment.Body = "edited comment"
	comment.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, commentRepo.Update(ctx, comment))

	afterEdit, err := revRepo.GetRevision(ctx, proj.ID)
	require.NoError(t, err)
	t.Logf("comment edit (must NOT bump): before=%d after=%d", afterCreate, afterEdit)
	assert.Equal(t, afterCreate, afterEdit, "ADR-0004 Decision 1 row #12: comment edit must NOT bump the counter")
}

// Note on ADR row #6 (label RENAME): no such operation exists anywhere in
// this codebase today (grep -rn "RenameLabel" -> nothing, confirmed again
// here as of this migration). There is nothing to seed a test against. The
// ADR documents that if one is ever added it will be a bulk UPDATE tasks
// over many rows in one project and is automatically covered by
// trg_tasks_bump_task_list_revision with no new trigger code -- this is
// asserted by design (the trigger fires on ANY UPDATE OF tasks, not on a
// specific column list), not by a runnable test against a feature that does
// not exist.
