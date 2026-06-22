package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

const (
	// bcryptCost is the bcrypt work factor for hashing user passwords.
	bcryptCost = 10

	// jwtIssuer is the issuer claim value for JWT tokens.
	jwtIssuer = "evc-mesh"

	// refreshTokenRandomBytes is the number of random bytes for the refresh token.
	refreshTokenRandomBytes = 32
)

// timeNow is a package-level variable so tests can override the clock.
var timeNow = time.Now

var (
	usernameRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$`)
	nonSlugRe   = regexp.MustCompile(`[^a-z0-9-]`)
	multiDashRe = regexp.MustCompile(`-{2,}`)
)

// Errors returned by the auth service.
var (
	ErrPasswordTooShort       = apierror.BadRequest("password must be at least 8 characters")
	ErrPasswordTooLong        = apierror.BadRequest("password must be at most 128 characters")
	ErrPasswordWeakComplexity = apierror.BadRequest("password must contain at least one uppercase letter, one lowercase letter, and one digit")
	ErrInvalidEmail           = apierror.BadRequest("invalid email address")
	ErrEmailAlreadyExists     = apierror.Conflict("a user with this email already exists")
	ErrInvalidCredentials     = apierror.Unauthorized("invalid email or password")
	ErrInvalidRefreshToken    = apierror.Unauthorized("invalid refresh token")
	ErrRefreshTokenExpired    = apierror.Unauthorized("refresh token has expired")
	ErrRefreshTokenRevoked    = apierror.Unauthorized("refresh token has been revoked")
	ErrTokenReused            = apierror.Unauthorized("refresh token reuse detected; all sessions revoked")
	ErrInvalidAccessToken     = apierror.Unauthorized("invalid or expired access token")
	ErrUserInactive           = apierror.Unauthorized("user account is inactive")
	ErrUsernameConflict       = apierror.Conflict("username is already taken")
	ErrInvalidUsername        = apierror.BadRequest("username must be 2-40 characters: lowercase letters, digits, hyphens; no leading/trailing hyphens")
)

// Claims represents the JWT claims for an access token.
type Claims struct {
	jwt.RegisteredClaims
	Email string `json:"email"`
	Name  string `json:"name"`
}

// TokenPair holds an access token and a refresh token returned to the client.
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"` // seconds until access token expiry
}

// Service provides authentication business logic: registration, login,
// token generation and validation, and refresh token rotation.
type Service struct {
	userRepo            repository.UserRepository
	refreshTokenRepo    repository.RefreshTokenRepository
	workspaceRepo       repository.WorkspaceRepository
	workspaceMemberRepo repository.WorkspaceMemberRepository
	jwtSecret           []byte
	accessTokenTTL      time.Duration
	refreshTokenTTL     time.Duration
}

// NewService creates a new auth Service with the given dependencies.
func NewService(
	userRepo repository.UserRepository,
	refreshTokenRepo repository.RefreshTokenRepository,
	workspaceRepo repository.WorkspaceRepository,
	workspaceMemberRepo repository.WorkspaceMemberRepository,
	jwtSecret string,
) *Service {
	return &Service{
		userRepo:            userRepo,
		refreshTokenRepo:    refreshTokenRepo,
		workspaceRepo:       workspaceRepo,
		workspaceMemberRepo: workspaceMemberRepo,
		jwtSecret:           []byte(jwtSecret),
		accessTokenTTL:      15 * time.Minute,
		refreshTokenTTL:     7 * 24 * time.Hour,
	}
}

// Register creates a new user account, a default workspace, and returns a token pair.
func (s *Service) Register(ctx context.Context, email, password, name string) (*domain.User, *TokenPair, error) {
	if err := validateEmail(email); err != nil {
		return nil, nil, err
	}
	if err := ValidatePassword(password); err != nil {
		return nil, nil, err
	}

	// Check email uniqueness.
	existing, err := s.userRepo.GetByEmail(ctx, email)
	if err != nil {
		return nil, nil, apierror.Wrap(err)
	}
	if existing != nil {
		return nil, nil, ErrEmailAlreadyExists
	}

	// Hash password.
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return nil, nil, apierror.InternalError("failed to hash password")
	}

	username, err := s.deriveUniqueUsername(ctx, email)
	if err != nil {
		return nil, nil, apierror.Wrap(err)
	}

	now := timeNow()
	user := &domain.User{
		ID:           uuid.New(),
		Email:        email,
		PasswordHash: string(hash),
		Name:         name,
		Username:     username,
		IsActive:     true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	err = s.userRepo.Create(ctx, user)
	if err != nil {
		return nil, nil, err
	}

	// Create default workspace.
	ws := &domain.Workspace{
		ID:        uuid.New(),
		Name:      "My Workspace",
		Slug:      fmt.Sprintf("ws-%s", user.ID.String()[:8]),
		OwnerID:   user.ID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	err = s.workspaceRepo.Create(ctx, ws)
	if err != nil {
		return nil, nil, err
	}

	// Create workspace member with owner role.
	member := &domain.WorkspaceMember{
		ID:          uuid.New(),
		WorkspaceID: ws.ID,
		UserID:      user.ID,
		Role:        domain.RoleOwner,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	err = s.workspaceMemberRepo.Create(ctx, member)
	if err != nil {
		return nil, nil, err
	}

	// Generate tokens.
	tokens, err := s.generateTokenPair(user)
	if err != nil {
		return nil, nil, err
	}

	return user, tokens, nil
}

// Login authenticates a user by email and password and returns a token pair.
func (s *Service) Login(ctx context.Context, email, password string) (*domain.User, *TokenPair, error) {
	user, err := s.userRepo.GetByEmail(ctx, email)
	if err != nil {
		return nil, nil, apierror.Wrap(err)
	}
	if user == nil {
		return nil, nil, ErrInvalidCredentials
	}

	if !user.IsActive {
		return nil, nil, ErrUserInactive
	}

	err = bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password))
	if err != nil {
		return nil, nil, ErrInvalidCredentials
	}

	tokens, err := s.generateTokenPair(user)
	if err != nil {
		return nil, nil, err
	}

	return user, tokens, nil
}

// RefreshTokens validates a refresh token and returns a new token pair.
// Implements refresh token rotation: the old token is revoked and a new one is issued.
// If a revoked token is reused, all tokens for the user are revoked (theft detection).
func (s *Service) RefreshTokens(ctx context.Context, refreshToken string) (*TokenPair, error) {
	tokenHash := hashRefreshToken(refreshToken)

	stored, err := s.refreshTokenRepo.GetByHash(ctx, tokenHash)
	if err != nil {
		return nil, apierror.Wrap(err)
	}
	if stored == nil {
		return nil, ErrInvalidRefreshToken
	}

	// Token theft detection: if token was already revoked, revoke all user tokens.
	if stored.RevokedAt != nil {
		_ = s.refreshTokenRepo.RevokeByUserID(ctx, stored.UserID)
		return nil, ErrTokenReused
	}

	if stored.ExpiresAt.Before(timeNow()) {
		return nil, ErrRefreshTokenExpired
	}

	// Revoke the old refresh token.
	err = s.refreshTokenRepo.RevokeByHash(ctx, tokenHash)
	if err != nil {
		return nil, apierror.Wrap(err)
	}

	// Look up the user to generate new tokens.
	user, err := s.userRepo.GetByID(ctx, stored.UserID)
	if err != nil {
		return nil, apierror.Wrap(err)
	}
	if user == nil || !user.IsActive {
		return nil, ErrUserInactive
	}

	return s.generateTokenPair(user)
}

// ValidateAccessToken parses and validates a JWT access token string.
// Returns the claims if the token is valid.
func (s *Service) ValidateAccessToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{},
		func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return s.jwtSecret, nil
		},
		jwt.WithIssuer(jwtIssuer),
		jwt.WithValidMethods([]string{"HS256"}),
	)
	if err != nil {
		return nil, ErrInvalidAccessToken
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, ErrInvalidAccessToken
	}

	return claims, nil
}

// generateTokenPair creates a new JWT access token and an opaque refresh token.
func (s *Service) generateTokenPair(user *domain.User) (*TokenPair, error) {
	now := timeNow()

	// Generate JWT access token.
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.accessTokenTTL)),
			Issuer:    jwtIssuer,
			ID:        uuid.New().String(),
		},
		Email: user.Email,
		Name:  user.Name,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	accessToken, err := token.SignedString(s.jwtSecret)
	if err != nil {
		return nil, apierror.InternalError("failed to sign access token")
	}

	// Generate opaque refresh token.
	plainRefreshToken, tokenHash, err := generateRefreshToken()
	if err != nil {
		return nil, apierror.InternalError("failed to generate refresh token")
	}

	// Store refresh token hash in the database.
	if err := s.refreshTokenRepo.Create(
		context.Background(),
		user.ID,
		tokenHash,
		now.Add(s.refreshTokenTTL),
	); err != nil {
		return nil, apierror.Wrap(err)
	}

	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: plainRefreshToken,
		ExpiresIn:    int(s.accessTokenTTL.Seconds()),
	}, nil
}

// generateRefreshToken creates a random refresh token and its SHA-256 hash.
// Format: rt_{64_hex_chars}
func generateRefreshToken() (plainToken, tokenHash string, err error) {
	b := make([]byte, refreshTokenRandomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}

	plainToken = "rt_" + hex.EncodeToString(b)
	hash := sha256.Sum256([]byte(plainToken))
	tokenHash = hex.EncodeToString(hash[:])

	return plainToken, tokenHash, nil
}

// hashRefreshToken computes the SHA-256 hash of a plain refresh token.
func hashRefreshToken(plainToken string) string {
	hash := sha256.Sum256([]byte(plainToken))
	return hex.EncodeToString(hash[:])
}

// ValidatePassword checks that a password meets complexity requirements.
func ValidatePassword(password string) error {
	if len(password) < 8 {
		return ErrPasswordTooShort
	}
	if len(password) > 128 {
		return ErrPasswordTooLong
	}

	var hasUpper, hasLower, hasDigit bool
	for _, r := range password {
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}

	if !hasUpper || !hasLower || !hasDigit {
		return ErrPasswordWeakComplexity
	}
	return nil
}

// Logout revokes all refresh tokens for the given user.
// The access token will expire naturally (15 min TTL).
func (s *Service) Logout(ctx context.Context, userID uuid.UUID) error {
	return s.refreshTokenRepo.RevokeByUserID(ctx, userID)
}

// UpdateProfile updates the display_name, username (optional), and avatar_url for the given user.
// name is trimmed and must be non-empty, max 100 runes. username is validated and checked for global uniqueness.
func (s *Service) UpdateProfile(ctx context.Context, userID uuid.UUID, name, username, avatarURL string) (*domain.User, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, apierror.ValidationError(map[string]string{"name": "name is required"})
	}
	if len([]rune(name)) > 100 {
		return nil, apierror.ValidationError(map[string]string{"name": "name must be at most 100 characters"})
	}

	if username != "" {
		if !usernameRe.MatchString(username) {
			return nil, ErrInvalidUsername
		}
		existing, err := s.userRepo.GetByUsernameGlobal(ctx, username)
		if err != nil {
			return nil, apierror.Wrap(err)
		}
		if existing != nil && existing.ID != userID {
			return nil, ErrUsernameConflict
		}
	}

	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, apierror.Wrap(err)
	}
	if user == nil {
		return nil, apierror.NotFound("User")
	}

	user.Name = name
	if username != "" {
		user.Username = username
	}
	if avatarURL != "" {
		user.AvatarURL = avatarURL
	}

	if err := s.userRepo.Update(ctx, user); err != nil {
		return nil, err
	}
	return user, nil
}

// CheckUsername reports whether a username is available for the given user.
// Returns false (not available) for invalid format; no error is returned in that case.
func (s *Service) CheckUsername(ctx context.Context, userID uuid.UUID, username string) (bool, error) {
	if !usernameRe.MatchString(username) {
		return false, nil
	}
	existing, err := s.userRepo.GetByUsernameGlobal(ctx, username)
	if err != nil {
		return false, apierror.Wrap(err)
	}
	if existing == nil || existing.ID == userID {
		return true, nil
	}
	return false, nil
}

// GetUserByID retrieves a user by ID. Used by the /auth/me endpoint.
func (s *Service) GetUserByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	user, err := s.userRepo.GetByID(ctx, id)
	if err != nil {
		return nil, apierror.Wrap(err)
	}
	if user == nil {
		return nil, apierror.NotFound("User")
	}
	return user, nil
}

// deriveUniqueUsername generates a unique username from an email address.
// Mirrors the backfill logic in migration 20260520046_add_users_username:
// strip domain, lowercase, replace non-slug chars with "-", deduplicate dashes,
// trim edges, clamp to 38 chars, append numeric suffix to resolve conflicts.
func (s *Service) deriveUniqueUsername(ctx context.Context, email string) (string, error) {
	prefix := strings.ToLower(strings.SplitN(email, "@", 2)[0])
	base := strings.Trim(multiDashRe.ReplaceAllString(nonSlugRe.ReplaceAllString(prefix, "-"), "-"), "-")
	if len(base) < 2 {
		base += "0"
	}
	if len(base) < 2 {
		base = "u0"
	}
	if len(base) > 38 {
		base = base[:38]
	}
	candidate := base
	for i := 1; ; i++ {
		exists, err := s.userRepo.UsernameExists(ctx, candidate)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
		suffix := fmt.Sprintf("%d", i)
		trimmed := base
		if len(trimmed)+len(suffix) > 40 {
			trimmed = trimmed[:40-len(suffix)]
		}
		candidate = trimmed + suffix
	}
}

// validateEmail checks that an email address is syntactically valid.
func validateEmail(email string) error {
	_, err := mail.ParseAddress(email)
	if err != nil {
		return ErrInvalidEmail
	}
	return nil
}
