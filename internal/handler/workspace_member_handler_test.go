package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// MockWorkspaceMemberService implements service.WorkspaceMemberService.
type MockWorkspaceMemberService struct {
	ListMembersFunc         func(ctx context.Context, workspaceID uuid.UUID) ([]domain.WorkspaceMemberWithUser, error)
	GetMemberFunc           func(ctx context.Context, workspaceID, userID uuid.UUID) (*domain.WorkspaceMemberWithUser, error)
	AddMemberFunc           func(ctx context.Context, workspaceID uuid.UUID, email, role string, invitedBy uuid.UUID) (*domain.WorkspaceMemberWithUser, error)
	AddMemberWithCreateFunc func(ctx context.Context, workspaceID uuid.UUID, email, name, role, password string, invitedBy uuid.UUID) (*domain.WorkspaceMemberWithUser, error)
	UpdateMemberRoleFunc    func(ctx context.Context, workspaceID, targetUserID uuid.UUID, newRole string) error
	SetDisplayNameFunc      func(ctx context.Context, workspaceID, targetUserID uuid.UUID, name string) error
	RemoveMemberFunc        func(ctx context.Context, workspaceID, targetUserID uuid.UUID) error
	GetMyRoleFunc           func(ctx context.Context, workspaceID, userID uuid.UUID) (string, error)
	SearchUsersFunc         func(ctx context.Context, workspaceID, callerID uuid.UUID, query string) ([]domain.UserWithMemberStatus, error)
}

func (m *MockWorkspaceMemberService) ListMembers(ctx context.Context, workspaceID uuid.UUID) ([]domain.WorkspaceMemberWithUser, error) {
	if m.ListMembersFunc != nil {
		return m.ListMembersFunc(ctx, workspaceID)
	}
	return nil, nil
}

func (m *MockWorkspaceMemberService) GetMember(ctx context.Context, workspaceID, userID uuid.UUID) (*domain.WorkspaceMemberWithUser, error) {
	if m.GetMemberFunc != nil {
		return m.GetMemberFunc(ctx, workspaceID, userID)
	}
	return nil, nil
}

func (m *MockWorkspaceMemberService) AddMember(ctx context.Context, workspaceID uuid.UUID, email, role string, invitedBy uuid.UUID) (*domain.WorkspaceMemberWithUser, error) {
	if m.AddMemberFunc != nil {
		return m.AddMemberFunc(ctx, workspaceID, email, role, invitedBy)
	}
	return nil, nil
}

func (m *MockWorkspaceMemberService) AddMemberWithCreate(ctx context.Context, workspaceID uuid.UUID, email, name, role, password string, invitedBy uuid.UUID) (*domain.WorkspaceMemberWithUser, error) {
	if m.AddMemberWithCreateFunc != nil {
		return m.AddMemberWithCreateFunc(ctx, workspaceID, email, name, role, password, invitedBy)
	}
	return nil, nil
}

func (m *MockWorkspaceMemberService) UpdateMemberRole(ctx context.Context, workspaceID, targetUserID uuid.UUID, newRole string) error {
	if m.UpdateMemberRoleFunc != nil {
		return m.UpdateMemberRoleFunc(ctx, workspaceID, targetUserID, newRole)
	}
	return nil
}

func (m *MockWorkspaceMemberService) SetMemberDisplayName(ctx context.Context, workspaceID, targetUserID uuid.UUID, name string) error {
	if m.SetDisplayNameFunc != nil {
		return m.SetDisplayNameFunc(ctx, workspaceID, targetUserID, name)
	}
	return nil
}

func (m *MockWorkspaceMemberService) RemoveMember(ctx context.Context, workspaceID, targetUserID uuid.UUID) error {
	if m.RemoveMemberFunc != nil {
		return m.RemoveMemberFunc(ctx, workspaceID, targetUserID)
	}
	return nil
}

func (m *MockWorkspaceMemberService) GetMyRole(ctx context.Context, workspaceID, userID uuid.UUID) (string, error) {
	if m.GetMyRoleFunc != nil {
		return m.GetMyRoleFunc(ctx, workspaceID, userID)
	}
	return domain.RoleMember, nil
}

func (m *MockWorkspaceMemberService) SearchUsers(ctx context.Context, workspaceID, callerID uuid.UUID, query string) ([]domain.UserWithMemberStatus, error) {
	if m.SearchUsersFunc != nil {
		return m.SearchUsersFunc(ctx, workspaceID, callerID, query)
	}
	return nil, nil
}

// ---------------------------------------------------------------------------

func memberFixture(wsID, userID uuid.UUID) *domain.WorkspaceMemberWithUser {
	return &domain.WorkspaceMemberWithUser{
		WorkspaceMember: domain.WorkspaceMember{
			ID: uuid.New(), WorkspaceID: wsID, UserID: userID, Role: domain.RoleMember,
		},
		User: domain.UserBrief{
			ID: userID, Email: "guest@example.com", Name: "Guest Person",
			Username: "guest", AvatarURL: "",
		},
	}
}

func newMemberReq(t *testing.T, method, body string, wsID, userID, callerID uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, "/", http.NoBody)
	} else {
		req = httptest.NewRequest(method, "/", strings.NewReader(body))
	}
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("ws_id", "user_id")
	c.SetParamValues(wsID.String(), userID.String())
	if callerID != uuid.Nil {
		c.Set("user_id", callerID)
	}
	return c, rec
}

// ---------------------------------------------------------------------------
// Add
// ---------------------------------------------------------------------------

// The name has to survive the handler: dropping it is what stored an address in
// display_name for every provisioned account.
func TestWorkspaceMemberHandler_Add_ForwardsTheName(t *testing.T) {
	wsID, userID := uuid.New(), uuid.New()
	var gotName, gotEmail, gotPassword string

	svc := &MockWorkspaceMemberService{
		AddMemberWithCreateFunc: func(_ context.Context, _ uuid.UUID, email, name, role, password string, _ uuid.UUID) (*domain.WorkspaceMemberWithUser, error) {
			gotEmail, gotName, gotPassword = email, name, password
			assert.Equal(t, domain.RoleMember, role)
			return memberFixture(wsID, userID), nil
		},
	}
	h := NewWorkspaceMemberHandler(svc)

	c, rec := newMemberReq(t, http.MethodPost,
		`{"email":"guest@example.com","name":"Guest Person","role":"member","password":"StrongP4ss"}`,
		wsID, userID, uuid.New())

	require.NoError(t, h.Add(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "guest@example.com", gotEmail)
	assert.Equal(t, "Guest Person", gotName)
	assert.Equal(t, "StrongP4ss", gotPassword)

	var out domain.WorkspaceMemberWithUser
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, "Guest Person", out.User.Name)
	assert.Equal(t, "guest", out.User.Username, "the payload must carry the username")
}

func TestWorkspaceMemberHandler_Add_RejectsABadWorkspaceID(t *testing.T) {
	h := NewWorkspaceMemberHandler(&MockWorkspaceMemberService{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"email":"a@b.c"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("ws_id")
	c.SetParamValues("not-a-uuid")

	require.NoError(t, h.Add(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// ---------------------------------------------------------------------------
// UpdateRole / name
// ---------------------------------------------------------------------------

// The web client splices this response into its member list, so answering with
// a status envelope blanked the row on every role change.
func TestWorkspaceMemberHandler_Update_ReturnsTheUpdatedMember(t *testing.T) {
	wsID, userID := uuid.New(), uuid.New()
	var gotRole string

	svc := &MockWorkspaceMemberService{
		UpdateMemberRoleFunc: func(_ context.Context, _, _ uuid.UUID, newRole string) error {
			gotRole = newRole
			return nil
		},
		GetMemberFunc: func(_ context.Context, _, _ uuid.UUID) (*domain.WorkspaceMemberWithUser, error) {
			m := memberFixture(wsID, userID)
			m.Role = domain.RoleAdmin
			return m, nil
		},
	}
	h := NewWorkspaceMemberHandler(svc)

	c, rec := newMemberReq(t, http.MethodPatch, `{"role":"admin"}`, wsID, userID, uuid.New())
	require.NoError(t, h.UpdateRole(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, domain.RoleAdmin, gotRole)

	var out domain.WorkspaceMemberWithUser
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, domain.RoleAdmin, out.Role)
	assert.Equal(t, "Guest Person", out.User.Name, "the row must come back intact, not as {status:updated}")
}

func TestWorkspaceMemberHandler_Update_SetsTheDisplayName(t *testing.T) {
	wsID, userID := uuid.New(), uuid.New()
	var gotName string
	roleTouched := false

	svc := &MockWorkspaceMemberService{
		UpdateMemberRoleFunc: func(_ context.Context, _, _ uuid.UUID, _ string) error {
			roleTouched = true
			return nil
		},
		SetDisplayNameFunc: func(_ context.Context, _, _ uuid.UUID, name string) error {
			gotName = name
			return nil
		},
		GetMemberFunc: func(_ context.Context, _, _ uuid.UUID) (*domain.WorkspaceMemberWithUser, error) {
			return memberFixture(wsID, userID), nil
		},
	}
	h := NewWorkspaceMemberHandler(svc)

	c, rec := newMemberReq(t, http.MethodPatch, `{"name":"Konstantin M."}`, wsID, userID, uuid.New())
	require.NoError(t, h.UpdateRole(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "Konstantin M.", gotName)
	assert.False(t, roleTouched, "a body with no role must not reach the role path at all")
}

// The refusal has to reach the client as a refusal — this is the cross-tenant
// guard on renaming somebody who has chosen their own name.
func TestWorkspaceMemberHandler_Update_PropagatesARefusedRename(t *testing.T) {
	wsID, userID := uuid.New(), uuid.New()

	svc := &MockWorkspaceMemberService{
		SetDisplayNameFunc: func(_ context.Context, _, _ uuid.UUID, _ string) error {
			return apierror.Forbidden("this member set their own display name — only they can change it")
		},
	}
	h := NewWorkspaceMemberHandler(svc)

	c, rec := newMemberReq(t, http.MethodPatch, `{"name":"Something Else"}`, wsID, userID, uuid.New())
	_ = h.UpdateRole(c)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestWorkspaceMemberHandler_Update_404sWhenTheMemberVanished(t *testing.T) {
	wsID, userID := uuid.New(), uuid.New()

	svc := &MockWorkspaceMemberService{
		GetMemberFunc: func(_ context.Context, _, _ uuid.UUID) (*domain.WorkspaceMemberWithUser, error) {
			return nil, nil
		},
	}
	h := NewWorkspaceMemberHandler(svc)

	c, rec := newMemberReq(t, http.MethodPatch, `{"role":"admin"}`, wsID, userID, uuid.New())
	require.NoError(t, h.UpdateRole(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestWorkspaceMemberHandler_Update_RejectsBadIDs(t *testing.T) {
	h := NewWorkspaceMemberHandler(&MockWorkspaceMemberService{})
	e := echo.New()

	for _, tc := range []struct{ ws, user string }{
		{"not-a-uuid", uuid.New().String()},
		{uuid.New().String(), "not-a-uuid"},
	} {
		req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(`{"role":"admin"}`))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("ws_id", "user_id")
		c.SetParamValues(tc.ws, tc.user)

		require.NoError(t, h.UpdateRole(c))
		assert.Equal(t, http.StatusBadRequest, rec.Code, "ws=%q user=%q", tc.ws, tc.user)
	}
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

// The caller's identity is what bounds the search. Losing it here would widen
// the result set back to "anyone with manage-members sees everyone".
func TestWorkspaceMemberHandler_SearchUsers_PassesTheCallerThrough(t *testing.T) {
	wsID, callerID := uuid.New(), uuid.New()
	var gotWS, gotCaller uuid.UUID
	var gotQuery string

	svc := &MockWorkspaceMemberService{
		SearchUsersFunc: func(_ context.Context, workspaceID, caller uuid.UUID, query string) ([]domain.UserWithMemberStatus, error) {
			gotWS, gotCaller, gotQuery = workspaceID, caller, query
			return []domain.UserWithMemberStatus{{
				UserBrief: domain.UserBrief{ID: uuid.New(), Email: "found@example.com", Name: "Found", Username: "found"},
				IsMember:  false,
			}}, nil
		},
	}
	h := NewWorkspaceMemberHandler(svc)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/?q=found@example.com", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())
	c.Set("user_id", callerID)

	require.NoError(t, h.SearchUsers(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, wsID, gotWS)
	assert.Equal(t, callerID, gotCaller, "the acting user must reach the service")
	assert.Equal(t, "found@example.com", gotQuery)

	// The envelope shape matters: the web store reads resp.users, and typing it
	// as a bare array is what left the dropdown permanently empty.
	var out struct {
		Users []domain.UserWithMemberStatus `json:"users"`
		Count int                           `json:"count"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out.Users, 1)
	assert.Equal(t, 1, out.Count)
	assert.Equal(t, "found", out.Users[0].Username)
	assert.False(t, out.Users[0].IsMember)
}

// An agent key names a workspace, not a person: the handler must pass uuid.Nil
// rather than invent a caller, which is what narrows the search to exact
// addresses for that class of client.
func TestWorkspaceMemberHandler_SearchUsers_NoUserInContextMeansNilCaller(t *testing.T) {
	gotCaller := uuid.New()

	svc := &MockWorkspaceMemberService{
		SearchUsersFunc: func(_ context.Context, _, caller uuid.UUID, _ string) ([]domain.UserWithMemberStatus, error) {
			gotCaller = caller
			return nil, nil
		},
	}
	h := NewWorkspaceMemberHandler(svc)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/?q=someone@example.com", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("ws_id")
	c.SetParamValues(uuid.New().String())

	require.NoError(t, h.SearchUsers(c))
	assert.Equal(t, uuid.Nil, gotCaller)

	// A nil slice must still serialize as [], not null.
	assert.Contains(t, rec.Body.String(), `"users":[]`)
}

func TestWorkspaceMemberHandler_SearchUsers_RejectsABadWorkspaceID(t *testing.T) {
	h := NewWorkspaceMemberHandler(&MockWorkspaceMemberService{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/?q=x", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("ws_id")
	c.SetParamValues("not-a-uuid")

	require.NoError(t, h.SearchUsers(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func TestWorkspaceMemberHandler_List_CarriesNameAndUsername(t *testing.T) {
	wsID, userID := uuid.New(), uuid.New()

	svc := &MockWorkspaceMemberService{
		ListMembersFunc: func(_ context.Context, _ uuid.UUID) ([]domain.WorkspaceMemberWithUser, error) {
			return []domain.WorkspaceMemberWithUser{*memberFixture(wsID, userID)}, nil
		},
	}
	h := NewWorkspaceMemberHandler(svc)

	c, rec := newMemberReq(t, http.MethodGet, "", wsID, userID, uuid.New())
	require.NoError(t, h.List(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var out struct {
		Members []domain.WorkspaceMemberWithUser `json:"members"`
		Count   int                              `json:"count"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out.Members, 1)
	assert.Equal(t, "Guest Person", out.Members[0].User.Name)
	assert.Equal(t, "guest", out.Members[0].User.Username)
	assert.False(t, out.Members[0].User.NameIsPlaceholder())
}

func TestWorkspaceMemberHandler_List_EmptyIsAnArrayNotNull(t *testing.T) {
	h := NewWorkspaceMemberHandler(&MockWorkspaceMemberService{})

	c, rec := newMemberReq(t, http.MethodGet, "", uuid.New(), uuid.New(), uuid.New())
	require.NoError(t, h.List(c))
	assert.Contains(t, rec.Body.String(), `"members":[]`)
}
