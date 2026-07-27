package handler

import (
	"net/http"

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

// refreshRequest represents the JSON body for token refresh.
type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
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

	return c.JSON(http.StatusCreated, map[string]interface{}{
		"user":   user,
		"tokens": tokens,
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

	return c.JSON(http.StatusOK, map[string]interface{}{
		"user":   user,
		"tokens": tokens,
	})
}

// Refresh handles POST /api/v1/auth/refresh
func (h *AuthHandler) Refresh(c echo.Context) error {
	var req refreshRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.RefreshToken == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"refresh_token": "refresh_token is required",
		}))
	}

	tokens, err := h.authService.RefreshTokens(c.Request().Context(), req.RefreshToken)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"tokens": tokens,
	})
}

// Logout handles POST /api/v1/auth/logout (protected endpoint).
// Revokes all refresh tokens for the current user.
func (h *AuthHandler) Logout(c echo.Context) error {
	userID, err := mw.GetUserID(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized(""))
	}

	if err := h.authService.Logout(c.Request().Context(), userID); err != nil {
		return handleError(c, err)
	}

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
