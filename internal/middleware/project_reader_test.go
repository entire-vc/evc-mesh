package middleware

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

// runReaderGuard serves GET /api/v1/tasks/:task_id/comments through the guard
// with the given principal and resolver answer.
func runReaderGuard(t *testing.T, authType string, reader uuid.UUID, projects []uuid.UUID, found bool, rerr error) int {
	t.Helper()
	e := echo.New()
	setPrincipal := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(ContextKeyAuthType, authType)
			c.Set(ContextKeyReaderProjectID, reader)
			return next(c)
		}
	}
	resolve := func(context.Context, echo.Context) ([]uuid.UUID, bool, error) { return projects, found, rerr }
	e.GET("/api/v1/tasks/:task_id/comments", func(c echo.Context) error { return c.NoContent(http.StatusOK) },
		setPrincipal, ProjectReaderGuard(resolve))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+uuid.NewString()+"/comments", http.NoBody))
	return rec.Code
}

func TestProjectReaderGuard_ResolverAnswers(t *testing.T) {
	own, other := uuid.New(), uuid.New()
	cases := []struct {
		name     string
		projects []uuid.UUID
		found    bool
		err      error
		want     int
	}{
		{"own project", []uuid.UUID{own}, true, nil, http.StatusOK},
		{"foreign project", []uuid.UUID{other}, true, nil, http.StatusForbidden},
		{"own and foreign", []uuid.UUID{own, other}, true, nil, http.StatusForbidden},
		{"not found", nil, false, nil, http.StatusForbidden},
		{"found but empty", []uuid.UUID{}, true, nil, http.StatusForbidden},
		{"names no project", nil, false, errNoProject, http.StatusForbidden},
		{"no rows", nil, false, sql.ErrNoRows, http.StatusForbidden},
		{"resolver failure fails closed", []uuid.UUID{own}, true, errors.New("db down"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, runReaderGuard(t, AuthTypeAgent, own, tc.projects, tc.found, tc.err))
		})
	}
}

func TestProjectReaderGuard_MarkerCountsOnlyForAgents(t *testing.T) {
	own, other := uuid.New(), uuid.New()
	// A user principal never carries the marker; a stray value is ignored.
	assert.Equal(t, http.StatusOK, runReaderGuard(t, "user", own, []uuid.UUID{other}, true, nil))
	// Red control: the same request as an agent with that marker is refused.
	assert.Equal(t, http.StatusForbidden, runReaderGuard(t, AuthTypeAgent, own, []uuid.UUID{other}, true, nil))
	// A nil marker is not a reader.
	assert.Equal(t, http.StatusOK, runReaderGuard(t, AuthTypeAgent, uuid.Nil, []uuid.UUID{other}, true, nil))
}
