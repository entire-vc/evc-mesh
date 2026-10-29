package handler

import (
	"context"
	"fmt"
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
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// memberProfileCall is what the service saw, nil when it was never reached.
type memberProfileCall struct {
	userID uuid.UUID
	upd    domain.MemberProfileUpdate
}

// serveMemberProfile wires PUT /workspaces/:ws_id/members/:user_id/profile the
// way cmd/api/main.go does — RequireSelfUserOrPermission in front of the
// handler — and sends one request as the caller set up by auth.
func serveMemberProfile(t *testing.T, roles map[string]string, wsID, targetID uuid.UUID, body string, auth func(echo.Context)) (*httptest.ResponseRecorder, *memberProfileCall) {
	t.Helper()
	var got *memberProfileCall
	svc := &MockWorkspaceMemberService{
		UpdateProfileFunc: func(_ context.Context, _, userID uuid.UUID, upd domain.MemberProfileUpdate) error {
			got = &memberProfileCall{userID: userID, upd: upd}
			return nil
		},
	}
	h := NewWorkspaceMemberHandler(svc)
	repo := &mockWorkspaceMemberRepo{members: roles}

	e := echo.New()
	authenticate := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(mw.ContextKeyWorkspaceID, wsID)
			auth(c)
			return next(c)
		}
	}
	e.PUT("/workspaces/:ws_id/members/:user_id/profile", h.UpdateProfile, authenticate,
		mw.RequireSelfUserOrPermission("user_id", mw.PermManageMembers, repo))

	req := httptest.NewRequest(http.MethodPut,
		fmt.Sprintf("/workspaces/%s/members/%s/profile", wsID, targetID), strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec, got
}

func asProfileUser(userID uuid.UUID) func(echo.Context) {
	return func(c echo.Context) {
		c.Set(mw.ContextKeyAuthType, mw.AuthTypeUser)
		c.Set(mw.ContextKeyUserID, userID)
	}
}

func TestUpdateMemberProfile_OwnProfile_204(t *testing.T) {
	wsID, me := uuid.New(), uuid.New()
	roles := map[string]string{fmt.Sprintf("%s/%s", wsID, me): domain.RoleMember}

	rec, got := serveMemberProfile(t, roles, wsID, me,
		`{"responsibility_zone":"billing","availability":"async"}`, asProfileUser(me))

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.NotNil(t, got)
	assert.Equal(t, me, got.userID)
	require.NotNil(t, got.upd.ResponsibilityZone)
	assert.Equal(t, "billing", *got.upd.ResponsibilityZone)
	require.NotNil(t, got.upd.Availability)
	assert.Equal(t, "async", *got.upd.Availability)
	assert.Nil(t, got.upd.Capabilities, "a field left out of the body must stay untouched")
}

func TestUpdateMemberProfile_SomeoneElse_WithoutRights_403(t *testing.T) {
	wsID, me, other := uuid.New(), uuid.New(), uuid.New()
	roles := map[string]string{
		fmt.Sprintf("%s/%s", wsID, me):    domain.RoleMember,
		fmt.Sprintf("%s/%s", wsID, other): domain.RoleMember,
	}

	rec, got := serveMemberProfile(t, roles, wsID, other, `{"responsibility_zone":"x"}`, asProfileUser(me))

	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Nil(t, got, "the write must not reach the service")
}

func TestUpdateMemberProfile_SomeoneElse_AsAdmin_204(t *testing.T) {
	wsID, admin, other := uuid.New(), uuid.New(), uuid.New()
	roles := map[string]string{
		fmt.Sprintf("%s/%s", wsID, admin): domain.RoleAdmin,
		fmt.Sprintf("%s/%s", wsID, other): domain.RoleMember,
	}

	rec, got := serveMemberProfile(t, roles, wsID, other, `{"capabilities":["go"]}`, asProfileUser(admin))

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.NotNil(t, got)
	assert.Equal(t, other, got.userID)
	assert.JSONEq(t, `["go"]`, string(got.upd.Capabilities))
}

// An agent has no user id of its own: even a path :user_id equal to its agent
// id is not "self", and agentPerms holds no PermManageMembers.
func TestUpdateMemberProfile_Agent_403(t *testing.T) {
	wsID, agentID := uuid.New(), uuid.New()

	rec, got := serveMemberProfile(t, map[string]string{}, wsID, agentID, `{"responsibility_zone":"x"}`,
		func(c echo.Context) {
			c.Set(mw.ContextKeyAuthType, mw.AuthTypeAgent)
			c.Set(mw.ContextKeyAgentID, agentID)
		})

	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Nil(t, got)
}

func TestUpdateMemberProfile_HandlerRefusals(t *testing.T) {
	wsID, userID := uuid.New(), uuid.New()
	cases := []struct {
		name   string
		ws     string
		user   string
		body   string
		svcErr error
		want   int
	}{
		{"bad workspace id", "nope", userID.String(), `{}`, nil, http.StatusBadRequest},
		{"bad user id", wsID.String(), "nope", `{}`, nil, http.StatusBadRequest},
		{"unparsable body", wsID.String(), userID.String(), `{`, nil, http.StatusBadRequest},
		{"not a member", wsID.String(), userID.String(), `{"availability":"async"}`, apierror.NotFound("WorkspaceMember"), http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewWorkspaceMemberHandler(&MockWorkspaceMemberService{
				UpdateProfileFunc: func(context.Context, uuid.UUID, uuid.UUID, domain.MemberProfileUpdate) error { return tc.svcErr },
			})
			e := echo.New()
			req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(tc.body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetParamNames("ws_id", "user_id")
			c.SetParamValues(tc.ws, tc.user)
			require.NoError(t, h.UpdateProfile(c))
			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}
}
