package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// Readers on the tables that hang off a task or a project's board —
// task_statuses, task_dependencies, task_templates, artifacts, vcs_links.
// Every query below was rewritten from `SELECT *` (bare or `a.*`-qualified) by
// this change and had no coverage at this package's level.
//
// See select_star_coverage_fixtures_db_test.go for why these assert on field
// values rather than on row counts.

// ---------------------------------------------------------------------------
// task_status_repo.go: GetByID, ListByProject, GetDefaultForProject
// ---------------------------------------------------------------------------

func TestTaskStatusRepoGetByID_ReturnsEveryListedColumn(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)

	got, err := NewTaskStatusRepo(db).GetByID(context.Background(), fx.statusID)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, fx.statusID, got.ID)
	assert.Equal(t, fx.projectID, got.ProjectID)
	assert.Equal(t, "Coverage Todo", got.Name)
	assert.Equal(t, "cover-todo", got.Slug)
	assert.Equal(t, "#1A2B3C", got.Color,
		"color is seeded to a non-default value precisely so a dropped column shows up")
	assert.Equal(t, 0, got.Position)
	assert.Equal(t, domain.StatusCategoryTodo, got.Category)
	assert.True(t, got.IsDefault)
	assert.JSONEq(t, `{}`, string(got.AutoTransition))
}

func TestTaskStatusRepoListByProject_OrdersByPositionAndPopulates(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)

	repo := NewTaskStatusRepo(db)
	// A second, non-default status at a later position.
	done := &domain.TaskStatus{
		ID: uuid.New(), ProjectID: fx.projectID, Name: "Coverage Done", Slug: "cover-done",
		Color: "#9F8E7D", Position: 7, Category: domain.StatusCategoryDone,
		AutoTransition: json.RawMessage(`{"on_merge": true}`),
	}
	require.NoError(t, repo.Create(context.Background(), done))

	statuses, err := repo.ListByProject(context.Background(), fx.projectID)
	require.NoError(t, err)
	require.Len(t, statuses, 2)

	assert.Equal(t, fx.statusID, statuses[0].ID, "ORDER BY position ASC puts position 0 first")
	assert.Equal(t, "#1A2B3C", statuses[0].Color)
	assert.True(t, statuses[0].IsDefault)

	assert.Equal(t, done.ID, statuses[1].ID)
	assert.Equal(t, "Coverage Done", statuses[1].Name)
	assert.Equal(t, "#9F8E7D", statuses[1].Color)
	assert.Equal(t, 7, statuses[1].Position)
	assert.Equal(t, domain.StatusCategoryDone, statuses[1].Category)
	assert.False(t, statuses[1].IsDefault)
	assert.JSONEq(t, `{"on_merge": true}`, string(statuses[1].AutoTransition),
		"auto_transition is the newest column on this table and the easiest to omit")
}

func TestTaskStatusRepoGetDefaultForProject_PicksTheDefaultRow(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)

	repo := NewTaskStatusRepo(db)
	require.NoError(t, repo.Create(context.Background(), &domain.TaskStatus{
		ID: uuid.New(), ProjectID: fx.projectID, Name: "Not Default", Slug: "cover-nondefault",
		Color: "#ABCDEF", Position: 3, Category: domain.StatusCategoryReview, IsDefault: false,
	}))

	got, err := repo.GetDefaultForProject(context.Background(), fx.projectID)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, fx.statusID, got.ID, "only the fixture's status carries is_default = TRUE")
	assert.Equal(t, "Coverage Todo", got.Name)
	assert.Equal(t, "#1A2B3C", got.Color)
	assert.True(t, got.IsDefault)
}

func TestTaskStatusRepoGetDefaultForProject_NoDefaultIsNilNotAnError(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)

	// A project with statuses but none flagged default.
	proj := &domain.Project{
		ID: uuid.New(), WorkspaceID: fx.workspaceID, Name: "no-default",
		Slug: "cover-nodefault-" + fx.suffix, DefaultAssigneeType: domain.DefaultAssigneeNone,
	}
	require.NoError(t, NewProjectRepo(db).Create(context.Background(), proj))

	got, err := NewTaskStatusRepo(db).GetDefaultForProject(context.Background(), proj.ID)
	require.NoError(t, err)
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// task_dependency_repo.go: ListByTask, ListDependents
// ---------------------------------------------------------------------------

func TestTaskDependencyRepoListByTask_ReturnsEveryListedColumn(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	blocker := newCoverageTask(t, db, fx, "the blocker")

	dep := &domain.TaskDependency{
		ID:              uuid.New(),
		TaskID:          fx.taskID,
		DependsOnTaskID: blocker,
		DependencyType:  domain.DependencyTypeBlocks,
		CreatedAt:       time.Now(),
	}
	require.NoError(t, NewTaskDependencyRepo(db).Create(context.Background(), dep))

	deps, err := NewTaskDependencyRepo(db).ListByTask(context.Background(), fx.taskID)
	require.NoError(t, err)
	require.Len(t, deps, 1)

	assert.Equal(t, dep.ID, deps[0].ID)
	assert.Equal(t, fx.taskID, deps[0].TaskID)
	assert.Equal(t, blocker, deps[0].DependsOnTaskID,
		"depends_on_task_id is the whole point of the row; dropping it reads as uuid.Nil")
	assert.Equal(t, domain.DependencyTypeBlocks, deps[0].DependencyType)
	assert.False(t, deps[0].CreatedAt.IsZero())
}

func TestTaskDependencyRepoListDependents_LooksTheOtherWayDownTheEdge(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	blocker := newCoverageTask(t, db, fx, "the blocker")

	dep := &domain.TaskDependency{
		ID:              uuid.New(),
		TaskID:          fx.taskID,
		DependsOnTaskID: blocker,
		DependencyType:  domain.DependencyTypeRelatesTo,
		CreatedAt:       time.Now(),
	}
	require.NoError(t, NewTaskDependencyRepo(db).Create(context.Background(), dep))

	repo := NewTaskDependencyRepo(db)

	// ListDependents(blocker) finds the row; ListByTask(blocker) must not —
	// the two queries differ only in which column they key on.
	dependents, err := repo.ListDependents(context.Background(), blocker)
	require.NoError(t, err)
	require.Len(t, dependents, 1)
	assert.Equal(t, dep.ID, dependents[0].ID)
	assert.Equal(t, fx.taskID, dependents[0].TaskID)
	assert.Equal(t, domain.DependencyTypeRelatesTo, dependents[0].DependencyType)

	none, err := repo.ListByTask(context.Background(), blocker)
	require.NoError(t, err)
	assert.Empty(t, none, "the blocker depends on nothing; the two directions must not collapse")
}

// ---------------------------------------------------------------------------
// task_template_repo.go: GetByID, List
// ---------------------------------------------------------------------------

// seedTaskTemplate writes one task_templates row with every nullable column
// populated, so an omitted column reads as nil rather than as a plausible value.
func seedTaskTemplate(t *testing.T, db *sqlx.DB, fx coverageFixture, name string) *domain.TaskTemplate {
	t.Helper()
	hours := 4.5
	assigneeType := domain.AssigneeTypeAgent
	tmpl := &domain.TaskTemplate{
		ID:                  uuid.New(),
		ProjectID:           fx.projectID,
		Name:                name,
		Description:         "description for " + name,
		TitleTemplate:       "[{{.Sprint}}] " + name,
		DescriptionTemplate: "body for " + name,
		Priority:            domain.PriorityUrgent,
		Labels:              pq.StringArray{"coverage", "template"},
		EstimatedHours:      &hours,
		CustomFields:        json.RawMessage(`{"team": "mesh"}`),
		AssigneeID:          &fx.agentID,
		AssigneeType:        &assigneeType,
		StatusID:            &fx.statusID,
		CreatedBy:           &fx.userID,
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}
	require.NoError(t, (&TaskTemplateRepo{db: db}).Create(context.Background(), tmpl))
	return tmpl
}

func TestTaskTemplateRepoGetByID_ReturnsEveryListedColumn(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	seeded := seedTaskTemplate(t, db, fx, "coverage template")

	got, err := (&TaskTemplateRepo{db: db}).GetByID(context.Background(), seeded.ID)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, seeded.ID, got.ID)
	assert.Equal(t, fx.projectID, got.ProjectID)
	assert.Equal(t, "coverage template", got.Name)
	assert.Equal(t, "description for coverage template", got.Description)
	assert.Equal(t, "[{{.Sprint}}] coverage template", got.TitleTemplate,
		"title_template and description_template are adjacent and easy to transpose")
	assert.Equal(t, "body for coverage template", got.DescriptionTemplate)
	assert.Equal(t, domain.PriorityUrgent, got.Priority)
	assert.Equal(t, pq.StringArray{"coverage", "template"}, got.Labels)
	require.NotNil(t, got.EstimatedHours)
	assert.InDelta(t, 4.5, *got.EstimatedHours, 0.001)
	assert.JSONEq(t, `{"team": "mesh"}`, string(got.CustomFields))
	require.NotNil(t, got.AssigneeID)
	assert.Equal(t, fx.agentID, *got.AssigneeID)
	require.NotNil(t, got.AssigneeType)
	assert.Equal(t, domain.AssigneeTypeAgent, *got.AssigneeType)
	require.NotNil(t, got.StatusID)
	assert.Equal(t, fx.statusID, *got.StatusID)
	require.NotNil(t, got.CreatedBy)
	assert.Equal(t, fx.userID, *got.CreatedBy)
}

func TestTaskTemplateRepoList_ScopesToTheProjectAndPopulates(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	seeded := seedTaskTemplate(t, db, fx, "coverage template")

	other := newCoverageFixture(t, db)
	seedTaskTemplate(t, db, other, "someone else's template")

	templates, err := (&TaskTemplateRepo{db: db}).List(context.Background(), fx.projectID)
	require.NoError(t, err)
	require.Len(t, templates, 1)

	assert.Equal(t, seeded.ID, templates[0].ID)
	assert.Equal(t, "coverage template", templates[0].Name)
	assert.Equal(t, domain.PriorityUrgent, templates[0].Priority)
	assert.Equal(t, pq.StringArray{"coverage", "template"}, templates[0].Labels)
	assert.JSONEq(t, `{"team": "mesh"}`, string(templates[0].CustomFields))
	require.NotNil(t, templates[0].StatusID)
	assert.Equal(t, fx.statusID, *templates[0].StatusID)
}

// ---------------------------------------------------------------------------
// artifact_repo.go: GetByID, GetByIDInWorkspace, ListByTask
// ---------------------------------------------------------------------------

// seedArtifact writes one artifacts row against the given task.
func seedArtifact(t *testing.T, db *sqlx.DB, taskID, uploader uuid.UUID, name string) *domain.Artifact {
	t.Helper()
	a := &domain.Artifact{
		ID:             uuid.New(),
		TaskID:         taskID,
		Name:           name,
		ArtifactType:   domain.ArtifactTypeReport,
		MimeType:       "text/markdown",
		StorageKey:     "artifacts/" + name,
		SizeBytes:      4242,
		ChecksumSHA256: "sha-" + uuid.New().String(),
		Metadata:       json.RawMessage(`{"origin": "coverage"}`),
		UploadedBy:     uploader,
		UploadedByType: domain.UploaderTypeAgent,
		CreatedAt:      time.Now(),
	}
	require.NoError(t, NewArtifactRepo(db).Create(context.Background(), a))
	return a
}

func TestArtifactRepoGetByID_ReturnsEveryListedColumn(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	seeded := seedArtifact(t, db, fx.taskID, fx.agentID, "coverage-report.md")

	got, err := NewArtifactRepo(db).GetByID(context.Background(), seeded.ID)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, seeded.ID, got.ID)
	assert.Equal(t, fx.taskID, got.TaskID)
	assert.Equal(t, "coverage-report.md", got.Name)
	assert.Equal(t, domain.ArtifactTypeReport, got.ArtifactType)
	assert.Equal(t, "text/markdown", got.MimeType)
	assert.Equal(t, "artifacts/coverage-report.md", got.StorageKey)
	assert.Equal(t, int64(4242), got.SizeBytes)
	assert.Equal(t, seeded.ChecksumSHA256, got.ChecksumSHA256)
	assert.JSONEq(t, `{"origin": "coverage"}`, string(got.Metadata))
	assert.Equal(t, fx.agentID, got.UploadedBy)
	assert.Equal(t, domain.UploaderTypeAgent, got.UploadedByType)
	assert.Empty(t, got.StorageURL, "storage_url has no column; the service layer computes it")
}

func TestArtifactRepoGetByIDInWorkspace_ResolvesThroughTaskAndProject(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	seeded := seedArtifact(t, db, fx.taskID, fx.agentID, "coverage-report.md")

	// This query is `a.`-qualified because it JOINs tasks and projects, both of
	// which also have created_at — an unqualified list would be ambiguous.
	got, err := NewArtifactRepo(db).GetByIDInWorkspace(context.Background(), seeded.ID, fx.workspaceID)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, seeded.ID, got.ID)
	assert.Equal(t, fx.taskID, got.TaskID)
	assert.Equal(t, "coverage-report.md", got.Name)
	assert.Equal(t, "artifacts/coverage-report.md", got.StorageKey)
	assert.Equal(t, int64(4242), got.SizeBytes)
	assert.Equal(t, domain.UploaderTypeAgent, got.UploadedByType)
	assert.WithinDuration(t, seeded.CreatedAt, got.CreatedAt, time.Second,
		"created_at must come from artifacts, not from the joined task or project")
}

func TestArtifactRepoGetByIDInWorkspace_DoesNotCrossWorkspaces(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	mine := newCoverageFixture(t, db)
	theirs := newCoverageFixture(t, db)
	seeded := seedArtifact(t, db, mine.taskID, mine.agentID, "private.md")

	got, err := NewArtifactRepo(db).GetByIDInWorkspace(context.Background(), seeded.ID, theirs.workspaceID)
	require.NoError(t, err)
	assert.Nil(t, got, "a foreign workspace must read as absent, not as an error")
}

func TestArtifactRepoListByTask_ReturnsFullyPopulatedRows(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	seeded := seedArtifact(t, db, fx.taskID, fx.agentID, "coverage-report.md")

	// An artifact on a different task must not leak in.
	otherTask := newCoverageTask(t, db, fx, "other task")
	seedArtifact(t, db, otherTask, fx.agentID, "elsewhere.md")

	page, err := NewArtifactRepo(db).ListByTask(context.Background(), fx.taskID,
		pagination.Params{Page: 1, PageSize: 50})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, 1, page.TotalCount)

	got := page.Items[0]
	assert.Equal(t, seeded.ID, got.ID)
	assert.Equal(t, "coverage-report.md", got.Name)
	assert.Equal(t, domain.ArtifactTypeReport, got.ArtifactType)
	assert.Equal(t, "text/markdown", got.MimeType)
	assert.Equal(t, int64(4242), got.SizeBytes)
	assert.JSONEq(t, `{"origin": "coverage"}`, string(got.Metadata))
	assert.Equal(t, domain.UploaderTypeAgent, got.UploadedByType)
}

// ---------------------------------------------------------------------------
// vcs_link_repo.go: ListByExternalID
// ---------------------------------------------------------------------------

func TestVCSLinkRepoListByExternalID_MatchesOnTheProviderTypeIDTriple(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	repo := NewVCSLinkRepo(db)
	ctx := context.Background()

	externalID := "pr-" + uuid.New().String()[:8]
	link := &domain.VCSLink{
		ID:         uuid.New(),
		TaskID:     fx.taskID,
		Provider:   domain.VCSProviderGitHub,
		LinkType:   domain.VCSLinkTypePR,
		ExternalID: externalID,
		URL:        "https://github.com/entire-vc/evc-mesh/pull/594",
		Title:      "replace unsafe SELECT *",
		Status:     domain.VCSLinkStatusOpen,
		Metadata:   json.RawMessage(`{"draft": false}`),
		CreatedAt:  time.Now(),
	}
	require.NoError(t, repo.Create(ctx, link))

	links, err := repo.ListByExternalID(ctx, domain.VCSProviderGitHub, domain.VCSLinkTypePR, externalID)
	require.NoError(t, err)
	require.Len(t, links, 1)

	got := links[0]
	assert.Equal(t, link.ID, got.ID)
	assert.Equal(t, fx.taskID, got.TaskID)
	assert.Equal(t, domain.VCSProviderGitHub, got.Provider)
	assert.Equal(t, domain.VCSLinkTypePR, got.LinkType)
	assert.Equal(t, externalID, got.ExternalID)
	assert.Equal(t, "https://github.com/entire-vc/evc-mesh/pull/594", got.URL)
	assert.Equal(t, "replace unsafe SELECT *", got.Title)
	assert.Equal(t, domain.VCSLinkStatusOpen, got.Status)
	assert.JSONEq(t, `{"draft": false}`, string(got.Metadata))
}

func TestVCSLinkRepoListByExternalID_DiscriminatesOnEachPartOfTheTriple(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	repo := NewVCSLinkRepo(db)
	ctx := context.Background()

	externalID := "pr-" + uuid.New().String()[:8]
	require.NoError(t, repo.Create(ctx, &domain.VCSLink{
		ID: uuid.New(), TaskID: fx.taskID,
		Provider: domain.VCSProviderGitHub, LinkType: domain.VCSLinkTypePR,
		ExternalID: externalID, URL: "https://example.com/pr",
		Title: "the pr", Status: domain.VCSLinkStatusOpen, CreatedAt: time.Now(),
	}))

	// Right external_id, wrong provider and wrong link_type: both must miss.
	wrongProvider, err := repo.ListByExternalID(ctx, domain.VCSProviderGitLab, domain.VCSLinkTypePR, externalID)
	require.NoError(t, err)
	assert.Empty(t, wrongProvider)

	wrongType, err := repo.ListByExternalID(ctx, domain.VCSProviderGitHub, domain.VCSLinkTypeCommit, externalID)
	require.NoError(t, err)
	assert.Empty(t, wrongType)
}
