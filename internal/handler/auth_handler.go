package handler

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/auth"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// AuthHandler handles HTTP requests for authentication endpoints.
type AuthHandler struct {
	authService *auth.Service
	// loginLockout is the account-level failed-login counter (see
	// mw.LoginLockout's doc comment). nil when not wired via
	// WithLoginLockout — Login then falls back to whatever protection the
	// surrounding per-IP RateLimit middleware provides, which per
	// RateLimitKeyByIP's doc comment may be none at all on an untrustworthy
	// reverse-proxy topology.
	loginLockout *mw.LoginLockout
}

// NewAuthHandler creates a new AuthHandler with the given auth service.
func NewAuthHandler(as *auth.Service) *AuthHandler {
	return &AuthHandler{authService: as}
}

// WithLoginLockout wires the Redis-backed per-account failed-login counter
// into the handler. Optional (see the loginLockout field doc comment).
func (h *AuthHandler) WithLoginLockout(l *mw.LoginLockout) {
	h.loginLockout = l
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
	c.SetCookie(&http.Cookie{ // nosemgrep: go.lang.security.audit.net.cookie-missing-secure.cookie-missing-secure — Secure IS set via isRequestSecure(c.Request()); the rule only recognises a literal `true`
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
	c.SetCookie(&http.Cookie{ // nosemgrep: go.lang.security.audit.net.cookie-missing-secure.cookie-missing-secure — Secure IS set via isRequestSecure(c.Request()); the rule only recognises a literal `true`
		Name:     refreshCookieName,
		Value:    "",
		Path:     refreshCookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isRequestSecure(c.Request()),
		SameSite: http.SameSiteStrictMode,
	})
}

// cookieInsecureOptOut disables the Secure attribute on the refresh cookie for
// the one deployment that legitimately cannot set it: Mesh served over plain
// HTTP on a trusted LAN (a self-hoster on http://192.168.x.y:8005). It is an
// opt-out rather than the default because getting it wrong in the safe
// direction costs a self-hoster a failed login they will notice immediately,
// while getting it wrong in the unsafe direction silently puts a 7-day refresh
// token on the wire in cleartext.
var cookieInsecureOptOut = os.Getenv("MESH_COOKIE_INSECURE") == "true"

// isRequestSecure reports whether the refresh cookie should carry Secure.
//
// It deliberately does NOT trust X-Forwarded-Proto. That header is set by
// whatever spoke to us last, and in our own production topology it is
// provably wrong: the hel01 edge terminates TLS and forwards to mesh-vm's
// Caddy over plain :80, and Caddy (v2.11.4, no `trusted_proxies` configured)
// OVERWRITES the inbound X-Forwarded-Proto with the scheme of the hop it
// received rather than preserving it. Measured on the host, echo server
// behind the same binary:
//
//	direct, client sends https  -> X-Forwarded-Proto='https'
//	through Caddy, same request -> X-Forwarded-Proto='http'
//
// So a header-trusting check returns false on https://mesh.entire.host and
// drops Secure from the cookie — on a host that has no HSTS and answers on
// port 80, which is exactly the setup where a cleartext request carries the
// cookie out before the 308 redirect can upgrade it.
//
// Instead: Secure unless this is plainly a local dev connection, with an
// explicit env opt-out for plain-HTTP LAN deployments. Unknown topology gets
// the safe answer.
func isRequestSecure(r *http.Request) bool {
	if cookieInsecureOptOut {
		return false
	}
	if isLoopbackHost(r.Host) {
		return r.TLS != nil
	}
	return true
}

// isLoopbackHost reports whether the request was addressed to this machine by
// a loopback name — the dev case (`go run` + Vite proxy on localhost), where
// the browser will not accept a Secure cookie over http.
func isLoopbackHost(host string) bool {
	h := host
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		h = parsed
	}
	h = strings.Trim(h, "[]")
	if h == "localhost" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
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

	ctx := c.Request().Context()
	// Normalized identifier for the failure lockout — same normalization
	// auth.Service.Login applies internally (auth.NormalizeEmail), so
	// "A@x.com" and "a@x.com" share one budget instead of each getting a
	// fresh one (which would make the lockout trivially bypassable by
	// varying case).
	identifier := auth.NormalizeEmail(req.Email)

	user, tokens, err := h.authService.Login(ctx, req.Email, req.Password)
	if err != nil {
		// Account-level failure lockout — the PRIMARY brute-force defense
		// for this endpoint (see mw.LoginLockout's doc comment). Only a
		// wrong-password attempt counts: ErrInvalidCredentials is returned
		// identically whether the account exists or not (see
		// auth.Service.Login), so this branch cannot become an
		// account-enumeration oracle, and it deliberately does NOT fire on
		// ErrUserInactive or an infra error — neither is a brute-force
		// signal, and spending a legitimate owner's failure budget on them
		// would be its own bug.
		if h.loginLockout != nil && errors.Is(err, auth.ErrInvalidCredentials) {
			locked, retryAfter, lockErr := h.loginLockout.RecordFailure(ctx, identifier)
			if lockErr != nil {
				// Fail open: log and fall through to the normal 401 below.
				// A Redis outage must not be able to lock every account out.
				c.Logger().Errorf("login lockout: record failure: %v", lockErr)
			} else if locked {
				return tooManyLoginAttemptsJSON(c, retryAfter)
			}
		}
		return handleError(c, err)
	}

	// The correct password is NEVER blocked by the lockout above, no matter
	// how many prior wrong guesses (the owner's own typos, or an attacker's)
	// were recorded against this identifier — see mw.LoginLockout's doc
	// comment for why that property is load-bearing. Reset now so the
	// identifier starts with a full budget again.
	if h.loginLockout != nil {
		if resetErr := h.loginLockout.Reset(ctx, identifier); resetErr != nil {
			c.Logger().Errorf("login lockout: reset: %v", resetErr)
		}
	}

	setRefreshCookie(c, tokens.RefreshToken, h.authService.RefreshTokenTTL())

	return c.JSON(http.StatusOK, map[string]interface{}{
		"user":   user,
		"tokens": tokenResponse(tokens),
	})
}

// tooManyLoginAttemptsJSON writes the 429 response for an identifier that
// has exceeded its failed-login budget. Status, body and Retry-After are
// identical whether or not an account with this identifier exists — the
// lockout counter is kept for the identifier STRING (see
// mw.LoginLockout's doc comment), never for a resolved account row, so this
// response can never be used to enumerate which emails have accounts here.
func tooManyLoginAttemptsJSON(c echo.Context, retryAfterSecs int) error {
	if retryAfterSecs > 0 {
		c.Response().Header().Set("Retry-After", fmt.Sprintf("%d", retryAfterSecs))
	}
	return c.JSON(http.StatusTooManyRequests, apierror.TooManyRequests("too many failed login attempts, try again later"))
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
