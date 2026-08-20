package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// Readers on workspaces, projects, project_updates and initiatives — every one
// of them rewritten from `SELECT *` (bare or `p.*`/`i.*`-qualified) by this
// change, and none of them previously covered at this package's level.
//
// See select_star_coverage_fixtures_db_test.go for why these assert on field
// values rather than on row counts.

// ---------------------------------------------------------------------------
// workspace_repo.go: GetByID, GetBySlug
// ---------------------------------------------------------------------------

func TestWorkspaceRepoGetByID_ReturnsEveryListedColumn(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)

	got, err := NewWorkspaceRepo(db).GetByID(context.Background(), fx.workspaceID)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, fx.workspaceID, got.ID)
	assert.Equal(t, "coverage workspace "+fx.suffix, got.Name)
	assert.Equal(t, fx.workspaceSlug, got.Slug,
		"slug must survive the explicit column list")
	assert.Equal(t, fx.userID, got.OwnerID,
		"owner_id is what every membership check reads; dropping it would read as uuid.Nil")
	assert.JSONEq(t, `{}`, string(got.Settings))
	assert.False(t, got.CreatedAt.IsZero(), "created_at must be selected, not left zero")
	assert.False(t, got.UpdatedAt.IsZero(), "updated_at must be selected, not left zero")
}

func TestWorkspaceRepoGetBySlug_ReturnsTheSameRowAsGetByID(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)

	got, err := NewWorkspaceRepo(db).GetBySlug(context.Background(), fx.workspaceSlug)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, fx.workspaceID, got.ID)
	assert.Equal(t, fx.workspaceSlug, got.Slug)
	assert.Equal(t, fx.userID, got.OwnerID)
	assert.Equal(t, "coverage workspace "+fx.suffix, got.Name)
}

func TestWorkspaceRepoGetBySlug_UnknownSlugIsNilNotAnError(t *testing.T) {
	db := selectStarCoverageTestDB(t)

	got, err := NewWorkspaceRepo(db).GetBySlug(context.Background(), "no-such-workspace-"+uuid.New().String()[:8])
	require.NoError(t, err)
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// project_repo.go: GetByID, GetBySlug, List
// ---------------------------------------------------------------------------

func TestProjectRepoGetByID_ReturnsEveryListedColumn(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)

	got, err := NewProjectRepo(db).GetByID(context.Background(), fx.projectID)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, fx.projectID, got.ID)
	assert.Equal(t, fx.workspaceID, got.WorkspaceID)
	assert.Equal(t, "coverage project "+fx.suffix, got.Name)
	assert.Equal(t, "seeded by newCoverageFixture", got.Description)
	assert.Equal(t, fx.projectSlug, got.Slug)
	assert.Equal(t, "rocket", got.Icon,
		"icon is easy to omit from a hand-written column list and reads as \"\" when it is")
	assert.Equal(t, domain.DefaultAssigneeNone, got.DefaultAssigneeType)
	assert.False(t, got.IsArchived)
	assert.JSONEq(t, `{}`, string(got.Settings))
	assert.False(t, got.CreatedAt.IsZero())
}

func TestProjectRepoGetBySlug_ScopesToTheWorkspace(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	mine := newCoverageFixture(t, db)
	theirs := newCoverageFixture(t, db)

	repo := NewProjectRepo(db)

	got, err := repo.GetBySlug(context.Background(), mine.workspaceID, mine.projectSlug)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, mine.projectID, got.ID)
	assert.Equal(t, "rocket", got.Icon)

	// Same slug lookup, wrong workspace: no row, and not an error.
	crossed, err := repo.GetBySlug(context.Background(), theirs.workspaceID, mine.projectSlug)
	require.NoError(t, err)
	assert.Nil(t, crossed, "a slug must not resolve across workspace boundaries")
}

func TestProjectRepoList_ReturnsFullyPopulatedRowsScopedToTheWorkspace(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)

	page, err := NewProjectRepo(db).List(context.Background(), fx.workspaceID,
		repository.ProjectFilter{}, pagination.Params{Page: 1, PageSize: 50})
	require.NoError(t, err)
	require.Len(t, page.Items, 1, "the fixture's workspace holds exactly one project")
	assert.Equal(t, 1, page.TotalCount)

	got := page.Items[0]
	assert.Equal(t, fx.projectID, got.ID)
	assert.Equal(t, fx.projectSlug, got.Slug)
	assert.Equal(t, "rocket", got.Icon)
	assert.Equal(t, domain.DefaultAssigneeNone, got.DefaultAssigneeType)
	assert.Equal(t, fx.workspaceID, got.WorkspaceID)
}

// ---------------------------------------------------------------------------
// project_update_repo.go: List, GetLatest
// ---------------------------------------------------------------------------

// seedProjectUpdate writes one project_updates row with every JSONB column set
// to a distinguishable value.
func seedProjectUpdate(t *testing.T, fx coverageFixture, repo *ProjectUpdateRepo, title string, createdAt time.Time) *domain.ProjectUpdate {
	t.Helper()
	u := &domain.ProjectUpdate{
		ID:         uuid.New(),
		ProjectID:  fx.projectID,
		Title:      title,
		Status:     domain.UpdateStatusAtRisk,
		Summary:    "summary for " + title,
		Highlights: json.RawMessage(`["shipped the reader"]`),
		Blockers:   json.RawMessage(`["waiting on review"]`),
		NextSteps:  json.RawMessage(`["merge"]`),
		Metrics:    json.RawMessage(`{"coverage": 80}`),
		CreatedBy:  fx.userID,
		CreatedAt:  createdAt,
	}
	require.NoError(t, repo.Create(context.Background(), u))
	return u
}

func TestProjectUpdateRepoList_ReturnsEveryListedColumn(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	repo := &ProjectUpdateRepo{db: db}

	now := time.Now()
	seedProjectUpdate(t, fx, repo, "older update", now.Add(-time.Hour))
	newest := seedProjectUpdate(t, fx, repo, "newest update", now)

	page, err := repo.List(context.Background(), fx.projectID,
		pagination.Params{Page: 1, PageSize: 50})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.Equal(t, 2, page.TotalCount)

	// ORDER BY created_at DESC: the newest row is first.
	got := page.Items[0]
	assert.Equal(t, newest.ID, got.ID)
	assert.Equal(t, "newest update", got.Title)
	assert.Equal(t, domain.UpdateStatusAtRisk, got.Status)
	assert.Equal(t, "summary for newest update", got.Summary)
	assert.JSONEq(t, `["shipped the reader"]`, string(got.Highlights))
	assert.JSONEq(t, `["waiting on review"]`, string(got.Blockers),
		"blockers is a distinct column from highlights; swapping or dropping it is invisible without this")
	assert.JSONEq(t, `["merge"]`, string(got.NextSteps))
	assert.JSONEq(t, `{"coverage": 80}`, string(got.Metrics))
	assert.Equal(t, fx.userID, got.CreatedBy)
}

func TestProjectUpdateRepoGetLatest_ReturnsTheNewestFullyPopulated(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	repo := &ProjectUpdateRepo{db: db}

	now := time.Now()
	seedProjectUpdate(t, fx, repo, "older update", now.Add(-time.Hour))
	newest := seedProjectUpdate(t, fx, repo, "newest update", now)

	got, err := repo.GetLatest(context.Background(), fx.projectID)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, newest.ID, got.ID)
	assert.Equal(t, "newest update", got.Title)
	assert.Equal(t, domain.UpdateStatusAtRisk, got.Status)
	assert.Equal(t, "summary for newest update", got.Summary)
	assert.JSONEq(t, `{"coverage": 80}`, string(got.Metrics))
}

func TestProjectUpdateRepoGetLatest_NoUpdatesIsNilNotAnError(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)

	got, err := (&ProjectUpdateRepo{db: db}).GetLatest(context.Background(), fx.projectID)
	require.NoError(t, err)
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// initiative_repo.go: GetByID, List, GetByProjectID, ListLinkedProjects
// ---------------------------------------------------------------------------

// seedInitiative writes one initiative in the fixture's workspace and links the
// fixture's project to it, so the JOIN-based readers have a row to find.
func seedInitiative(t *testing.T, fx coverageFixture, repo *InitiativeRepo, name string) *domain.Initiative {
	t.Helper()
	ctx := context.Background()
	// initiatives.target_date is a DATE, not a timestamptz: anything with a
	// time-of-day comes back truncated to midnight. Seed midnight UTC so the
	// value round-trips exactly and the assertion can be an equality.
	y, m, d := time.Now().Add(72 * time.Hour).UTC().Date()
	target := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	ini := &domain.Initiative{
		ID:          uuid.New(),
		WorkspaceID: fx.workspaceID,
		Name:        name,
		Description: "description for " + name,
		Status:      domain.InitiativeStatusActive,
		TargetDate:  &target,
		CreatedBy:   fx.userID,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	require.NoError(t, repo.Create(ctx, ini))
	return ini
}

func TestInitiativeRepoGetByID_ReturnsEveryListedColumn(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	repo := &InitiativeRepo{db: db}
	seeded := seedInitiative(t, fx, repo, "coverage initiative")

	got, err := repo.GetByID(context.Background(), seeded.ID)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, seeded.ID, got.ID)
	assert.Equal(t, fx.workspaceID, got.WorkspaceID)
	assert.Equal(t, "coverage initiative", got.Name)
	assert.Equal(t, "description for coverage initiative", got.Description)
	assert.Equal(t, domain.InitiativeStatusActive, got.Status)
	require.NotNil(t, got.TargetDate, "target_date must be selected, not left nil")
	assert.WithinDuration(t, *seeded.TargetDate, *got.TargetDate, time.Second)
	assert.Equal(t, fx.userID, got.CreatedBy)
}

func TestInitiativeRepoList_ScopesToTheWorkspaceAndPopulates(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	repo := &InitiativeRepo{db: db}
	seeded := seedInitiative(t, fx, repo, "coverage initiative")

	// A second workspace with its own initiative must not leak into the list.
	other := newCoverageFixture(t, db)
	seedInitiative(t, other, repo, "someone else's initiative")

	items, err := repo.List(context.Background(), fx.workspaceID)
	require.NoError(t, err)
	require.Len(t, items, 1)

	assert.Equal(t, seeded.ID, items[0].ID)
	assert.Equal(t, "coverage initiative", items[0].Name)
	assert.Equal(t, domain.InitiativeStatusActive, items[0].Status)
	assert.Equal(t, fx.userID, items[0].CreatedBy)
}

func TestInitiativeRepoGetByProjectID_ResolvesThroughTheLinkTable(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	repo := &InitiativeRepo{db: db}
	seeded := seedInitiative(t, fx, repo, "linked initiative")
	require.NoError(t, repo.LinkProject(context.Background(), seeded.ID, fx.projectID))

	items, err := repo.GetByProjectID(context.Background(), fx.projectID)
	require.NoError(t, err)
	require.Len(t, items, 1)

	// This query is `i.`-qualified: an unqualified created_at here would be
	// ambiguous against initiative_projects, so the values matter twice over.
	assert.Equal(t, seeded.ID, items[0].ID)
	assert.Equal(t, "linked initiative", items[0].Name)
	assert.Equal(t, "description for linked initiative", items[0].Description)
	assert.Equal(t, fx.workspaceID, items[0].WorkspaceID)
	assert.False(t, items[0].CreatedAt.IsZero())
}

func TestInitiativeRepoGetByProjectID_UnlinkedProjectIsEmpty(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	repo := &InitiativeRepo{db: db}
	seedInitiative(t, fx, repo, "unlinked initiative")

	items, err := repo.GetByProjectID(context.Background(), fx.projectID)
	require.NoError(t, err)
	assert.Empty(t, items)
}

func TestInitiativeRepoListLinkedProjects_ReturnsFullyPopulatedProjects(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	repo := &InitiativeRepo{db: db}
	seeded := seedInitiative(t, fx, repo, "initiative with projects")
	require.NoError(t, repo.LinkProject(context.Background(), seeded.ID, fx.projectID))

	projects, err := repo.ListLinkedProjects(context.Background(), seeded.ID)
	require.NoError(t, err)
	require.Len(t, projects, 1)

	// Scans into projectRow via projectSelectColsQualified — the same column
	// list project_repo.go uses, reached through a different query.
	got := projects[0]
	assert.Equal(t, fx.projectID, got.ID)
	assert.Equal(t, fx.projectSlug, got.Slug)
	assert.Equal(t, "coverage project "+fx.suffix, got.Name)
	assert.Equal(t, "rocket", got.Icon)
	assert.Equal(t, fx.workspaceID, got.WorkspaceID)
	assert.Equal(t, domain.DefaultAssigneeNone, got.DefaultAssigneeType)
	assert.JSONEq(t, `{}`, string(got.Settings))
}

func TestInitiativeRepoListLinkedProjects_SkipsSoftDeletedProjects(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	repo := &InitiativeRepo{db: db}
	seeded := seedInitiative(t, fx, repo, "initiative with a deleted project")
	require.NoError(t, repo.LinkProject(context.Background(), seeded.ID, fx.projectID))
	require.NoError(t, NewProjectRepo(db).Delete(context.Background(), fx.projectID))

	projects, err := repo.ListLinkedProjects(context.Background(), seeded.ID)
	require.NoError(t, err)
	assert.Empty(t, projects, "the link survives a soft delete; the reader must still filter it out")
}
