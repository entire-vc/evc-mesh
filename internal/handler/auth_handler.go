package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/auth"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// AuthHandler handles HTTP requests for authentication endpoints.
type AuthHandler struct {
	authService *auth.Service
}

// NewAuthHandler creates a new AuthHandler with the given auth service.
func NewAuthHandler(as *auth.Service) *AuthHandler {
	return &AuthHandler{authService: as}
}

// refreshCookieName is the httpOnly cookie carrying the refresh token. It is
// scoped to refreshCookiePath so the browser never attaches it to any other
// request — a stolen access token (which never lives in a cookie) or an XSS
// payload reading document.cookie on some other page cannot reach it either.
const (
	refreshCookieName = "refresh_token"
	refreshCookiePath = "/api/v1/auth/refresh"
)

// setRefreshCookie attaches the refresh token to the response as an httpOnly,
// SameSite=Strict cookie instead of returning it in the JSON body. ttl mirrors
// the token's actual database lifetime (auth.Service.RefreshTokenTTL) so the
// cookie never outlives, or gets dropped before, the token it carries.
func setRefreshCookie(c echo.Context, token string, ttl time.Duration) {
	c.SetCookie(&http.Cookie{
		Name:     refreshCookieName,
		Value:    token,
		Path:     refreshCookiePath,
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   isRequestSecure(c.Request()),
		SameSite: http.SameSiteStrictMode,
	})
}

// clearRefreshCookie expires the refresh cookie immediately. Path must match
// setRefreshCookie's exactly — browsers key cookie deletion on Name+Path
// (+Domain), so a mismatched Path silently leaves the old cookie in place.
func clearRefreshCookie(c echo.Context) {
	c.SetCookie(&http.Cookie{
		Name:     refreshCookieName,
		Value:    "",
		Path:     refreshCookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isRequestSecure(c.Request()),
		SameSite: http.SameSiteStrictMode,
	})
}

// isRequestSecure reports whether the client's original connection was
// HTTPS, trusting X-Forwarded-Proto from the reverse proxy (Caddy/nginx) over
// the Go server's own (always-plain-HTTP-behind-a-proxy) r.TLS. Falls back to
// r.TLS for direct/dev connections with no proxy in front.
func isRequestSecure(r *http.Request) bool {
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		return p == "https"
	}
	return r.TLS != nil
}

// tokenResponse is the JSON shape returned for access-token-only responses —
// the refresh token travels in the httpOnly cookie set alongside it, never in
// the body.
func tokenResponse(tokens *auth.TokenPair) map[string]interface{} {
	return map[string]interface{}{
		"access_token": tokens.AccessToken,
		"expires_in":   tokens.ExpiresIn,
	}
}

// registerRequest represents the JSON body for user registration.
type registerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

// loginRequest represents the JSON body for user login.
type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Register handles POST /api/v1/auth/register
func (h *AuthHandler) Register(c echo.Context) error {
	var req registerRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.Email == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"email": "email is required",
		}))
	}
	if req.Password == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"password": "password is required",
		}))
	}
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"name": "name is required",
		}))
	}

	user, tokens, err := h.authService.Register(c.Request().Context(), req.Email, req.Password, req.Name)
	if err != nil {
		return handleError(c, err)
	}

	setRefreshCookie(c, tokens.RefreshToken, h.authService.RefreshTokenTTL())

	return c.JSON(http.StatusCreated, map[string]interface{}{
		"user":   user,
		"tokens": tokenResponse(tokens),
	})
}

// Config handles GET /api/v1/auth/config (public, unauthenticated).
// Returns instance-level auth settings the frontend needs before login —
// currently just whether self-registration is open.
func (h *AuthHandler) Config(c echo.Context) error {
	open, err := h.authService.RegistrationOpen(c.Request().Context())
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]bool{"registration_enabled": open})
}

// Login handles POST /api/v1/auth/login
func (h *AuthHandler) Login(c echo.Context) error {
	var req loginRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.Email == "" || req.Password == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"email":    "email is required",
			"password": "password is required",
		}))
	}

	user, tokens, err := h.authService.Login(c.Request().Context(), req.Email, req.Password)
	if err != nil {
		return handleError(c, err)
	}

	setRefreshCookie(c, tokens.RefreshToken, h.authService.RefreshTokenTTL())

	return c.JSON(http.StatusOK, map[string]interface{}{
		"user":   user,
		"tokens": tokenResponse(tokens),
	})
}

// Refresh handles POST /api/v1/auth/refresh. The refresh token comes from the
// httpOnly cookie set by Login/Register/Refresh/invite-accept — never from
// the request body, since a body-carried refresh token is exactly the
// JS-readable value this endpoint exists to eliminate.
func (h *AuthHandler) Refresh(c echo.Context) error {
	cookie, err := c.Cookie(refreshCookieName)
	if err != nil || cookie.Value == "" {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("missing refresh token"))
	}

	tokens, err := h.authService.RefreshTokens(c.Request().Context(), cookie.Value)
	if err != nil {
		if errors.Is(err, auth.ErrTokenReused) {
			// The presented token was already revoked — every session for this
			// user has just been killed server-side. Kill the cookie on this
			// client too, rather than leaving a corpse that re-triggers the
			// same reuse error (and re-revokes an already-revoked user) on
			// every reload until it expires on its own.
			clearRefreshCookie(c)
		}
		return handleError(c, err)
	}

	setRefreshCookie(c, tokens.RefreshToken, h.authService.RefreshTokenTTL())

	return c.JSON(http.StatusOK, map[string]interface{}{
		"tokens": tokenResponse(tokens),
	})
}

// Logout handles POST /api/v1/auth/logout (protected endpoint).
// Revokes all refresh tokens for the current user and clears the cookie.
func (h *AuthHandler) Logout(c echo.Context) error {
	userID, err := mw.GetUserID(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized(""))
	}

	if err := h.authService.Logout(c.Request().Context(), userID); err != nil {
		return handleError(c, err)
	}

	clearRefreshCookie(c)

	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// Me handles GET /api/v1/auth/me (protected endpoint).
func (h *AuthHandler) Me(c echo.Context) error {
	userID, err := mw.GetUserID(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized(""))
	}

	user, err := h.authService.GetUserByID(c.Request().Context(), userID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, user)
}

// updateUserProfileRequest represents the JSON body for updating the current user's profile.
type updateUserProfileRequest struct {
	Name      string `json:"name"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`
}

// UpdateMe handles PATCH /api/v1/auth/me (protected endpoint).
// Note: avatar_url="" is treated as "no change"; clearing avatar is a separate flow.
func (h *AuthHandler) UpdateMe(c echo.Context) error {
	userID, err := mw.GetUserID(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized(""))
	}

	var req updateUserProfileRequest
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	user, err := h.authService.UpdateProfile(c.Request().Context(), userID, req.Name, req.Username, req.AvatarURL)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, user)
}

// CheckUsername handles GET /api/v1/auth/check-username (protected endpoint).
// Returns {"available": true/false} for a given ?username= query parameter.
func (h *AuthHandler) CheckUsername(c echo.Context) error {
	userID, err := mw.GetUserID(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized(""))
	}

	username := c.QueryParam("username")
	if username == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("username query param is required"))
	}

	available, err := h.authService.CheckUsername(c.Request().Context(), userID, username)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]bool{"available": available})
}
