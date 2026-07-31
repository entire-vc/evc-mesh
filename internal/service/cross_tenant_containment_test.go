package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// --- /workspaces/:ws_id/invites/:invite_id ----------------------------------

// fakeInviteRepo is the smallest repository the two guarded calls need.
type fakeInviteRepo struct {
	repository.WorkspaceInviteRepository
	invites map[uuid.UUID]*domain.WorkspaceInvite
	deleted []uuid.UUID
}

func (f *fakeInviteRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.WorkspaceInvite, error) {
	return f.invites[id], nil
}

func (f *fakeInviteRepo) Delete(_ context.Context, id uuid.UUID) error {
	f.deleted = append(f.deleted, id)
	delete(f.invites, id)
	return nil
}

type fakeWorkspaceRepoForInvites struct {
	repository.WorkspaceRepository
	ws *domain.Workspace
}

func (f *fakeWorkspaceRepoForInvites) GetByID(context.Context, uuid.UUID) (*domain.Workspace, error) {
	return f.ws, nil
}

type recordingEmailService struct{ sent int }

func (r *recordingEmailService) Enabled() bool { return true }

func (r *recordingEmailService) SendInvite(context.Context, string, string, string) error {
	r.sent++
	return nil
}

// TestInviteService_ResendAndRevoke_AreScopedToTheWorkspace is the cross-tenant
// repro at the service.
//
// The route is /workspaces/:ws_id/invites/:invite_id, but only :invite_id ever
// reached this code — RevokeInvite was a one-line `return s.inviteRepo.Delete(id)`.
// So an admin of any workspace could pass their own :ws_id with a stranger's
// :invite_id and revoke that tenant's pending invitation (204, row gone, their new
// hire's link dead) or re-send it, which mails a stranger's invitee on demand.
func TestInviteService_ResendAndRevoke_AreScopedToTheWorkspace(t *testing.T) {
	victimWS := uuid.New()
	intruderWS := uuid.New()
	inviteID := uuid.New()

	newSvc := func() (WorkspaceInviteService, *fakeInviteRepo, *recordingEmailService) {
		repo := &fakeInviteRepo{invites: map[uuid.UUID]*domain.WorkspaceInvite{
			inviteID: {
				ID:          inviteID,
				WorkspaceID: victimWS,
				Email:       "victim-invitee@example.com",
				Token:       "tok",
				ExpiresAt:   time.Now().Add(24 * time.Hour),
			},
		}}
		email := &recordingEmailService{}
		svc := NewInviteService(repo, nil, nil,
			&fakeWorkspaceRepoForInvites{ws: &domain.Workspace{ID: victimWS, Name: "Victim"}},
			email, nil, "https://mesh.example.com")
		return svc, repo, email
	}

	t.Run("revoke from another workspace is refused and deletes nothing", func(t *testing.T) {
		svc, repo, _ := newSvc()

		err := svc.RevokeInvite(context.Background(), intruderWS, inviteID)
		require.Error(t, err, "an invite was revoked from another tenant")
		assert.Empty(t, repo.deleted, "the row was deleted anyway")
		assert.Len(t, repo.invites, 1)
	})

	t.Run("resend from another workspace sends no email", func(t *testing.T) {
		svc, _, email := newSvc()

		_, err := svc.ResendInvite(context.Background(), intruderWS, inviteID)
		require.Error(t, err, "another tenant's invitee was mailed")
		assert.Zero(t, email.sent, "the invitation email went out anyway")
	})

	t.Run("the owning workspace still works", func(t *testing.T) {
		svc, _, email := newSvc()

		delivery, err := svc.ResendInvite(context.Background(), victimWS, inviteID)
		require.NoError(t, err,
			"the workspace that owns the invite was refused its own resend")
		assert.Equal(t, InviteDeliverySent, delivery.Status)
		assert.Equal(t, 1, email.sent)

		svc2, repo2, _ := newSvc()
		require.NoError(t, svc2.RevokeInvite(context.Background(), victimWS, inviteID),
			"the workspace that owns the invite was refused its own revoke")
		assert.Equal(t, []uuid.UUID{inviteID}, repo2.deleted)
	})

	t.Run("an invite that does not exist is a 404, not a delete", func(t *testing.T) {
		svc, repo, _ := newSvc()

		require.Error(t, svc.RevokeInvite(context.Background(), victimWS, uuid.New()))
		assert.Empty(t, repo.deleted)
	})
}

// --- POST /initiatives/:init_id/projects ------------------------------------

type fakeInitiativeRepo struct {
	repository.InitiativeRepository
	initiatives map[uuid.UUID]*domain.Initiative
	linked      [][2]uuid.UUID
}

func (f *fakeInitiativeRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Initiative, error) {
	return f.initiatives[id], nil
}

func (f *fakeInitiativeRepo) LinkProject(_ context.Context, initiativeID, projectID uuid.UUID) error {
	f.linked = append(f.linked, [2]uuid.UUID{initiativeID, projectID})
	return nil
}

// TestInitiativeService_LinkProject_RefusesAnotherWorkspacesProject is the
// cross-tenant repro for the body half of the class.
//
// project_id arrives in the request body, where no route parameter names it and
// the workspace guard cannot see it; :init_id is the caller's own initiative and
// resolves to their own workspace. Linking was allowed on "both exist", so a
// member of any workspace could graft a stranger's project onto their own
// initiative and read its name and summary out of every initiative view.
func TestInitiativeService_LinkProject_RefusesAnotherWorkspacesProject(t *testing.T) {
	ownWS := uuid.New()
	foreignWS := uuid.New()
	initID := uuid.New()

	newSvc := func() (InitiativeService, *fakeInitiativeRepo, *MockProjectRepository) {
		initRepo := &fakeInitiativeRepo{initiatives: map[uuid.UUID]*domain.Initiative{
			initID: {ID: initID, WorkspaceID: ownWS, Name: "Mine"},
		}}
		projRepo := NewMockProjectRepository()
		return NewInitiativeService(initRepo, projRepo, nil), initRepo, projRepo
	}

	t.Run("a project from another workspace is refused and nothing is linked", func(t *testing.T) {
		svc, initRepo, projRepo := newSvc()
		foreignProject := uuid.New()
		projRepo.items[foreignProject] = &domain.Project{ID: foreignProject, WorkspaceID: foreignWS, Name: "Theirs"}

		err := svc.LinkProject(context.Background(), initID, foreignProject)
		require.Error(t, err, "another tenant's project was linked into this initiative")
		assert.Empty(t, initRepo.linked, "the link was written anyway")
	})

	t.Run("a project in the initiative's own workspace still links", func(t *testing.T) {
		svc, initRepo, projRepo := newSvc()
		ownProject := uuid.New()
		projRepo.items[ownProject] = &domain.Project{ID: ownProject, WorkspaceID: ownWS, Name: "Mine"}

		require.NoError(t, svc.LinkProject(context.Background(), initID, ownProject),
			"the owner was refused a project in their own workspace")
		assert.Equal(t, [][2]uuid.UUID{{initID, ownProject}}, initRepo.linked)
	})

	t.Run("an unknown project is a 404", func(t *testing.T) {
		svc, initRepo, _ := newSvc()

		require.Error(t, svc.LinkProject(context.Background(), initID, uuid.New()))
		assert.Empty(t, initRepo.linked)
	})

	t.Run("an unknown initiative is a 404", func(t *testing.T) {
		svc, initRepo, projRepo := newSvc()
		p := uuid.New()
		projRepo.items[p] = &domain.Project{ID: p, WorkspaceID: ownWS}

		require.Error(t, svc.LinkProject(context.Background(), uuid.New(), p))
		assert.Empty(t, initRepo.linked)
	})
}
