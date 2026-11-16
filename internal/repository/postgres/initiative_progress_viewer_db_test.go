package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Initiative progress and linked projects are read through a ProjectViewer: a
// project the viewer cannot open adds nothing to the bar and is not listed
// (PRD dashboards §6.3, §6.4). Before this, the count joined every linked
// project, so a member outside one of them read its task totals off the bar.

// initiativeViewerFixture is one workspace with two projects linked to one
// initiative:
//
//	A: 2 counted tasks (1 todo, 1 done) + 1 cancelled (never counted)
//	B: 3 counted tasks, all done
type initiativeViewerFixture struct {
	coverageFixture
	sqlDB        *sqlx.DB
	repo         *InitiativeRepo
	initiativeID uuid.UUID
	projectB     uuid.UUID
}

func newInitiativeViewerFixture(t *testing.T) initiativeViewerFixture {
	t.Helper()
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	ctx := context.Background()

	doneA := seedStatus(t, db, fx.projectID, "done-a", domain.StatusCategoryDone)
	cancelledA := seedStatus(t, db, fx.projectID, "cancelled-a", domain.StatusCategoryCancelled)
	seedTaskIn(t, db, fx, fx.projectID, doneA, "A done")
	seedTaskIn(t, db, fx, fx.projectID, cancelledA, "A cancelled")

	now := time.Now()
	projB := &domain.Project{
		ID:                  uuid.New(),
		WorkspaceID:         fx.workspaceID,
		Name:                "private project " + fx.suffix,
		Slug:                "private-proj-" + fx.suffix,
		DefaultAssigneeType: domain.DefaultAssigneeNone,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	require.NoError(t, NewProjectRepo(db).Create(ctx, projB))
	doneB := seedStatus(t, db, projB.ID, "done-b", domain.StatusCategoryDone)
	for _, title := range []string{"B one", "B two", "B three"} {
		seedTaskIn(t, db, fx, projB.ID, doneB, title)
	}

	repo := &InitiativeRepo{db: db}
	ini := seedInitiative(t, fx, repo, "viewer initiative")
	require.NoError(t, repo.LinkProject(ctx, ini.ID, fx.projectID))
	require.NoError(t, repo.LinkProject(ctx, ini.ID, projB.ID))

	return initiativeViewerFixture{coverageFixture: fx, sqlDB: db, repo: repo, initiativeID: ini.ID, projectB: projB.ID}
}

func seedStatus(t *testing.T, db *sqlx.DB, projectID uuid.UUID, slug string, cat domain.StatusCategory) uuid.UUID {
	t.Helper()
	st := &domain.TaskStatus{
		ID: uuid.New(), ProjectID: projectID, Name: slug, Slug: slug,
		Color: "#123456", Position: 5, Category: cat,
	}
	require.NoError(t, NewTaskStatusRepo(db).Create(context.Background(), st))
	return st.ID
}

func seedTaskIn(t *testing.T, db *sqlx.DB, fx coverageFixture, projectID, statusID uuid.UUID, title string) {
	t.Helper()
	task := &domain.Task{
		ID: uuid.New(), ProjectID: projectID, StatusID: statusID, Title: title,
		AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium,
		CreatedBy: fx.userID, CreatedByType: domain.ActorTypeUser,
	}
	require.NoError(t, NewTaskRepo(db).Create(context.Background(), task, nil))
}

// seedMemberUser creates a workspace member who is a member of exactly the
// given projects (project_members requires the workspace membership first).
func seedMemberUser(t *testing.T, db *sqlx.DB, workspaceID uuid.UUID, projects ...uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.New().String()[:8]
	u := &domain.User{
		ID: uuid.New(), Email: "ini-member-" + suffix + "@example.com", PasswordHash: "x",
		Name: "Initiative Member", Username: "ini-member-" + suffix, IsActive: true,
	}
	require.NoError(t, NewUserRepo(db).Create(ctx, u))
	_, err := db.ExecContext(ctx, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1, $2, $3, 'member')`,
		uuid.New(), workspaceID, u.ID)
	require.NoError(t, err)
	for _, p := range projects {
		uid := u.ID
		require.NoError(t, NewProjectMemberRepo(db).Create(ctx, &domain.ProjectMember{
			ID: uuid.New(), ProjectID: p, UserID: &uid, Role: domain.ProjectRoleMember,
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}))
	}
	return u.ID
}

func linkedIDs(ps []domain.Project) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return out
}

func TestInitiativeProgress_MemberWithoutAccessToAProjectDoesNotCountIt(t *testing.T) {
	fx := newInitiativeViewerFixture(t)
	ctx := context.Background()
	onlyA := seedMemberUser(t, fx.sqlDB, fx.workspaceID, fx.projectID)

	total, completed, err := fx.repo.Progress(ctx, fx.initiativeID, domain.ProjectViewer{UserID: &onlyA})
	require.NoError(t, err)
	assert.Equal(t, 2, total, "B's three tasks must not reach a member outside B")
	assert.Equal(t, 1, completed)

	projects, err := fx.repo.ListLinkedProjects(ctx, fx.initiativeID, domain.ProjectViewer{UserID: &onlyA})
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{fx.projectID}, linkedIDs(projects), "B is not listed to a member outside it")
}

func TestInitiativeProgress_MemberWithAccessCountsEveryProject(t *testing.T) {
	fx := newInitiativeViewerFixture(t)
	ctx := context.Background()
	both := seedMemberUser(t, fx.sqlDB, fx.workspaceID, fx.projectID, fx.projectB)

	total, completed, err := fx.repo.Progress(ctx, fx.initiativeID, domain.ProjectViewer{UserID: &both})
	require.NoError(t, err)
	assert.Equal(t, 5, total)
	assert.Equal(t, 4, completed)

	projects, err := fx.repo.ListLinkedProjects(ctx, fx.initiativeID, domain.ProjectViewer{UserID: &both})
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{fx.projectID, fx.projectB}, linkedIDs(projects))
}

func TestInitiativeProgress_OwnerAdminAgentAndNobody(t *testing.T) {
	fx := newInitiativeViewerFixture(t)
	ctx := context.Background()

	t.Run("owner or admin sees every linked project", func(t *testing.T) {
		total, completed, err := fx.repo.Progress(ctx, fx.initiativeID, domain.AllProjectsViewer())
		require.NoError(t, err)
		assert.Equal(t, 5, total)
		assert.Equal(t, 4, completed)
	})

	t.Run("an agent counts only its explicit memberships", func(t *testing.T) {
		agentID := fx.agentID
		require.NoError(t, NewProjectMemberRepo(fx.sqlDB).Create(ctx, &domain.ProjectMember{
			ID: uuid.New(), ProjectID: fx.projectB, AgentID: &agentID, Role: domain.ProjectRoleMember,
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}))
		total, completed, err := fx.repo.Progress(ctx, fx.initiativeID, domain.ProjectViewer{AgentID: &agentID})
		require.NoError(t, err)
		assert.Equal(t, 3, total)
		assert.Equal(t, 3, completed)
	})

	t.Run("the zero viewer sees nothing", func(t *testing.T) {
		total, completed, err := fx.repo.Progress(ctx, fx.initiativeID, domain.ProjectViewer{})
		require.NoError(t, err)
		assert.Zero(t, total)
		assert.Zero(t, completed)
		projects, err := fx.repo.ListLinkedProjects(ctx, fx.initiativeID, domain.ProjectViewer{})
		require.NoError(t, err)
		assert.Empty(t, projects)
	})
}
