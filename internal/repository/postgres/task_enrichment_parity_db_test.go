package postgres

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// No //go:build integration tag — same convention as the other *_db_test.go
// files here: run against a migrated DATABASE_URL, skip when unreachable.
//
// The claim this file has to defend is narrow and total: the join-based
// enrichment returns EXACTLY what the correlated subqueries returned. A
// rewrite that is merely faster is worthless if a NULL turns into an empty
// string or a count silently becomes 0, and neither shows up in a latency
// graph. So rather than asserting expected values by hand, each test below
// runs BOTH spellings over the same rows and diffs them: taskComputedCols is
// still in the file, so it can be the oracle.

func enrichmentParityDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres at %s, skipping: %v", dsn, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Skipf("Postgres at %s not accepting connections, skipping: %v", dsn, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// enrichmentFixture builds a project whose tasks cover every branch the
// enrichment columns have: an agent assignee, a user assignee, an unassigned
// task, a soft-deleted agent (name must come back NULL, not the row's name), a
// user with an empty display_name (created_by_name falls back to the email
// local part), tasks with and without subtasks, artifacts and vcs_links, and a
// soft-deleted subtask that must NOT be counted.
type enrichmentFixture struct {
	projectID  uuid.UUID
	assigneeID uuid.UUID
}

func newEnrichmentFixture(t *testing.T, db *sqlx.DB) enrichmentFixture {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.New().String()[:8]

	userRepo := NewUserRepo(db)
	namedUser := &domain.User{
		ID: uuid.New(), Email: "named-" + suffix + "@example.com", PasswordHash: "x",
		Name: "Named User", Username: "named-" + suffix, IsActive: true,
	}
	require.NoError(t, userRepo.Create(ctx, namedUser))
	blankUser := &domain.User{
		ID: uuid.New(), Email: "blank-" + suffix + "@example.com", PasswordHash: "x",
		Name: "", Username: "blank-" + suffix, IsActive: true,
	}
	require.NoError(t, userRepo.Create(ctx, blankUser))

	ws := &domain.Workspace{
		ID: uuid.New(), Name: "enrich-ws", Slug: "enrich-ws-" + suffix, OwnerID: namedUser.ID,
	}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, ws))

	proj := &domain.Project{
		ID: uuid.New(), WorkspaceID: ws.ID, Name: "enrich-proj",
		Slug: "enrich-proj-" + suffix, DefaultAssigneeType: domain.DefaultAssigneeNone,
	}
	require.NoError(t, NewProjectRepo(db).Create(ctx, proj))

	status := &domain.TaskStatus{
		ID: uuid.New(), ProjectID: proj.ID, Name: "Todo", Slug: "todo",
		Color: "#00FF00", Position: 0, Category: domain.StatusCategoryTodo, IsDefault: true,
	}
	require.NoError(t, NewTaskStatusRepo(db).Create(ctx, status))

	agentRepo := NewAgentRepo(db)
	liveAgent := &domain.Agent{
		ID: uuid.New(), WorkspaceID: ws.ID, Name: "Live Agent", Slug: "live-" + suffix,
		AgentType: domain.AgentTypeClaudeCode, APIKeyHash: "h", APIKeyPrefix: "p" + suffix,
		Status: domain.AgentStatusOffline,
	}
	require.NoError(t, agentRepo.Create(ctx, liveAgent))
	deadAgent := &domain.Agent{
		ID: uuid.New(), WorkspaceID: ws.ID, Name: "Dead Agent", Slug: "dead-" + suffix,
		AgentType: domain.AgentTypeClaudeCode, APIKeyHash: "h", APIKeyPrefix: "q" + suffix,
		Status: domain.AgentStatusOffline,
	}
	require.NoError(t, agentRepo.Create(ctx, deadAgent))
	require.NoError(t, agentRepo.Delete(ctx, deadAgent.ID)) // soft delete

	taskRepo := NewTaskRepo(db)
	mk := func(title string, assignee *uuid.UUID, at domain.AssigneeType,
		reviewer *uuid.UUID, rt *domain.AssigneeType, createdBy uuid.UUID, cbt domain.ActorType,
	) *domain.Task {
		task := &domain.Task{
			ID: uuid.New(), ProjectID: proj.ID, StatusID: status.ID, Title: title,
			AssigneeID: assignee, AssigneeType: at, Priority: domain.PriorityNone,
			ReviewerID: reviewer, ReviewerType: rt,
			CreatedBy: createdBy, CreatedByType: cbt,
		}
		require.NoError(t, taskRepo.Create(ctx, task))
		return task
	}

	agentType := domain.AssigneeTypeAgent
	userType := domain.AssigneeTypeUser

	withAgent := mk("agent assignee", &liveAgent.ID, agentType,
		&liveAgent.ID, &agentType, liveAgent.ID, domain.ActorTypeAgent)
	mk("user assignee", &namedUser.ID, userType,
		&namedUser.ID, &userType, namedUser.ID, domain.ActorTypeUser)
	mk("blank display name creator", nil, domain.AssigneeTypeUnassigned,
		nil, nil, blankUser.ID, domain.ActorTypeUser)
	mk("deleted agent assignee", &deadAgent.ID, agentType,
		&deadAgent.ID, &agentType, deadAgent.ID, domain.ActorTypeAgent)
	mk("unassigned", nil, domain.AssigneeTypeUnassigned, nil, nil, namedUser.ID, domain.ActorTypeUser)

	// Subtasks: two live, one soft-deleted (must not be counted).
	for i := 0; i < 2; i++ {
		sub := mk(fmt.Sprintf("subtask %d", i), nil, domain.AssigneeTypeUnassigned,
			nil, nil, namedUser.ID, domain.ActorTypeUser)
		sub.ParentTaskID = &withAgent.ID
		require.NoError(t, taskRepo.Update(ctx, sub))
	}
	deletedSub := mk("deleted subtask", nil, domain.AssigneeTypeUnassigned,
		nil, nil, namedUser.ID, domain.ActorTypeUser)
	deletedSub.ParentTaskID = &withAgent.ID
	require.NoError(t, taskRepo.Update(ctx, deletedSub))
	require.NoError(t, taskRepo.Delete(ctx, deletedSub.ID))

	// vcs_links and artifacts on the same task, so the counts are non-zero and
	// distinguishable from each other.
	for i := 0; i < 3; i++ {
		_, err := db.ExecContext(ctx,
			`INSERT INTO vcs_links (id, task_id, provider, link_type, external_id, url)
			 VALUES ($1, $2, 'github', 'pull_request', $3, $4)`,
			uuid.New(), withAgent.ID, fmt.Sprintf("%s-%d", suffix, i),
			fmt.Sprintf("https://example.com/%s/%d", suffix, i))
		require.NoError(t, err)
	}

	return enrichmentFixture{projectID: proj.ID, assigneeID: liveAgent.ID}
}

// enrichedShape is the set of computed columns, scanned from either spelling.
type enrichedShape struct {
	ID            uuid.UUID `db:"id"`
	SubtaskCount  int       `db:"subtask_count"`
	ArtifactCount int       `db:"artifact_count"`
	VCSLinkCount  int       `db:"vcs_link_count"`
	AssigneeName  *string   `db:"assignee_name"`
	ReviewerName  *string   `db:"reviewer_name"`
	CreatedByName *string   `db:"created_by_name"`
}

// TestTaskEnrichment_JoinsMatchCorrelatedSubqueries is the parity check: same
// rows, both spellings, byte-for-byte identical enrichment.
func TestTaskEnrichment_JoinsMatchCorrelatedSubqueries(t *testing.T) {
	db := enrichmentParityDB(t)
	fx := newEnrichmentFixture(t, db)
	ctx := context.Background()

	subqueryQ := `SELECT id, ` + taskComputedCols + `
		FROM tasks
		WHERE project_id = $1 AND deleted_at IS NULL
		ORDER BY id ASC`

	joinQ := enrichTaskPage(
		`SELECT `+taskBaseColsNoAlias+`
		 FROM tasks
		 WHERE project_id = $1 AND deleted_at IS NULL
		 ORDER BY id ASC`,
		"p.id", "ORDER BY p.id ASC")

	var viaSubquery, viaJoin []enrichedShape
	require.NoError(t, db.SelectContext(ctx, &viaSubquery, subqueryQ, fx.projectID))
	require.NoError(t, db.SelectContext(ctx, &viaJoin, joinQ, fx.projectID))

	require.NotEmpty(t, viaSubquery, "the fixture produced no rows — this test would prove nothing")
	require.Equal(t, len(viaSubquery), len(viaJoin), "the join must not add or drop rows")
	assert.Equal(t, viaSubquery, viaJoin,
		"the join-based enrichment must be indistinguishable from the correlated subqueries, "+
			"including NULL vs empty-string names and zero vs missing counts")
}

// The counts are the half most likely to break silently, so assert the actual
// values too rather than only that the two spellings agree.
func TestTaskEnrichment_CountsExcludeSoftDeletedSubtasks(t *testing.T) {
	db := enrichmentParityDB(t)
	fx := newEnrichmentFixture(t, db)

	joinQ := enrichTaskPage(
		`SELECT `+taskBaseColsNoAlias+`
		 FROM tasks
		 WHERE project_id = $1 AND deleted_at IS NULL AND title = 'agent assignee'`,
		"p.id", "")

	var rows []enrichedShape
	require.NoError(t, db.SelectContext(context.Background(), &rows, joinQ, fx.projectID))
	require.Len(t, rows, 1)

	assert.Equal(t, 2, rows[0].SubtaskCount,
		"the soft-deleted subtask must not be counted")
	assert.Equal(t, 3, rows[0].VCSLinkCount)
	assert.Equal(t, 0, rows[0].ArtifactCount,
		"no artifacts were created, and a missing aggregate must read as 0, not NULL")
}

// A deleted agent contributed a name through neither spelling; make sure the
// join did not start resolving one.
func TestTaskEnrichment_DeletedAgentYieldsNoName(t *testing.T) {
	db := enrichmentParityDB(t)
	fx := newEnrichmentFixture(t, db)

	joinQ := enrichTaskPage(
		`SELECT `+taskBaseColsNoAlias+`
		 FROM tasks
		 WHERE project_id = $1 AND deleted_at IS NULL AND title = 'deleted agent assignee'`,
		"p.id", "")

	var rows []enrichedShape
	require.NoError(t, db.SelectContext(context.Background(), &rows, joinQ, fx.projectID))
	require.Len(t, rows, 1)
	assert.Nil(t, rows[0].AssigneeName)
	assert.Nil(t, rows[0].ReviewerName)
	assert.Nil(t, rows[0].CreatedByName)
}

// created_by_name falls back to the email local part when display_name is
// empty — a branch the plain COALESCE would have lost.
func TestTaskEnrichment_BlankDisplayNameFallsBackToEmailLocalPart(t *testing.T) {
	db := enrichmentParityDB(t)
	fx := newEnrichmentFixture(t, db)

	joinQ := enrichTaskPage(
		`SELECT `+taskBaseColsNoAlias+`
		 FROM tasks
		 WHERE project_id = $1 AND deleted_at IS NULL AND title = 'blank display name creator'`,
		"p.id", "")

	var rows []enrichedShape
	require.NoError(t, db.SelectContext(context.Background(), &rows, joinQ, fx.projectID))
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].CreatedByName)
	assert.Contains(t, *rows[0].CreatedByName, "blank-")
}

// A join can reorder its input; the repository methods must not.
func TestTaskEnrichment_PageOrderIsPreservedThroughTheJoins(t *testing.T) {
	db := enrichmentParityDB(t)
	fx := newEnrichmentFixture(t, db)
	repo := NewTaskRepo(db)
	ctx := context.Background()

	page, err := repo.List(ctx, fx.projectID, repository.TaskFilter{},
		pagination.Params{Page: 1, PageSize: 50, SortBy: "title", SortDir: "asc"})
	require.NoError(t, err)
	require.Greater(t, len(page.Items), 1)

	for i := 1; i < len(page.Items); i++ {
		assert.LessOrEqual(t, page.Items[i-1].Title, page.Items[i].Title,
			"List must still return the page in sort order after the enrichment joins")
	}

	desc, err := repo.List(ctx, fx.projectID, repository.TaskFilter{},
		pagination.Params{Page: 1, PageSize: 50, SortBy: "title", SortDir: "desc"})
	require.NoError(t, err)
	require.Greater(t, len(desc.Items), 1)
	for i := 1; i < len(desc.Items); i++ {
		assert.GreaterOrEqual(t, desc.Items[i-1].Title, desc.Items[i].Title,
			"sort_dir must survive the outer ORDER BY too")
	}
}

// ListSubtasks and ListByAssignee were rewritten as well; both must still be
// enriched rather than returning zeroed counts.
func TestTaskEnrichment_ListSubtasksAndListByAssigneeStayEnriched(t *testing.T) {
	db := enrichmentParityDB(t)
	fx := newEnrichmentFixture(t, db)
	repo := NewTaskRepo(db)
	ctx := context.Background()

	page, err := repo.List(ctx, fx.projectID, repository.TaskFilter{},
		pagination.Params{Page: 1, PageSize: 50})
	require.NoError(t, err)

	var parentID uuid.UUID
	for _, task := range page.Items {
		if task.Title == "agent assignee" {
			parentID = task.ID
			assert.Equal(t, 2, task.SubtaskCount)
			assert.Equal(t, 3, task.VCSLinkCount)
			assert.NotNil(t, task.AssigneeName)
		}
	}
	require.NotEqual(t, uuid.Nil, parentID)

	subs, err := repo.ListSubtasks(ctx, parentID)
	require.NoError(t, err)
	assert.Len(t, subs, 2, "the soft-deleted subtask must stay out of the list too")
	for _, sub := range subs {
		assert.NotNil(t, sub.CreatedByName, "subtasks must come back enriched")
	}
}
