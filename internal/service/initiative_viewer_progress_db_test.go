package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// The initiative service reads progress and linked projects through the
// viewer (PRD dashboards §6.3, §6.4), against real Postgres repos: a member
// outside one linked project gets neither its tasks in the bar nor the project
// in the list, through GetByID and List alike.

type initiativeViewerSvcFixture struct {
	svc         InitiativeService
	workspaceID uuid.UUID
	projectA    uuid.UUID
	projectB    uuid.UUID
	onlyA       uuid.UUID
	both        uuid.UUID
	ini         *domain.Initiative
}

func newInitiativeViewerSvcFixture(t *testing.T) initiativeViewerSvcFixture {
	t.Helper()
	db := agentKeyWsReuseTestDB(t)
	ctx := context.Background()
	owner := agentKeyWsReuseOwner(t, db)
	suffix := uuid.New().String()[:8]
	now := time.Now()

	ws := &domain.Workspace{
		ID: uuid.New(), Name: "initiative viewer " + suffix, Slug: "ini-viewer-" + suffix,
		OwnerID: owner, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, postgres.NewWorkspaceRepo(db).Create(ctx, ws))

	newProject := func(name string) uuid.UUID {
		p := &domain.Project{
			ID: uuid.New(), WorkspaceID: ws.ID, Name: name + " " + suffix, Slug: name + "-" + suffix,
			DefaultAssigneeType: domain.DefaultAssigneeNone, CreatedAt: now, UpdatedAt: now,
		}
		require.NoError(t, postgres.NewProjectRepo(db).Create(ctx, p))
		return p.ID
	}
	projectA, projectB := newProject("ini-a"), newProject("ini-b")

	// A: 1 todo + 1 done + 1 cancelled (not counted). B: 3 done.
	seedTasks(t, db, owner, projectA, map[domain.StatusCategory]int{
		domain.StatusCategoryTodo: 1, domain.StatusCategoryDone: 1, domain.StatusCategoryCancelled: 1,
	})
	seedTasks(t, db, owner, projectB, map[domain.StatusCategory]int{domain.StatusCategoryDone: 3})

	svc := NewInitiativeService(postgres.NewInitiativeRepo(db), postgres.NewProjectRepo(db))
	ini, err := svc.Create(ctx, CreateInitiativeInput{WorkspaceID: ws.ID, Name: "viewer initiative " + suffix, CreatedBy: owner})
	require.NoError(t, err)
	require.NoError(t, svc.LinkProject(ctx, ini.ID, projectA))
	require.NoError(t, svc.LinkProject(ctx, ini.ID, projectB))

	return initiativeViewerSvcFixture{
		svc: svc, workspaceID: ws.ID, projectA: projectA, projectB: projectB, ini: ini,
		onlyA: seedWorkspaceMember(t, db, ws.ID, projectA),
		both:  seedWorkspaceMember(t, db, ws.ID, projectA, projectB),
	}
}

func seedTasks(t *testing.T, db *sqlx.DB, creator, projectID uuid.UUID, counts map[domain.StatusCategory]int) {
	t.Helper()
	ctx := context.Background()
	pos := 0
	for cat, n := range counts {
		pos++
		st := &domain.TaskStatus{
			ID: uuid.New(), ProjectID: projectID, Name: "ini " + string(cat), Slug: "ini-" + string(cat),
			Color: "#123456", Position: pos, Category: cat, IsDefault: pos == 1,
		}
		require.NoError(t, postgres.NewTaskStatusRepo(db).Create(ctx, st))
		for i := 0; i < n; i++ {
			require.NoError(t, postgres.NewTaskRepo(db).Create(ctx, &domain.Task{
				ID: uuid.New(), ProjectID: projectID, StatusID: st.ID, Title: string(cat) + " task",
				AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium,
				CreatedBy: creator, CreatedByType: domain.ActorTypeUser,
			}, nil))
		}
	}
}

// seedWorkspaceMember creates a workspace member who belongs to exactly the given projects.
func seedWorkspaceMember(t *testing.T, db *sqlx.DB, workspaceID uuid.UUID, projects ...uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.New().String()[:8]
	u := &domain.User{
		ID: uuid.New(), Email: "ini-svc-" + suffix + "@example.com", PasswordHash: "x",
		Name: "Initiative Viewer", Username: "ini-svc-" + suffix, IsActive: true,
	}
	require.NoError(t, postgres.NewUserRepo(db).Create(ctx, u))
	_, err := db.ExecContext(ctx, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1, $2, $3, 'member')`,
		uuid.New(), workspaceID, u.ID)
	require.NoError(t, err)
	for _, p := range projects {
		uid := u.ID
		require.NoError(t, postgres.NewProjectMemberRepo(db).Create(ctx, &domain.ProjectMember{
			ID: uuid.New(), ProjectID: p, UserID: &uid, Role: domain.ProjectRoleMember,
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}))
	}
	return u.ID
}

func projectIDs(ps []domain.Project) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return out
}

func TestInitiativeService_GetByID_CountsOnlyProjectsTheViewerMayOpen(t *testing.T) {
	fx := newInitiativeViewerSvcFixture(t)
	ctx := context.Background()

	got, err := fx.svc.GetByID(ctx, fx.ini.ID, domain.ProjectViewer{UserID: &fx.onlyA})
	require.NoError(t, err)
	assert.Equal(t, 2, got.TotalTasks, "B's tasks must not reach a member outside B")
	assert.Equal(t, 1, got.CompletedTasks)
	assert.Equal(t, []uuid.UUID{fx.projectA}, projectIDs(got.LinkedProjects))

	// Positive control: a member of both projects counts both.
	got, err = fx.svc.GetByID(ctx, fx.ini.ID, domain.ProjectViewer{UserID: &fx.both})
	require.NoError(t, err)
	assert.Equal(t, 5, got.TotalTasks)
	assert.Equal(t, 4, got.CompletedTasks)
	assert.ElementsMatch(t, []uuid.UUID{fx.projectA, fx.projectB}, projectIDs(got.LinkedProjects))

	got, err = fx.svc.GetByID(ctx, fx.ini.ID, domain.AllProjectsViewer())
	require.NoError(t, err)
	assert.Equal(t, 5, got.TotalTasks)

	_, err = fx.svc.GetByID(ctx, uuid.New(), domain.AllProjectsViewer())
	require.Error(t, err, "an unknown initiative is not found")
}

func TestInitiativeService_List_CountsOnlyProjectsTheViewerMayOpen(t *testing.T) {
	fx := newInitiativeViewerSvcFixture(t)
	ctx := context.Background()

	find := func(items []domain.Initiative) domain.Initiative {
		for _, i := range items {
			if i.ID == fx.ini.ID {
				return i
			}
		}
		t.Fatalf("initiative %s not listed", fx.ini.ID)
		return domain.Initiative{}
	}

	items, err := fx.svc.List(ctx, fx.workspaceID, domain.ProjectViewer{UserID: &fx.onlyA})
	require.NoError(t, err)
	ini := find(items)
	assert.Equal(t, 2, ini.TotalTasks)
	assert.Equal(t, 1, ini.CompletedTasks)

	items, err = fx.svc.List(ctx, fx.workspaceID, domain.ProjectViewer{UserID: &fx.both})
	require.NoError(t, err)
	ini = find(items)
	assert.Equal(t, 5, ini.TotalTasks)
	assert.Equal(t, 4, ini.CompletedTasks)

	items, err = fx.svc.List(ctx, fx.workspaceID, domain.ProjectViewer{})
	require.NoError(t, err)
	assert.Zero(t, find(items).TotalTasks, "the zero viewer counts nothing")
}

// Update clears the deadline only on ClearTargetDate; an update without a date
// leaves it, so the edit form can both keep and remove a deadline.
func TestInitiativeService_Update_TargetDate(t *testing.T) {
	fx := newInitiativeViewerSvcFixture(t)
	ctx := context.Background()
	deadline := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	name := "renamed"

	_, err := fx.svc.Update(ctx, fx.ini.ID, UpdateInitiativeInput{TargetDate: &deadline})
	require.NoError(t, err)

	_, err = fx.svc.Update(ctx, fx.ini.ID, UpdateInitiativeInput{Name: &name})
	require.NoError(t, err)
	got, err := fx.svc.GetByID(ctx, fx.ini.ID, domain.AllProjectsViewer())
	require.NoError(t, err)
	require.NotNil(t, got.TargetDate, "an update without a date keeps the deadline")
	assert.Equal(t, "2026-12-01", got.TargetDate.Format("2006-01-02"))

	_, err = fx.svc.Update(ctx, fx.ini.ID, UpdateInitiativeInput{ClearTargetDate: true})
	require.NoError(t, err)
	got, err = fx.svc.GetByID(ctx, fx.ini.ID, domain.AllProjectsViewer())
	require.NoError(t, err)
	assert.Nil(t, got.TargetDate, "ClearTargetDate removes the deadline")
	assert.Equal(t, name, got.Name)
}
