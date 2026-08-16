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
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
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
func (r *authTestUserRepo) SearchAddableUsers(_ context.Context, _ uuid.UUID, _ string, _ int) ([]domain.User, error) {
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

// authTestRefreshTokenRepo is an in-memory RefreshTokenRepository — real
// enough to exercise rotation and reuse detection (RefreshTokens' whole
// point), unlike the previous no-op stub which could never distinguish a
// live token from a revoked or unknown one.
type authTestRefreshTokenRepo struct {
	mu     sync.Mutex
	tokens map[string]*repository.RefreshToken
}

func newAuthTestRefreshTokenRepo() *authTestRefreshTokenRepo {
	return &authTestRefreshTokenRepo{tokens: make(map[string]*repository.RefreshToken)}
}
func (r *authTestRefreshTokenRepo) Create(_ context.Context, userID uuid.UUID, tokenHash string, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens[tokenHash] = &repository.RefreshToken{UserID: userID, TokenHash: tokenHash, ExpiresAt: expiresAt}
	return nil
}
func (r *authTestRefreshTokenRepo) GetByHash(_ context.Context, tokenHash string) (*repository.RefreshToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tokens[tokenHash], nil
}
func (r *authTestRefreshTokenRepo) RevokeByUserID(_ context.Context, userID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for _, t := range r.tokens {
		if t.UserID == userID && t.RevokedAt == nil {
			t.RevokedAt = &now
		}
	}
	return nil
}
func (r *authTestRefreshTokenRepo) RevokeByHash(_ context.Context, tokenHash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tokens[tokenHash]; ok {
		now := time.Now()
		t.RevokedAt = &now
	}
	return nil
}
func (r *authTestRefreshTokenRepo) DeleteExpired(_ context.Context) error { return nil }

type authTestWorkspaceRepo struct{ mu sync.Mutex }

func (r *authTestWorkspaceRepo) Create(_ context.Context, _ *domain.Workspace) error { return nil }
func (r *authTestWorkspaceRepo) GetByID(_ context.Context, _ uuid.UUID) (*domain.Workspace, error) {
	return nil, nil
}
func (r *authTestWorkspaceRepo) GetBySlug(_ context.Context, _ string) (*domain.Workspace, error) {
	return nil, nil
}
func (r *authTestWorkspaceRepo) Update(_ context.Context, _ *domain.Workspace) error { return nil }
func (r *authTestWorkspaceRepo) Delete(_ context.Context, _ uuid.UUID) error         { return nil }
func (r *authTestWorkspaceRepo) ListByOwner(_ context.Context, _ uuid.UUID) ([]domain.Workspace, error) {
	return nil, nil
}
func (r *authTestWorkspaceRepo) ListForUser(_ context.Context, _ uuid.UUID) ([]domain.Workspace, error) {
	return nil, nil
}

type authTestWorkspaceMemberRepo struct{}

func (authTestWorkspaceMemberRepo) Create(_ context.Context, _ *domain.WorkspaceMember) error {
	return nil
}
func (authTestWorkspaceMemberRepo) GetByWorkspaceAndUser(_ context.Context, _, _ uuid.UUID) (*domain.WorkspaceMember, error) {
	return nil, nil
}
func (authTestWorkspaceMemberRepo) GetRole(_ context.Context, _, _ uuid.UUID) (string, error) {
	return "", nil
}
func (authTestWorkspaceMemberRepo) List(_ context.Context, _ uuid.UUID) ([]domain.WorkspaceMemberWithUser, error) {
	return nil, nil
}
func (authTestWorkspaceMemberRepo) ListWithProjects(_ context.Context, _ uuid.UUID) ([]repository.HumanWithProjects, error) {
	return nil, nil
}
func (authTestWorkspaceMemberRepo) UpdateRole(_ context.Context, _, _ uuid.UUID, _ string) error {
	return nil
}
func (authTestWorkspaceMemberRepo) Delete(_ context.Context, _, _ uuid.UUID) error { return nil }
func (authTestWorkspaceMemberRepo) CountOwners(_ context.Context, _ uuid.UUID) (int, error) {
	return 1, nil
}

func newAuthHandlerTest(userRepo *authTestUserRepo, opts ...auth.Option) (*AuthHandler, *echo.Echo) {
	authSvc := auth.NewService(userRepo, newAuthTestRefreshTokenRepo(), &authTestWorkspaceRepo{}, authTestWorkspaceMemberRepo{}, "test-secret-key-for-jwt-signing-32b", opts...)
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

// findRefreshCookie extracts the refresh_token cookie from a recorded
// response, failing the test if it is missing.
func findRefreshCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == refreshCookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in response; Set-Cookie headers: %v", refreshCookieName, rec.Result().Header.Values("Set-Cookie"))
	return nil
}

func TestAuthHandler_Register_SetsHttpOnlyCookie_NotInBody(t *testing.T) {
	h, e := newAuthHandlerTest(newAuthTestUserRepo())

	body := `{"email":"cookie-register@example.com","password":"StrongP4ss","name":"Cookie Test"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.Register(c))
	require.Equal(t, http.StatusCreated, rec.Code)

	// The refresh token must never appear in the JSON body — only the cookie.
	assert.NotContains(t, rec.Body.String(), "refresh_token",
		"refresh_token leaked into the response body; it must only travel via the httpOnly cookie")

	cookie := findRefreshCookie(t, rec)
	assert.True(t, cookie.HttpOnly, "refresh cookie must be HttpOnly — JS must never read it")
	assert.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
	assert.Equal(t, refreshCookiePath, cookie.Path, "cookie must be scoped to the refresh endpoint, not sent on every request")
	assert.NotEmpty(t, cookie.Value)
	assert.Greater(t, cookie.MaxAge, 0)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	tokens, ok := resp["tokens"].(map[string]any)
	require.True(t, ok)
	assert.NotEmpty(t, tokens["access_token"])
	_, hasRefresh := tokens["refresh_token"]
	assert.False(t, hasRefresh, "tokens object must not carry refresh_token in the body")
}

func TestAuthHandler_Login_SetsHttpOnlyCookie_NotInBody(t *testing.T) {
	userRepo := newAuthTestUserRepo()
	h, e := newAuthHandlerTest(userRepo)

	// Seed a user via Register first (through the handler, so hashing matches).
	regBody := `{"email":"cookie-login@example.com","password":"StrongP4ss","name":"Cookie Login"}`
	regReq := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(regBody))
	regReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	regRec := httptest.NewRecorder()
	require.NoError(t, h.Register(e.NewContext(regReq, regRec)))
	require.Equal(t, http.StatusCreated, regRec.Code)

	loginBody := `{"email":"cookie-login@example.com","password":"StrongP4ss"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(loginBody))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	require.NoError(t, h.Login(e.NewContext(req, rec)))
	require.Equal(t, http.StatusOK, rec.Code)

	assert.NotContains(t, rec.Body.String(), "refresh_token")
	cookie := findRefreshCookie(t, rec)
	assert.True(t, cookie.HttpOnly)
}

func TestAuthHandler_Refresh_MissingCookie_Returns401(t *testing.T) {
	h, e := newAuthHandlerTest(newAuthTestUserRepo())

	req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	rec := httptest.NewRecorder()

	require.NoError(t, h.Refresh(e.NewContext(req, rec)))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthHandler_Refresh_RotatesCookie(t *testing.T) {
	h, e := newAuthHandlerTest(newAuthTestUserRepo())

	regBody := `{"email":"cookie-refresh@example.com","password":"StrongP4ss","name":"Refresh Test"}`
	regReq := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(regBody))
	regReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	regRec := httptest.NewRecorder()
	require.NoError(t, h.Register(e.NewContext(regReq, regRec)))
	firstCookie := findRefreshCookie(t, regRec)

	req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	req.AddCookie(&http.Cookie{Name: refreshCookieName, Value: firstCookie.Value})
	rec := httptest.NewRecorder()
	require.NoError(t, h.Refresh(e.NewContext(req, rec)))
	require.Equal(t, http.StatusOK, rec.Code)

	rotated := findRefreshCookie(t, rec)
	assert.NotEqual(t, firstCookie.Value, rotated.Value, "refresh must rotate the token, not reissue the same one")
}

func TestAuthHandler_Refresh_ReusedToken_ClearsCookieAndReturns401(t *testing.T) {
	h, e := newAuthHandlerTest(newAuthTestUserRepo())

	regBody := `{"email":"cookie-reuse@example.com","password":"StrongP4ss","name":"Reuse Test"}`
	regReq := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(regBody))
	regReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	regRec := httptest.NewRecorder()
	require.NoError(t, h.Register(e.NewContext(regReq, regRec)))
	firstCookie := findRefreshCookie(t, regRec)

	// First use rotates it.
	req1 := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	req1.AddCookie(&http.Cookie{Name: refreshCookieName, Value: firstCookie.Value})
	rec1 := httptest.NewRecorder()
	require.NoError(t, h.Refresh(e.NewContext(req1, rec1)))
	require.Equal(t, http.StatusOK, rec1.Code)

	// Presenting the now-revoked original token again must trip reuse
	// detection (the whole point of AC7 — a tolerant server would defeat
	// the theft-detection this task's Web Locks coordination relies on).
	req2 := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	req2.AddCookie(&http.Cookie{Name: refreshCookieName, Value: firstCookie.Value})
	rec2 := httptest.NewRecorder()
	require.NoError(t, h.Refresh(e.NewContext(req2, rec2)))
	assert.Equal(t, http.StatusUnauthorized, rec2.Code)

	cleared := findRefreshCookie(t, rec2)
	assert.LessOrEqual(t, cleared.MaxAge, 0, "a reuse-detected refresh must clear the client cookie, not leave the dead token in place")
}

func TestAuthHandler_Logout_ClearsCookie(t *testing.T) {
	userRepo := newAuthTestUserRepo()
	h, e := newAuthHandlerTest(userRepo)

	regBody := `{"email":"cookie-logout@example.com","password":"StrongP4ss","name":"Logout Test"}`
	regReq := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(regBody))
	regReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	regRec := httptest.NewRecorder()
	require.NoError(t, h.Register(e.NewContext(regReq, regRec)))

	var regResp map[string]any
	require.NoError(t, json.Unmarshal(regRec.Body.Bytes(), &regResp))
	user := regResp["user"].(map[string]any)
	userID, err := uuid.Parse(user["id"].(string))
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(mw.ContextKeyUserID, userID)

	require.NoError(t, h.Logout(c))
	require.Equal(t, http.StatusOK, rec.Code)

	cleared := findRefreshCookie(t, rec)
	assert.LessOrEqual(t, cleared.MaxAge, 0)
}
