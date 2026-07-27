package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// Minimal in-memory repos — just enough to construct a real *auth.Service for
// the handler tests below. Register's happy path (workspace + membership
// creation) is exercised at the service layer already
// (internal/auth/service_test.go); these tests target the HTTP layer for the
// new registration-gate endpoints: GET /auth/config and the closed-registration
// 403 on POST /auth/register.

type authTestUserRepo struct {
	mu       sync.RWMutex
	users    map[uuid.UUID]*domain.User
	countErr error
}

func newAuthTestUserRepo() *authTestUserRepo {
	return &authTestUserRepo{users: make(map[uuid.UUID]*domain.User)}
}
func (r *authTestUserRepo) Create(_ context.Context, u *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.users[u.ID] = u
	return nil
}
func (r *authTestUserRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.users[id], nil
}
func (r *authTestUserRepo) GetByEmail(_ context.Context, email string) (*domain.User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, u := range r.users {
		if u.Email == email {
			return u, nil
		}
	}
	return nil, nil
}
func (r *authTestUserRepo) Update(_ context.Context, u *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.users[u.ID] = u
	return nil
}
func (r *authTestUserRepo) UsernameExists(_ context.Context, _ string) (bool, error) {
	return false, nil
}
func (r *authTestUserRepo) SearchUsers(_ context.Context, _ string, _ int) ([]domain.User, error) {
	return nil, nil
}
func (r *authTestUserRepo) GetByUsername(_ context.Context, _ uuid.UUID, _ string) (*domain.User, error) {
	return nil, nil
}
func (r *authTestUserRepo) SearchInWorkspace(_ context.Context, _ uuid.UUID, _ string, _ int) ([]domain.User, error) {
	return nil, nil
}
func (r *authTestUserRepo) GetByUsernameGlobal(_ context.Context, _ string) (*domain.User, error) {
	return nil, nil
}
func (r *authTestUserRepo) Count(_ context.Context) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.countErr != nil {
		return 0, r.countErr
	}
	return len(r.users), nil
}

type authTestRefreshTokenRepo struct{}

func (authTestRefreshTokenRepo) Create(_ context.Context, _ uuid.UUID, _ string, _ time.Time) error {
	return nil
}
func (authTestRefreshTokenRepo) GetByHash(_ context.Context, _ string) (*repository.RefreshToken, error) {
	return nil, nil
}
func (authTestRefreshTokenRepo) RevokeByUserID(_ context.Context, _ uuid.UUID) error { return nil }
func (authTestRefreshTokenRepo) RevokeByHash(_ context.Context, _ string) error      { return nil }
func (authTestRefreshTokenRepo) DeleteExpired(_ context.Context) error               { return nil }

func newAuthHandlerTest(userRepo *authTestUserRepo, opts ...auth.Option) (*AuthHandler, *echo.Echo) {
	authSvc := auth.NewService(userRepo, authTestRefreshTokenRepo{}, nil, nil, "test-secret-key-for-jwt-signing-32b", opts...)
	return NewAuthHandler(authSvc), echo.New()
}

func TestAuthHandler_Config_RegistrationOpen(t *testing.T) {
	h, e := newAuthHandlerTest(newAuthTestUserRepo()) // default: allow

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.Config(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var body map[string]bool
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.True(t, body["registration_enabled"])
}

func TestAuthHandler_Config_RegistrationClosed(t *testing.T) {
	userRepo := newAuthTestUserRepo()
	userRepo.Create(context.Background(), &domain.User{ID: uuid.New(), Email: "existing@example.com"})
	h, e := newAuthHandlerTest(userRepo, auth.WithAllowRegistration(false))

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.Config(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var body map[string]bool
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.False(t, body["registration_enabled"])
}

func TestAuthHandler_Config_RegistrationClosedButBootstrapOpen(t *testing.T) {
	// Zero users + closed flag: the handler must report open (matching
	// auth.Service's bootstrap exception), not closed.
	h, e := newAuthHandlerTest(newAuthTestUserRepo(), auth.WithAllowRegistration(false))

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.Config(c))

	var body map[string]bool
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.True(t, body["registration_enabled"], "zero users must report open even with the flag off")
}

func TestAuthHandler_Config_PropagatesRegistrationOpenError(t *testing.T) {
	// Closed registration + an existing user forces RegistrationOpen to call
	// Count; a Count failure must surface as an error response, not a silent
	// true/false default.
	userRepo := newAuthTestUserRepo()
	userRepo.Create(context.Background(), &domain.User{ID: uuid.New(), Email: "existing@example.com"})
	userRepo.countErr = errors.New("count query failed")
	h, e := newAuthHandlerTest(userRepo, auth.WithAllowRegistration(false))

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.Config(c), "handleError writes the response itself, it does not bubble up")
	assert.Equal(t, http.StatusInternalServerError, rec.Code, "a Count failure must surface as a 500, not a silent open/closed default")
}

func TestAuthHandler_Register_ClosedReturns403(t *testing.T) {
	userRepo := newAuthTestUserRepo()
	userRepo.Create(context.Background(), &domain.User{ID: uuid.New(), Email: "existing@example.com"})
	h, e := newAuthHandlerTest(userRepo, auth.WithAllowRegistration(false))

	body := `{"email":"newperson@example.com","password":"StrongP4ss","name":"New Person"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.Register(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "registration is closed")

	stored, err := userRepo.GetByEmail(context.Background(), "newperson@example.com")
	require.NoError(t, err)
	assert.Nil(t, stored, "no user should be created when registration is closed")
}

func TestAuthHandler_Register_MissingFieldsStillValidatedWhenClosed(t *testing.T) {
	// Field validation (email/password/name required) happens before the
	// service is even called — must still fire regardless of the gate.
	h, e := newAuthHandlerTest(newAuthTestUserRepo(), auth.WithAllowRegistration(false))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.Register(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
