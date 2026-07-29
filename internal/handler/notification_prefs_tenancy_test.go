package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// PUT /notifications/preferences took the workspace to subscribe to out of the
// request body and checked nothing. The path names no tenant, so
// RequireWorkspaceMemberScoped had nothing to resolve and treated it like
// /auth/me; there was no rbac() on the route either, and rbac() would not have
// helped, since on an agent key it never looks at the target workspace.
//
// The result was that any authenticated caller could subscribe to any workspace,
// and the dispatcher then delivered that workspace's comment bodies to them. The
// only thing that made it hard to exploit was that self-registration was closed,
// so a stranger could not get a token in the first place — which is a property of
// the deployment, not of the API.
//
// These tests wire the route the way cmd/api/main.go does, guard included, and
// require the request to die at the guard.

// --- mock service -----------------------------------------------------------

type mockNotificationService struct {
	upsertCalled bool
	deletedPref  uuid.UUID
	deletedUser  uuid.UUID
	deleteErr    error
}

func (m *mockNotificationService) Notify(context.Context, domain.NotificationEvent) {}

func (m *mockNotificationService) GetPreferences(context.Context, uuid.UUID) ([]domain.NotificationPreference, error) {
	return nil, nil
}

func (m *mockNotificationService) UpsertPreferences(_ context.Context, pref *domain.NotificationPreference) (*domain.NotificationPreference, error) {
	m.upsertCalled = true
	return pref, nil
}

func (m *mockNotificationService) DeletePreference(_ context.Context, userID, prefID uuid.UUID) error {
	m.deletedUser, m.deletedPref = userID, prefID
	return m.deleteErr
}

func (m *mockNotificationService) ListUnread(context.Context, uuid.UUID) ([]domain.Notification, error) {
	return nil, nil
}
func (m *mockNotificationService) CountUnread(context.Context, uuid.UUID) (int, error) { return 0, nil }
func (m *mockNotificationService) MarkRead(context.Context, uuid.UUID, []uuid.UUID) error {
	return nil
}
func (m *mockNotificationService) MarkAllRead(context.Context, uuid.UUID) error { return nil }

// --- harness ----------------------------------------------------------------

// putPreferences serves PUT /notifications/preferences through the same guard the
// router puts in front of it, as the given actor.
func putPreferences(t *testing.T, db *sqlx.DB, authType string, actorID uuid.UUID, body string) (*mockNotificationService, *httptest.ResponseRecorder) {
	t.Helper()

	svc := &mockNotificationService{}
	h := NewNotificationHandler(svc)

	e := echo.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(mw.ContextKeyAuthType, authType)
			if authType == mw.AuthTypeAgent {
				c.Set(mw.ContextKeyAgentID, actorID)
			} else {
				c.Set(mw.ContextKeyUserID, actorID)
				c.Set("user_id", actorID)
			}
			return next(c)
		}
	})
	e.PUT("/api/v1/notifications/preferences", h.UpdatePreferences, mw.RequireBodyWorkspace(db))

	req := httptest.NewRequest(http.MethodPut, "/api/v1/notifications/preferences", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return svc, rec
}

func newSQLMock(t *testing.T) (*sqlx.DB, sqlmock.Sqlmock) {
	t.Helper()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	return sqlx.NewDb(raw, "postgres"), mock
}

// --- PUT /notifications/preferences -----------------------------------------

// TestUpdatePreferences_ForeignWorkspaceIsRefused is the reported repro, as a
// user JWT.
func TestUpdatePreferences_ForeignWorkspaceIsRefused(t *testing.T) {
	db, mock := newSQLMock(t)

	victimWS := uuid.New()
	stranger := uuid.New()

	mock.ExpectQuery("SELECT role FROM workspace_members").
		WithArgs(victimWS, stranger).
		WillReturnRows(sqlmock.NewRows([]string{"role"}))
	mock.ExpectQuery("SELECT owner_id FROM workspaces").
		WithArgs(victimWS).
		WillReturnRows(sqlmock.NewRows([]string{"owner_id"}).AddRow(uuid.New()))

	svc, rec := putPreferences(t, db, mw.AuthTypeUser, stranger,
		`{"workspace_id":"`+victimWS.String()+`","channel":"web_push","events":["comment.created"]}`)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, svc.upsertCalled,
		"a subscription to another tenant's workspace was written; the dispatcher would have "+
			"delivered that workspace's comment bodies to a stranger")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestUpdatePreferences_ForeignWorkspaceIsRefusedForAgentKey is not a duplicate.
// An agent key is the credential that walked through every "protected by rbac"
// route in this class, because rbac() short-circuits to a static capability map
// on an agent key and never looks at the workspace being addressed.
func TestUpdatePreferences_ForeignWorkspaceIsRefusedForAgentKey(t *testing.T) {
	db, mock := newSQLMock(t)

	victimWS := uuid.New()
	intruder := uuid.New()

	mock.ExpectQuery("SELECT workspace_id FROM agents").
		WithArgs(intruder).
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(uuid.New()))

	svc, rec := putPreferences(t, db, mw.AuthTypeAgent, intruder,
		`{"workspace_id":"`+victimWS.String()+`"}`)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, svc.upsertCalled, "an agent key subscribed to another tenant's workspace")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestUpdatePreferences_OwnWorkspaceStillWorks is the half that decides whether
// this can ship: the guard reads the body before the handler binds it, so the
// ordinary case has to still arrive intact.
func TestUpdatePreferences_OwnWorkspaceStillWorks(t *testing.T) {
	db, mock := newSQLMock(t)

	ownWS := uuid.New()
	member := uuid.New()

	mock.ExpectQuery("SELECT role FROM workspace_members").
		WithArgs(ownWS, member).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("member"))

	svc, rec := putPreferences(t, db, mw.AuthTypeUser, member,
		`{"workspace_id":"`+ownWS.String()+`","channel":"web_push","events":["comment.created"],"is_enabled":true}`)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, svc.upsertCalled, "the guard swallowed a legitimate subscription")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// --- DELETE /notifications/preferences/:pref_id -----------------------------

// deletePreference serves the unsubscribe route as the given user.
func deletePreference(t *testing.T, svc *mockNotificationService, userID uuid.UUID, prefID string) *httptest.ResponseRecorder {
	t.Helper()

	e := echo.New()
	h := NewNotificationHandler(svc)
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if userID != uuid.Nil {
				c.Set("user_id", userID)
			}
			return next(c)
		}
	})
	e.DELETE("/api/v1/notifications/preferences/:pref_id", h.DeletePreference)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/notifications/preferences/"+prefID, http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// TestDeletePreference_CancelsTheCallersOwnSubscription: the capability that did
// not exist. Preferences could be created and updated and nothing else, so a
// subscription to a workspace the subscriber had no business in was permanent as
// far as the API was concerned.
func TestDeletePreference_CancelsTheCallersOwnSubscription(t *testing.T) {
	svc := &mockNotificationService{}
	userID, prefID := uuid.New(), uuid.New()

	rec := deletePreference(t, svc, userID, prefID.String())

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, prefID, svc.deletedPref)
	assert.Equal(t, userID, svc.deletedUser,
		"the handler did not pass the caller down, so the delete was not scoped to their own rows")
}

// TestDeletePreference_SomebodyElsesIdIs404: the service answers not-found rather
// than forbidden, so the response does not confirm that the id exists.
func TestDeletePreference_SomebodyElsesIdIs404(t *testing.T) {
	svc := &mockNotificationService{deleteErr: apierror.NotFound("notification preference")}

	rec := deletePreference(t, svc, uuid.New(), uuid.New().String())

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestDeletePreference_RequiresAuthentication(t *testing.T) {
	svc := &mockNotificationService{}

	rec := deletePreference(t, svc, uuid.Nil, uuid.New().String())

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, uuid.Nil, svc.deletedPref)
}

func TestDeletePreference_RejectsAMalformedId(t *testing.T) {
	svc := &mockNotificationService{}

	rec := deletePreference(t, svc, uuid.New(), "not-a-uuid")

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, uuid.Nil, svc.deletedPref)
}
