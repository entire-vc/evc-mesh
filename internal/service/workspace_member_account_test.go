package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// recordingRevoker remembers whose sessions were ended.
type recordingRevoker struct {
	mu      sync.Mutex
	revoked []uuid.UUID
	err     error
}

func (r *recordingRevoker) RevokeByUserID(_ context.Context, userID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.revoked = append(r.revoked, userID)
	return nil
}

type accountFixture struct {
	svc        *workspaceMemberService
	users      *MockUserRepository
	members    *minimalWorkspaceMemberRepo
	workspaces *MockWorkspaceRepository
	activity   *MockActivityLogRepository
	revoker    *recordingRevoker
	ws         uuid.UUID
	owner      *domain.User
	admin      *domain.User
	target     *domain.User
}

// newAccountFixture builds one workspace with an owner, an admin and a plain
// member (target), all people.
func newAccountFixture(t *testing.T) *accountFixture {
	t.Helper()
	f := &accountFixture{
		users:      NewMockUserRepository(),
		members:    &minimalWorkspaceMemberRepo{},
		workspaces: NewMockWorkspaceRepository(),
		activity:   NewMockActivityLogRepository(),
		revoker:    &recordingRevoker{},
	}
	f.svc = NewWorkspaceMemberService(f.members, f.users, NewMockProjectMemberRepository(), f.activity, nil).(*workspaceMemberService)
	f.svc.SetAccountEditDeps(f.workspaces, f.revoker)

	f.owner = f.addUser(t, "owner@example.com", "owner")
	f.admin = f.addUser(t, "admin@example.com", "admin")
	f.target = f.addUser(t, "target@example.com", "target")
	f.ws = f.addWorkspace(t, f.owner.ID)
	f.join(f.ws, f.owner.ID, domain.RoleOwner)
	f.join(f.ws, f.admin.ID, domain.RoleAdmin)
	f.join(f.ws, f.target.ID, domain.RoleMember)
	return f
}

func (f *accountFixture) addUser(t *testing.T, email, username string) *domain.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("OldPassw0rd"), bcrypt.MinCost)
	require.NoError(t, err)
	u := &domain.User{ID: uuid.New(), Email: email, Name: username, Username: username, PasswordHash: string(hash), IsActive: true}
	require.NoError(t, f.users.Create(context.Background(), u))
	return u
}

func (f *accountFixture) addWorkspace(t *testing.T, ownerID uuid.UUID) uuid.UUID {
	t.Helper()
	ws := &domain.Workspace{ID: uuid.New(), Name: "ws", Slug: uuid.NewString()[:8], OwnerID: ownerID, CreatedAt: time.Now()}
	require.NoError(t, f.workspaces.Create(context.Background(), ws))
	return ws.ID
}

func (f *accountFixture) join(workspaceID, userID uuid.UUID, role string) {
	_ = f.members.Create(context.Background(), &domain.WorkspaceMember{ID: uuid.New(), WorkspaceID: workspaceID, UserID: userID, Role: role})
	f.workspaces.AddMember(workspaceID, userID)
}

func as(user *domain.User) context.Context {
	return actorctx.WithActor(context.Background(), user.ID, domain.ActorTypeUser)
}

func requireStatus(t *testing.T, err error, status int) {
	t.Helper()
	require.Error(t, err)
	var apiErr *apierror.Error
	require.True(t, errors.As(err, &apiErr), "want an API error, got %v", err)
	assert.Equal(t, status, apiErr.StatusCode(), "error: %v", err)
}

func (f *accountFixture) activityActions() map[string]string {
	f.activity.mu.RLock()
	defer f.activity.mu.RUnlock()
	out := map[string]string{}
	for _, e := range f.activity.items {
		out[e.Action] = string(e.Changes)
	}
	return out
}

// The case the card exists for: an owner sets a member's password, the member
// signs in with it, the old one stops working, their sessions are ended, and
// the audit row names the reset but not the password.
func TestSetMemberPassword_OwnerSetsAWorkingPassword(t *testing.T) {
	f := newAccountFixture(t)

	require.NoError(t, f.svc.SetMemberPassword(as(f.owner), f.ws, f.target.ID, "NewPassw0rd!"))

	stored, err := f.users.GetByID(context.Background(), f.target.ID)
	require.NoError(t, err)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(stored.PasswordHash), []byte("NewPassw0rd!")), "new password must verify")
	assert.Error(t, bcrypt.CompareHashAndPassword([]byte(stored.PasswordHash), []byte("OldPassw0rd")), "old password must stop working")
	assert.Equal(t, []uuid.UUID{f.target.ID}, f.revoker.revoked, "the member's sessions must be ended")

	changes, ok := f.activityActions()["member.password_reset"]
	require.True(t, ok, "password reset must be audited")
	assert.Contains(t, changes, f.target.ID.String())
	assert.NotContains(t, changes, "NewPassw0rd", "the password must never reach the audit log")
	assert.NotContains(t, strings.ToLower(changes), "hash")
}

func TestSetMemberPassword_AdminCanResetAMember(t *testing.T) {
	f := newAccountFixture(t)
	require.NoError(t, f.svc.SetMemberPassword(as(f.admin), f.ws, f.target.ID, "NewPassw0rd!"))
}

func TestSetMemberPassword_EnforcesThePasswordPolicy(t *testing.T) {
	f := newAccountFixture(t)
	requireStatus(t, f.svc.SetMemberPassword(as(f.owner), f.ws, f.target.ID, "short"), http.StatusBadRequest)
	requireStatus(t, f.svc.SetMemberPassword(as(f.owner), f.ws, f.target.ID, "alllowercase1"), http.StatusBadRequest)
	assert.Empty(t, f.revoker.revoked)
}

func TestAccountEdit_MemberIsRefused(t *testing.T) {
	f := newAccountFixture(t)
	peer := f.addUser(t, "peer@example.com", "peer")
	f.join(f.ws, peer.ID, domain.RoleMember)

	requireStatus(t, f.svc.SetMemberPassword(as(f.target), f.ws, peer.ID, "NewPassw0rd!"), http.StatusForbidden)
	requireStatus(t, f.svc.SetMemberEmail(as(f.target), f.ws, peer.ID, "x@example.com"), http.StatusForbidden)
}

func TestAccountEdit_AdminCannotTouchAnOwner(t *testing.T) {
	f := newAccountFixture(t)
	requireStatus(t, f.svc.SetMemberPassword(as(f.admin), f.ws, f.owner.ID, "NewPassw0rd!"), http.StatusForbidden)
	requireStatus(t, f.svc.SetMemberEmail(as(f.admin), f.ws, f.owner.ID, "x@example.com"), http.StatusForbidden)
	assert.Empty(t, f.revoker.revoked)
}

func TestAccountEdit_NotOnYourself(t *testing.T) {
	f := newAccountFixture(t)
	requireStatus(t, f.svc.SetMemberPassword(as(f.owner), f.ws, f.owner.ID, "NewPassw0rd!"), http.StatusBadRequest)
}

func TestAccountEdit_AgentKeyIsRefused(t *testing.T) {
	f := newAccountFixture(t)
	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)
	requireStatus(t, f.svc.SetMemberPassword(ctx, f.ws, f.target.ID, "NewPassw0rd!"), http.StatusForbidden)
}

func TestAccountEdit_ServiceAccountIsRefused(t *testing.T) {
	f := newAccountFixture(t)
	agentID := uuid.New()
	svcUser := f.addUser(t, "hugh@example.com", "hugh")
	svcUser.ServiceAgentID = &agentID
	require.NoError(t, f.users.Update(context.Background(), svcUser))
	f.join(f.ws, svcUser.ID, domain.RoleMember)

	requireStatus(t, f.svc.SetMemberPassword(as(f.owner), f.ws, svcUser.ID, "NewPassw0rd!"), http.StatusForbidden)
}

// Anti-takeover: the target also belongs to a workspace the actor does not
// administer. Resetting their password from here would hand the actor that
// other workspace too.
func TestAccountEdit_CrossWorkspaceTakeoverIsRefused(t *testing.T) {
	f := newAccountFixture(t)
	elsewhere := f.addWorkspace(t, f.target.ID) // target owns another workspace
	f.join(elsewhere, f.target.ID, domain.RoleOwner)

	requireStatus(t, f.svc.SetMemberPassword(as(f.owner), f.ws, f.target.ID, "NewPassw0rd!"), http.StatusForbidden)
	requireStatus(t, f.svc.SetMemberEmail(as(f.owner), f.ws, f.target.ID, "x@example.com"), http.StatusForbidden)

	stored, err := f.users.GetByID(context.Background(), f.target.ID)
	require.NoError(t, err)
	assert.Equal(t, "target@example.com", stored.Email)
	assert.Empty(t, f.revoker.revoked)
}

// The same second workspace is fine when the actor administers it too.
func TestAccountEdit_CrossWorkspaceAllowedWhenActorAdministersBoth(t *testing.T) {
	f := newAccountFixture(t)
	other := f.addWorkspace(t, f.owner.ID)
	f.join(other, f.owner.ID, domain.RoleOwner)
	f.join(other, f.target.ID, domain.RoleMember)

	require.NoError(t, f.svc.SetMemberPassword(as(f.owner), f.ws, f.target.ID, "NewPassw0rd!"))
}

func TestSetMemberEmail_ChangesAndAudits(t *testing.T) {
	f := newAccountFixture(t)

	require.NoError(t, f.svc.SetMemberEmail(as(f.owner), f.ws, f.target.ID, "  New.Address@Example.COM "))

	stored, err := f.users.GetByID(context.Background(), f.target.ID)
	require.NoError(t, err)
	assert.Equal(t, "new.address@example.com", stored.Email, "stored trimmed and lowercased")

	changes, ok := f.activityActions()["member.email_changed"]
	require.True(t, ok)
	assert.Contains(t, changes, "target@example.com")
	assert.Contains(t, changes, "new.address@example.com")
}

func TestSetMemberEmail_DuplicateIsAConflict(t *testing.T) {
	f := newAccountFixture(t)
	requireStatus(t, f.svc.SetMemberEmail(as(f.owner), f.ws, f.target.ID, "Admin@Example.com"), http.StatusConflict)
}

func TestSetMemberEmail_RejectsAnInvalidAddress(t *testing.T) {
	f := newAccountFixture(t)
	requireStatus(t, f.svc.SetMemberEmail(as(f.owner), f.ws, f.target.ID, "not-an-address"), http.StatusBadRequest)
}

func TestSetMemberUsername_ChangesAndAudits(t *testing.T) {
	f := newAccountFixture(t)

	require.NoError(t, f.svc.SetMemberUsername(as(f.owner), f.ws, f.target.ID, "renamed-member"))

	stored, err := f.users.GetByID(context.Background(), f.target.ID)
	require.NoError(t, err)
	assert.Equal(t, "renamed-member", stored.Username)
	_, ok := f.activityActions()["member.username_changed"]
	assert.True(t, ok)
}

func TestSetMemberUsername_RejectsABadHandle(t *testing.T) {
	f := newAccountFixture(t)
	requireStatus(t, f.svc.SetMemberUsername(as(f.owner), f.ws, f.target.ID, "Bad Handle"), http.StatusBadRequest)
}

func TestSetMemberPassword_SessionRevokeFailureIsReported(t *testing.T) {
	f := newAccountFixture(t)
	f.revoker.err = errors.New("db down")
	assert.ErrorIs(t, f.svc.SetMemberPassword(as(f.owner), f.ws, f.target.ID, "NewPassw0rd!"), ErrSessionsNotRevoked)

	// The password did change, so the reset is on the audit trail even though
	// the call reports the failed session revoke.
	stored, err := f.users.GetByID(context.Background(), f.target.ID)
	require.NoError(t, err)
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(stored.PasswordHash), []byte("NewPassw0rd!")))
	_, audited := f.activityActions()["member.password_reset"]
	assert.True(t, audited, "a stored password change must be audited even when revoking sessions fails")
}

// No audit row, no password change: the reset is refused, not done silently.
func TestSetMemberPassword_AuditFailureLeavesThePasswordUnchanged(t *testing.T) {
	f := newAccountFixture(t)
	f.activity.errToReturn = errors.New("activity store down")

	assert.Error(t, f.svc.SetMemberPassword(as(f.owner), f.ws, f.target.ID, "NewPassw0rd!"))

	stored, err := f.users.GetByID(context.Background(), f.target.ID)
	require.NoError(t, err)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(stored.PasswordHash), []byte("OldPassw0rd")), "old password must still work")
	assert.Empty(t, f.revoker.revoked)
}

// Without the workspace lookup the cross-workspace check cannot run, so the
// edit is refused rather than run on a partial check.
func TestAccountEdit_RefusedWhenNotWired(t *testing.T) {
	f := newAccountFixture(t)
	f.svc.workspaceRepo = nil
	requireStatus(t, f.svc.SetMemberPassword(as(f.owner), f.ws, f.target.ID, "NewPassw0rd!"), http.StatusInternalServerError)
}

// flakyAccountUserRepo makes one user-store call fail so the error branches of the
// account edits are exercised.
type flakyAccountUserRepo struct {
	*MockUserRepository
	getByIDErr, getByEmailErr, updateEmailErr error
	hideUser                                  bool
}

func (r *flakyAccountUserRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	if r.getByIDErr != nil {
		return nil, r.getByIDErr
	}
	if r.hideUser {
		return nil, nil
	}
	return r.MockUserRepository.GetByID(ctx, id)
}

func (r *flakyAccountUserRepo) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	if r.getByEmailErr != nil {
		return nil, r.getByEmailErr
	}
	return r.MockUserRepository.GetByEmail(ctx, email)
}

func (r *flakyAccountUserRepo) UpdateEmail(ctx context.Context, userID uuid.UUID, email string) error {
	if r.updateEmailErr != nil {
		return r.updateEmailErr
	}
	return r.MockUserRepository.UpdateEmail(ctx, userID, email)
}

type flakyAccountWorkspaceRepo struct {
	*MockWorkspaceRepository
	listErr error
}

func (r *flakyAccountWorkspaceRepo) ListForUser(context.Context, uuid.UUID) ([]domain.Workspace, error) {
	return nil, r.listErr
}

func (f *accountFixture) failUsers() *flakyAccountUserRepo {
	r := &flakyAccountUserRepo{MockUserRepository: f.users}
	f.svc.userRepo = r
	return r
}

func TestMemberRoleRank_OrdersRolesAndRanksUnknownLowest(t *testing.T) {
	assert.Greater(t, memberRoleRank(domain.RoleOwner), memberRoleRank(domain.RoleAdmin))
	assert.Greater(t, memberRoleRank(domain.RoleAdmin), memberRoleRank(domain.RoleMember))
	assert.Greater(t, memberRoleRank(domain.RoleMember), memberRoleRank(domain.RoleViewer))
	assert.Greater(t, memberRoleRank(domain.RoleViewer), memberRoleRank("superuser"))
	assert.Equal(t, 0, memberRoleRank(""))
}

func TestAccountEdit_NonMemberIsNotFound(t *testing.T) {
	f := newAccountFixture(t)
	stranger := f.addUser(t, "stranger@example.com", "stranger")
	requireStatus(t, f.svc.SetMemberPassword(as(f.owner), f.ws, stranger.ID, "NewPassw0rd!"), http.StatusNotFound)
}

func TestAccountEdit_MissingUserIsNotFound(t *testing.T) {
	f := newAccountFixture(t)
	f.failUsers().hideUser = true
	requireStatus(t, f.svc.SetMemberPassword(as(f.owner), f.ws, f.target.ID, "NewPassw0rd!"), http.StatusNotFound)
}

func TestAccountEdit_StoreErrorsAreReturned(t *testing.T) {
	boom := errors.New("db down")

	f := newAccountFixture(t)
	f.failUsers().getByIDErr = boom
	assert.ErrorIs(t, f.svc.SetMemberPassword(as(f.owner), f.ws, f.target.ID, "NewPassw0rd!"), boom)

	f = newAccountFixture(t)
	f.svc.workspaceRepo = &flakyAccountWorkspaceRepo{MockWorkspaceRepository: f.workspaces, listErr: boom}
	assert.ErrorIs(t, f.svc.SetMemberPassword(as(f.owner), f.ws, f.target.ID, "NewPassw0rd!"), boom)
	assert.Empty(t, f.revoker.revoked)
}

func TestSetMemberEmail_SameAddressIsANoop(t *testing.T) {
	f := newAccountFixture(t)
	require.NoError(t, f.svc.SetMemberEmail(as(f.owner), f.ws, f.target.ID, "Target@Example.com"))
	_, audited := f.activityActions()["member.email_changed"]
	assert.False(t, audited, "an unchanged address is not an email change")
}

func TestSetMemberEmail_LookupErrorIsReturned(t *testing.T) {
	f := newAccountFixture(t)
	boom := errors.New("db down")
	f.failUsers().getByEmailErr = boom
	assert.ErrorIs(t, f.svc.SetMemberEmail(as(f.owner), f.ws, f.target.ID, "new@example.com"), boom)
}

// The unique index can still refuse an address the pre-check saw as free (a
// concurrent signup); that is a 409, not a 500.
func TestSetMemberEmail_RaceOnTheUniqueIndexIsAConflict(t *testing.T) {
	f := newAccountFixture(t)
	f.failUsers().updateEmailErr = repository.ErrEmailTaken
	requireStatus(t, f.svc.SetMemberEmail(as(f.owner), f.ws, f.target.ID, "new@example.com"), http.StatusConflict)

	boom := errors.New("db down")
	f = newAccountFixture(t)
	f.failUsers().updateEmailErr = boom
	assert.ErrorIs(t, f.svc.SetMemberEmail(as(f.owner), f.ws, f.target.ID, "new@example.com"), boom)
}

func TestSetMemberUsername_SameHandleIsANoop(t *testing.T) {
	f := newAccountFixture(t)
	require.NoError(t, f.svc.SetMemberUsername(as(f.owner), f.ws, f.target.ID, "target"))
	_, audited := f.activityActions()["member.username_changed"]
	assert.False(t, audited)
}

func TestSetMemberUsername_TakenHandleIsAConflict(t *testing.T) {
	f := newAccountFixture(t)
	requireStatus(t, f.svc.SetMemberUsername(as(f.owner), f.ws, f.target.ID, "admin"), http.StatusConflict)
}

// A handle that equals an agent slug in one of the member's workspaces would
// make @-mentions ambiguous, so it is refused.
func TestSetMemberUsername_AgentSlugIsAConflict(t *testing.T) {
	f := newAccountFixture(t)
	agents := NewMockAgentRepository()
	require.NoError(t, agents.Create(context.Background(), &domain.Agent{ID: uuid.New(), WorkspaceID: f.ws, Name: "Scout", Slug: "scout"}))
	f.svc.agentRepo = agents

	requireStatus(t, f.svc.SetMemberUsername(as(f.owner), f.ws, f.target.ID, "scout"), http.StatusConflict)
	require.NoError(t, f.svc.SetMemberUsername(as(f.owner), f.ws, f.target.ID, "not-an-agent"))
}

// A password over bcrypt's 72-byte limit is a 400 from the policy, not a 500
// from the hasher.
func TestSetMemberPassword_OverlongPasswordIsABadRequest(t *testing.T) {
	f := newAccountFixture(t)
	requireStatus(t, f.svc.SetMemberPassword(as(f.owner), f.ws, f.target.ID, "Aa1"+strings.Repeat("x", 70)), http.StatusBadRequest)
	assert.Empty(t, f.revoker.revoked)
}
