package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// This file covers who may remove a notification preference.
//
// The workspace itself is guarded before the handler runs, by
// middleware.RequireBodyWorkspace on the route — every caller reaching Delete has
// already been shown to belong to the workspace named in the body. What is left,
// and what is tested here, is the second question: belonging to a workspace is
// not authority over what its other members hear about it.

// --- test doubles ----------------------------------------------------------

type deletePrefCall struct {
	workspaceID uuid.UUID
	userID      *uuid.UUID
	agentID     *uuid.UUID
	channel     string
}

type stubNotificationService struct {
	deleted []deletePrefCall
	err     error
}

func (s *stubNotificationService) Notify(context.Context, domain.NotificationEvent) {}

func (s *stubNotificationService) GetPreferences(context.Context, uuid.UUID) ([]domain.NotificationPreference, error) {
	return nil, nil
}

func (s *stubNotificationService) UpsertPreferences(_ context.Context, p *domain.NotificationPreference) (*domain.NotificationPreference, error) {
	return p, nil
}

func (s *stubNotificationService) DeletePreferences(
	_ context.Context, wsID uuid.UUID, userID, agentID *uuid.UUID, channel string,
) (bool, error) {
	s.deleted = append(s.deleted, deletePrefCall{wsID, userID, agentID, channel})
	if s.err != nil {
		return false, s.err
	}
	return true, nil
}

func (s *stubNotificationService) ListUnread(context.Context, uuid.UUID) ([]domain.Notification, error) {
	return nil, nil
}
func (s *stubNotificationService) CountUnread(context.Context, uuid.UUID) (int, error) { return 0, nil }
func (s *stubNotificationService) MarkRead(context.Context, uuid.UUID, []uuid.UUID) error {
	return nil
}
func (s *stubNotificationService) MarkAllRead(context.Context, uuid.UUID) error { return nil }

// stubMemberRepo answers GetRole and nothing else; the other methods exist to
// satisfy the interface and panic if the handler ever grows a use for them
// without a test.
type stubMemberRepo struct {
	roles map[uuid.UUID]string // keyed by user id
}

func (s *stubMemberRepo) GetRole(_ context.Context, _, userID uuid.UUID) (string, error) {
	if role, ok := s.roles[userID]; ok {
		return role, nil
	}
	return "", errors.New("no membership row")
}

func (s *stubMemberRepo) Create(context.Context, *domain.WorkspaceMember) error { panic("unused") }
func (s *stubMemberRepo) GetByWorkspaceAndUser(context.Context, uuid.UUID, uuid.UUID) (*domain.WorkspaceMember, error) {
	panic("unused")
}
func (s *stubMemberRepo) List(context.Context, uuid.UUID) ([]domain.WorkspaceMemberWithUser, error) {
	panic("unused")
}
func (s *stubMemberRepo) ListWithProjects(context.Context, uuid.UUID) ([]repository.HumanWithProjects, error) {
	panic("unused")
}
func (s *stubMemberRepo) UpdateRole(context.Context, uuid.UUID, uuid.UUID, string) error {
	panic("unused")
}
func (s *stubMemberRepo) Delete(context.Context, uuid.UUID, uuid.UUID) error { panic("unused") }
func (s *stubMemberRepo) CountOwners(context.Context, uuid.UUID) (int, error) {
	panic("unused")
}

type stubWorkspaceRepo struct {
	ownerID uuid.UUID
}

func (s *stubWorkspaceRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Workspace, error) {
	return &domain.Workspace{ID: id, OwnerID: s.ownerID}, nil
}
func (s *stubWorkspaceRepo) Create(context.Context, *domain.Workspace) error { panic("unused") }
func (s *stubWorkspaceRepo) GetBySlug(context.Context, string) (*domain.Workspace, error) {
	panic("unused")
}
func (s *stubWorkspaceRepo) Update(context.Context, *domain.Workspace) error { panic("unused") }
func (s *stubWorkspaceRepo) Delete(context.Context, uuid.UUID) error         { panic("unused") }
func (s *stubWorkspaceRepo) ListByOwner(context.Context, uuid.UUID) ([]domain.Workspace, error) {
	panic("unused")
}
func (s *stubWorkspaceRepo) ListForUser(context.Context, uuid.UUID) ([]domain.Workspace, error) {
	panic("unused")
}

// deletePrefAs issues DELETE /notifications/preferences as the given actor.
// caller is either a user id or, when asAgent is set, an agent id.
func deletePrefAs(t *testing.T, h *NotificationHandler, caller uuid.UUID, asAgent bool, body string) *httptest.ResponseRecorder {
	t.Helper()

	e := echo.New()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/notifications/preferences", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if asAgent {
		c.Set(mw.ContextKeyAuthType, mw.AuthTypeAgent)
		c.Set(mw.ContextKeyAgentID, caller)
	} else {
		c.Set("user_id", caller)
	}

	require.NoError(t, h.Delete(c))
	return rec
}

// --- tests -----------------------------------------------------------------

// TestNotificationPrefs_DeleteOwnIsAllowed: naming nobody means yourself, which
// is the ordinary case and the one that was impossible before this endpoint
// existed — a subscription could be created through the API and removed through
// none of it.
func TestNotificationPrefs_DeleteOwnIsAllowed(t *testing.T) {
	user := uuid.New()
	wsID := uuid.New()
	svc := &stubNotificationService{}
	h := NewNotificationHandler(svc, &stubMemberRepo{roles: map[uuid.UUID]string{user: domain.RoleMember}}, &stubWorkspaceRepo{})

	rec := deletePrefAs(t, h, user, false,
		`{"workspace_id":"`+wsID.String()+`","channel":"web_push"}`)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	require.Len(t, svc.deleted, 1)
	assert.Equal(t, wsID, svc.deleted[0].workspaceID)
	require.NotNil(t, svc.deleted[0].userID)
	assert.Equal(t, user, *svc.deleted[0].userID, "the delete was not scoped to the caller")
	assert.Equal(t, "web_push", svc.deleted[0].channel)
}

// TestNotificationPrefs_DeleteOtherAsPlainMemberIsRefused is the one that matters.
// The caller is a genuine member of this workspace — RequireBodyWorkspace let them
// through, correctly — and that is not authority to unsubscribe a colleague.
func TestNotificationPrefs_DeleteOtherAsPlainMemberIsRefused(t *testing.T) {
	member := uuid.New()
	victim := uuid.New()
	wsID := uuid.New()

	svc := &stubNotificationService{}
	h := NewNotificationHandler(svc,
		&stubMemberRepo{roles: map[uuid.UUID]string{member: domain.RoleMember}},
		&stubWorkspaceRepo{ownerID: uuid.New()})

	rec := deletePrefAs(t, h, member, false,
		`{"workspace_id":"`+wsID.String()+`","channel":"web_push","user_id":"`+victim.String()+`"}`)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Empty(t, svc.deleted, "an ordinary member removed another member's subscription")
}

// TestNotificationPrefs_DeleteOtherAsAdminIsAllowed: the incident-response lever.
// An owner or admin can already remove the member outright and the row cascades
// with them, so naming the row grants no new authority — only a less destructive
// way to apply it.
func TestNotificationPrefs_DeleteOtherAsAdminIsAllowed(t *testing.T) {
	for _, role := range []string{domain.RoleOwner, domain.RoleAdmin} {
		t.Run(role, func(t *testing.T) {
			admin := uuid.New()
			target := uuid.New()
			wsID := uuid.New()

			svc := &stubNotificationService{}
			h := NewNotificationHandler(svc,
				&stubMemberRepo{roles: map[uuid.UUID]string{admin: role}},
				&stubWorkspaceRepo{ownerID: uuid.New()})

			rec := deletePrefAs(t, h, admin, false,
				`{"workspace_id":"`+wsID.String()+`","channel":"browser_push","user_id":"`+target.String()+`"}`)

			assert.Equal(t, http.StatusNoContent, rec.Code)
			require.Len(t, svc.deleted, 1)
			require.NotNil(t, svc.deleted[0].userID)
			assert.Equal(t, target, *svc.deleted[0].userID)
			assert.Equal(t, "browser_push", svc.deleted[0].channel)
		})
	}
}

// TestNotificationPrefs_DeleteOtherAsLegacyOwnerIsAllowed covers the fallback that
// exists because the workspaces created before membership rows were written have
// an owner_id and no workspace_members row at all. Without it the owner of such a
// workspace could not evict a subscription from their own workspace.
func TestNotificationPrefs_DeleteOtherAsLegacyOwnerIsAllowed(t *testing.T) {
	owner := uuid.New()
	target := uuid.New()
	wsID := uuid.New()

	svc := &stubNotificationService{}
	h := NewNotificationHandler(svc,
		&stubMemberRepo{roles: map[uuid.UUID]string{}}, // no membership row anywhere
		&stubWorkspaceRepo{ownerID: owner})

	rec := deletePrefAs(t, h, owner, false,
		`{"workspace_id":"`+wsID.String()+`","channel":"web_push","user_id":"`+target.String()+`"}`)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Len(t, svc.deleted, 1)
}

// TestNotificationPrefs_AgentDeletesOwnOnly: an agent key is scoped to a
// workspace, not granted a role in it. It may remove its own preference and
// nobody else's — there is no role to read that would say otherwise.
func TestNotificationPrefs_AgentDeletesOwnOnly(t *testing.T) {
	agentID := uuid.New()
	wsID := uuid.New()

	t.Run("its own", func(t *testing.T) {
		svc := &stubNotificationService{}
		h := NewNotificationHandler(svc, &stubMemberRepo{}, &stubWorkspaceRepo{})

		rec := deletePrefAs(t, h, agentID, true,
			`{"workspace_id":"`+wsID.String()+`","channel":"web_push"}`)

		assert.Equal(t, http.StatusNoContent, rec.Code)
		require.Len(t, svc.deleted, 1)
		require.NotNil(t, svc.deleted[0].agentID)
		assert.Equal(t, agentID, *svc.deleted[0].agentID)
		assert.Nil(t, svc.deleted[0].userID)
	})

	t.Run("somebody else's", func(t *testing.T) {
		svc := &stubNotificationService{}
		h := NewNotificationHandler(svc, &stubMemberRepo{}, &stubWorkspaceRepo{ownerID: uuid.New()})

		rec := deletePrefAs(t, h, agentID, true,
			`{"workspace_id":"`+wsID.String()+`","channel":"web_push","user_id":"`+uuid.New().String()+`"}`)

		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Empty(t, svc.deleted, "an agent key removed a user's subscription")
	})

	t.Run("another agent's", func(t *testing.T) {
		svc := &stubNotificationService{}
		h := NewNotificationHandler(svc, &stubMemberRepo{}, &stubWorkspaceRepo{ownerID: uuid.New()})

		rec := deletePrefAs(t, h, agentID, true,
			`{"workspace_id":"`+wsID.String()+`","channel":"web_push","agent_id":"`+uuid.New().String()+`"}`)

		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Empty(t, svc.deleted)
	})
}

// TestNotificationPrefs_DeleteRejectsMalformedBodies keeps the parsing from
// falling through to a delete with the wrong subject. Naming both ids is the
// interesting one: silently preferring either would delete a row the caller did
// not name.
func TestNotificationPrefs_DeleteRejectsMalformedBodies(t *testing.T) {
	user := uuid.New()
	wsID := uuid.New()

	cases := []struct {
		name string
		body string
	}{
		{"no workspace", `{"channel":"web_push"}`},
		{"unparseable workspace", `{"workspace_id":"not-a-uuid"}`},
		{"unparseable user", `{"workspace_id":"` + wsID.String() + `","user_id":"nope"}`},
		{"unparseable agent", `{"workspace_id":"` + wsID.String() + `","agent_id":"nope"}`},
		{"both subjects", `{"workspace_id":"` + wsID.String() + `","user_id":"` + uuid.New().String() +
			`","agent_id":"` + uuid.New().String() + `"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &stubNotificationService{}
			h := NewNotificationHandler(svc,
				&stubMemberRepo{roles: map[uuid.UUID]string{user: domain.RoleOwner}},
				&stubWorkspaceRepo{ownerID: user})

			rec := deletePrefAs(t, h, user, false, tc.body)

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Empty(t, svc.deleted, "a malformed body reached the delete")
		})
	}
}

// TestNotificationPrefs_DeleteDefaultsChannel: the PUT defaults to web_push when
// the channel is omitted, so the DELETE has to agree — otherwise the row created
// by a default PUT could not be removed by a default DELETE.
func TestNotificationPrefs_DeleteDefaultsChannel(t *testing.T) {
	user := uuid.New()
	wsID := uuid.New()
	svc := &stubNotificationService{}
	h := NewNotificationHandler(svc, &stubMemberRepo{}, &stubWorkspaceRepo{})

	rec := deletePrefAs(t, h, user, false, `{"workspace_id":"`+wsID.String()+`"}`)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	require.Len(t, svc.deleted, 1)
	assert.Equal(t, "web_push", svc.deleted[0].channel)
}

// TestNotificationPrefs_DeleteIsIdempotent: removing a subscription that is not
// there is a success, not a 404. Reporting "no such row" would tell a caller
// entitled to ask whether a given person is subscribed, which is a fact about
// somebody else.
func TestNotificationPrefs_DeleteIsIdempotent(t *testing.T) {
	user := uuid.New()
	wsID := uuid.New()
	svc := &stubNotificationService{}
	h := NewNotificationHandler(svc, &stubMemberRepo{}, &stubWorkspaceRepo{})

	rec := deletePrefAs(t, h, user, false, `{"workspace_id":"`+wsID.String()+`","channel":"web_push"}`)
	assert.Equal(t, http.StatusNoContent, rec.Code)

	rec = deletePrefAs(t, h, user, false, `{"workspace_id":"`+wsID.String()+`","channel":"web_push"}`)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

// TestNotificationPrefs_DeleteUnauthenticated: no identity, no subject to delete.
func TestNotificationPrefs_DeleteUnauthenticated(t *testing.T) {
	wsID := uuid.New()
	svc := &stubNotificationService{}
	h := NewNotificationHandler(svc, &stubMemberRepo{}, &stubWorkspaceRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/notifications/preferences",
		strings.NewReader(`{"workspace_id":"`+wsID.String()+`"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	require.NoError(t, h.Delete(e.NewContext(req, rec)))

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, svc.deleted)
}
