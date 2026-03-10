package auth

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// ---------------------------------------------------------------------------
// In-memory mock repositories
// ---------------------------------------------------------------------------

type mockUserRepo struct {
	mu    sync.RWMutex
	users map[uuid.UUID]*domain.User
}

func newMockUserRepo() *mockUserRepo {
	return &mockUserRepo{users: make(map[uuid.UUID]*domain.User)}
}

func (r *mockUserRepo) Create(_ context.Context, user *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.users[user.ID] = user
	return nil
}

func (r *mockUserRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	u, ok := r.users[id]
	if !ok {
		return nil, nil
	}
	return u, nil
}

func (r *mockUserRepo) GetByEmail(_ context.Context, email string) (*domain.User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, u := range r.users {
		if u.Email == email {
			return u, nil
		}
	}
	return nil, nil
}

func (r *mockUserRepo) Update(_ context.Context, user *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.users[user.ID] = user
	return nil
}

func (r *mockUserRepo) SearchUsers(_ context.Context, _ string, _ int) ([]domain.User, error) {
	return nil, nil
}

// ---

type mockRefreshTokenRepo struct {
	mu     sync.RWMutex
	tokens map[string]*repository.RefreshToken
}

func newMockRefreshTokenRepo() *mockRefreshTokenRepo {
	return &mockRefreshTokenRepo{tokens: make(map[string]*repository.RefreshToken)}
}

func (r *mockRefreshTokenRepo) Create(_ context.Context, userID uuid.UUID, tokenHash string, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens[tokenHash] = &repository.RefreshToken{
		ID:        uuid.New(),
		UserID:    userID,
		TokenHash: tokenHash,
		ExpiresAt: expiresAt,
		CreatedAt: time.Now(),
	}
	return nil
}

func (r *mockRefreshTokenRepo) GetByHash(_ context.Context, tokenHash string) (*repository.RefreshToken, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tokens[tokenHash]
	if !ok {
		return nil, nil
	}
	return t, nil
}

func (r *mockRefreshTokenRepo) RevokeByUserID(_ context.Context, userID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for _, t := range r.tokens {
		if t.UserID == userID {
			t.RevokedAt = &now
		}
	}
	return nil
}

func (r *mockRefreshTokenRepo) RevokeByHash(_ context.Context, tokenHash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tokens[tokenHash]; ok {
		now := time.Now()
		t.RevokedAt = &now
	}
	return nil
}

func (r *mockRefreshTokenRepo) DeleteExpired(_ context.Context) error {
	return nil
}

// ---

type mockWorkspaceRepo struct {
	mu    sync.RWMutex
	items map[uuid.UUID]*domain.Workspace
}

func newMockWorkspaceRepo() *mockWorkspaceRepo {
	return &mockWorkspaceRepo{items: make(map[uuid.UUID]*domain.Workspace)}
}

func (r *mockWorkspaceRepo) Create(_ context.Context, ws *domain.Workspace) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[ws.ID] = ws
	return nil
}

func (r *mockWorkspaceRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Workspace, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ws, ok := r.items[id]
	if !ok {
		return nil, nil
	}
	return ws, nil
}

func (r *mockWorkspaceRepo) GetBySlug(_ context.Context, slug string) (*domain.Workspace, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, ws := range r.items {
		if ws.Slug == slug {
			return ws, nil
		}
	}
	return nil, nil
}

func (r *mockWorkspaceRepo) Update(_ context.Context, ws *domain.Workspace) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[ws.ID] = ws
	return nil
}

func (r *mockWorkspaceRepo) Delete(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.items, id)
	return nil
}

func (r *mockWorkspaceRepo) ListByOwner(_ context.Context, ownerID uuid.UUID) ([]domain.Workspace, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []domain.Workspace
	for _, ws := range r.items {
		if ws.OwnerID == ownerID {
			result = append(result, *ws)
		}
	}
	return result, nil
}

// ---

type mockWorkspaceMemberRepo struct {
	mu    sync.RWMutex
	items map[uuid.UUID]*domain.WorkspaceMember
}

func newMockWorkspaceMemberRepo() *mockWorkspaceMemberRepo {
	return &mockWorkspaceMemberRepo{items: make(map[uuid.UUID]*domain.WorkspaceMember)}
}

func (r *mockWorkspaceMemberRepo) Create(_ context.Context, m *domain.WorkspaceMember) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[m.ID] = m
	return nil
}

func (r *mockWorkspaceMemberRepo) GetByWorkspaceAndUser(_ context.Context, wsID, userID uuid.UUID) (*domain.WorkspaceMember, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, m := range r.items {
		if m.WorkspaceID == wsID && m.UserID == userID {
			return m, nil
		}
	}
	return nil, nil
}

func (r *mockWorkspaceMemberRepo) GetRole(_ context.Context, wsID, userID uuid.UUID) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, m := range r.items {
		if m.WorkspaceID == wsID && m.UserID == userID {
			return m.Role, nil
		}
	}
	return "", fmt.Errorf("user is not a member of this workspace")
}

func (r *mockWorkspaceMemberRepo) List(_ context.Context, _ uuid.UUID) ([]domain.WorkspaceMemberWithUser, error) {
	return nil, nil
}

func (r *mockWorkspaceMemberRepo) UpdateRole(_ context.Context, _, _ uuid.UUID, _ string) error {
	return nil
}

func (r *mockWorkspaceMemberRepo) Delete(_ context.Context, _, _ uuid.UUID) error {
	return nil
}

func (r *mockWorkspaceMemberRepo) CountOwners(_ context.Context, _ uuid.UUID) (int, error) {
	return 0, nil
}

func (r *mockWorkspaceMemberRepo) ListWithProjects(_ context.Context, _ uuid.UUID) ([]repository.HumanWithProjects, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Helper: create a test service
// ---------------------------------------------------------------------------

const testJWTSecret = "test-secret-key-for-jwt-signing-32b"

func newTestService() (*Service, *mockUserRepo, *mockRefreshTokenRepo, *mockWorkspaceRepo, *mockWorkspaceMemberRepo) {
	userRepo := newMockUserRepo()
	refreshRepo := newMockRefreshTokenRepo()
	wsRepo := newMockWorkspaceRepo()
	wsMemberRepo := newMockWorkspaceMemberRepo()

	svc := NewService(userRepo, refreshRepo, wsRepo, wsMemberRepo, testJWTSecret)
	return svc, userRepo, refreshRepo, wsRepo, wsMemberRepo
}

// ---------------------------------------------------------------------------
// Tests: Register
// ---------------------------------------------------------------------------

func TestRegister_Success(t *testing.T) {
	svc, userRepo, _, wsRepo, wsMemberRepo := newTestService()

	user, tokens, err := svc.Register(context.Background(), "test@example.com", "StrongP4ss", "Test User")
	require.NoError(t, err)
	require.NotNil(t, user)
	require.NotNil(t, tokens)

	assert.Equal(t, "test@example.com", user.Email)
	assert.Equal(t, "Test User", user.Name)
	assert.True(t, user.IsActive)
	assert.NotEqual(t, uuid.Nil, user.ID)

	// Password hash should not be empty.
	storedUser, _ := userRepo.GetByID(context.Background(), user.ID)
	assert.NotEmpty(t, storedUser.PasswordHash)

	// Tokens should be present.
	assert.NotEmpty(t, tokens.AccessToken)
	assert.NotEmpty(t, tokens.RefreshToken)
	assert.Equal(t, 900, tokens.ExpiresIn) // 15 minutes = 900 seconds

	// Default workspace should be created.
	var foundWS bool
	wsRepo.mu.RLock()
	for _, ws := range wsRepo.items {
		if ws.OwnerID == user.ID {
			foundWS = true
			break
		}
	}
	wsRepo.mu.RUnlock()
	assert.True(t, foundWS, "default workspace should be created")

	// Workspace member should be created with owner role.
	var foundMember bool
	wsMemberRepo.mu.RLock()
	for _, m := range wsMemberRepo.items {
		if m.UserID == user.ID && m.Role == domain.RoleOwner {
			foundMember = true
			break
		}
	}
	wsMemberRepo.mu.RUnlock()
	assert.True(t, foundMember, "workspace member with owner role should be created")
}

func TestRegister_DuplicateEmail(t *testing.T) {
	svc, _, _, _, _ := newTestService()

	_, _, err := svc.Register(context.Background(), "dupe@example.com", "StrongP4ss", "User One")
	require.NoError(t, err)

	_, _, err = svc.Register(context.Background(), "dupe@example.com", "StrongP4ss", "User Two")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestRegister_WeakPassword(t *testing.T) {
	svc, _, _, _, _ := newTestService()

	tests := []struct {
		name     string
		password string
		wantErr  string
	}{
		{"too short", "Abc1", "at least 8"},
		{"no uppercase", "abcdefg1", "uppercase"},
		{"no lowercase", "ABCDEFG1", "lowercase"},
		{"no digit", "Abcdefgh", "digit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := svc.Register(context.Background(), "weak@example.com", tt.password, "User")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestRegister_InvalidEmail(t *testing.T) {
	svc, _, _, _, _ := newTestService()

	_, _, err := svc.Register(context.Background(), "not-an-email", "StrongP4ss", "User")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid email")
}

// ---------------------------------------------------------------------------
// Tests: Login
// ---------------------------------------------------------------------------

func TestLogin_Success(t *testing.T) {
	svc, _, _, _, _ := newTestService()

	// Register first.
	_, _, err := svc.Register(context.Background(), "login@example.com", "StrongP4ss", "Login User")
	require.NoError(t, err)

	// Login.
	user, tokens, err := svc.Login(context.Background(), "login@example.com", "StrongP4ss")
	require.NoError(t, err)
	require.NotNil(t, user)
	require.NotNil(t, tokens)
	assert.Equal(t, "login@example.com", user.Email)
	assert.NotEmpty(t, tokens.AccessToken)
	assert.NotEmpty(t, tokens.RefreshToken)
}

func TestLogin_WrongPassword(t *testing.T) {
	svc, _, _, _, _ := newTestService()

	_, _, err := svc.Register(context.Background(), "wrongpw@example.com", "StrongP4ss", "User")
	require.NoError(t, err)

	_, _, err = svc.Login(context.Background(), "wrongpw@example.com", "WrongPassword1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid email or password")
}

func TestLogin_NonExistentEmail(t *testing.T) {
	svc, _, _, _, _ := newTestService()

	_, _, err := svc.Login(context.Background(), "nobody@example.com", "StrongP4ss")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid email or password")
}

func TestLogin_InactiveUser(t *testing.T) {
	svc, userRepo, _, _, _ := newTestService()

	_, _, err := svc.Register(context.Background(), "inactive@example.com", "StrongP4ss", "User")
	require.NoError(t, err)

	// Deactivate the user.
	userRepo.mu.Lock()
	for _, u := range userRepo.users {
		if u.Email == "inactive@example.com" {
			u.IsActive = false
		}
	}
	userRepo.mu.Unlock()

	_, _, err = svc.Login(context.Background(), "inactive@example.com", "StrongP4ss")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inactive")
}

// ---------------------------------------------------------------------------
// Tests: RefreshTokens
// ---------------------------------------------------------------------------

func TestRefreshTokens_Success(t *testing.T) {
	svc, _, _, _, _ := newTestService()

	_, tokens, err := svc.Register(context.Background(), "refresh@example.com", "StrongP4ss", "User")
	require.NoError(t, err)

	newTokens, err := svc.RefreshTokens(context.Background(), tokens.RefreshToken)
	require.NoError(t, err)
	require.NotNil(t, newTokens)
	assert.NotEmpty(t, newTokens.AccessToken)
	assert.NotEmpty(t, newTokens.RefreshToken)
	// New refresh token should differ from the old one.
	assert.NotEqual(t, tokens.RefreshToken, newTokens.RefreshToken)
}

func TestRefreshTokens_ExpiredToken(t *testing.T) {
	svc, _, refreshRepo, _, _ := newTestService()

	_, tokens, err := svc.Register(context.Background(), "expired@example.com", "StrongP4ss", "User")
	require.NoError(t, err)

	// Manually expire the refresh token.
	tokenHash := hashRefreshToken(tokens.RefreshToken)
	refreshRepo.mu.Lock()
	if t, ok := refreshRepo.tokens[tokenHash]; ok {
		t.ExpiresAt = time.Now().Add(-1 * time.Hour)
	}
	refreshRepo.mu.Unlock()

	_, err = svc.RefreshTokens(context.Background(), tokens.RefreshToken)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expired")
}

func TestRefreshTokens_RevokedToken_TheftDetection(t *testing.T) {
	svc, _, _, _, _ := newTestService()

	_, tokens, err := svc.Register(context.Background(), "theft@example.com", "StrongP4ss", "User")
	require.NoError(t, err)

	// First refresh: should succeed and revoke the old token.
	_, err = svc.RefreshTokens(context.Background(), tokens.RefreshToken)
	require.NoError(t, err)

	// Second refresh with the same (now revoked) token: theft detection.
	_, err = svc.RefreshTokens(context.Background(), tokens.RefreshToken)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reuse detected")
}

func TestRefreshTokens_InvalidToken(t *testing.T) {
	svc, _, _, _, _ := newTestService()

	_, err := svc.RefreshTokens(context.Background(), "rt_completely_invalid_token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid refresh token")
}

// ---------------------------------------------------------------------------
// Tests: ValidateAccessToken
// ---------------------------------------------------------------------------

func TestValidateAccessToken_Valid(t *testing.T) {
	svc, _, _, _, _ := newTestService()

	_, tokens, err := svc.Register(context.Background(), "validate@example.com", "StrongP4ss", "User")
	require.NoError(t, err)

	claims, err := svc.ValidateAccessToken(tokens.AccessToken)
	require.NoError(t, err)
	require.NotNil(t, claims)
	assert.Equal(t, "validate@example.com", claims.Email)
	assert.Equal(t, "User", claims.Name)
}

func TestValidateAccessToken_Expired(t *testing.T) {
	svc, _, _, _, _ := newTestService()

	// Override clock to create an already-expired token.
	past := time.Now().Add(-1 * time.Hour)
	origTimeNow := timeNow
	timeNow = func() time.Time { return past }

	_, tokens, err := svc.Register(context.Background(), "expiredaccess@example.com", "StrongP4ss", "User")
	require.NoError(t, err)

	// Restore clock.
	timeNow = origTimeNow

	_, err = svc.ValidateAccessToken(tokens.AccessToken)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid or expired")
}

func TestValidateAccessToken_InvalidSignature(t *testing.T) {
	svc, _, _, _, _ := newTestService()

	// Create a service with a different secret.
	otherSvc := NewService(newMockUserRepo(), newMockRefreshTokenRepo(), newMockWorkspaceRepo(), newMockWorkspaceMemberRepo(), "different-secret-key-for-signing!")

	_, tokens, err := otherSvc.Register(context.Background(), "other@example.com", "StrongP4ss", "User")
	require.NoError(t, err)

	// Validate with the original service (different secret).
	_, err = svc.ValidateAccessToken(tokens.AccessToken)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid or expired")
}

func TestValidateAccessToken_Malformed(t *testing.T) {
	svc, _, _, _, _ := newTestService()

	_, err := svc.ValidateAccessToken("not-a-jwt-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid or expired")
}

// ---------------------------------------------------------------------------
// Tests: ValidatePassword
// ---------------------------------------------------------------------------

func TestValidatePassword(t *testing.T) {
	assert.NoError(t, ValidatePassword("StrongP4ss"))
	assert.NoError(t, ValidatePassword("A1bcdefg"))
	assert.Error(t, ValidatePassword("short1A"))  // too short
	assert.Error(t, ValidatePassword("alllower1")) // no uppercase
	assert.Error(t, ValidatePassword("ALLUPPER1")) // no lowercase
	assert.Error(t, ValidatePassword("NoDigits"))  // no digit
}
