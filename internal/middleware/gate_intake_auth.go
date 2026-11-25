package middleware

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// GateIntakeRoutePath is the one route a gate-intake token reaches.
const (
	GateIntakeRouteMethod = http.MethodPost
	GateIntakeRoutePath   = "/api/v1/gate-intake"
)

// ContextKeyGateIntakePrincipal holds the *domain.GateIntakePrincipal of an
// authenticated gate-intake request.
const ContextKeyGateIntakePrincipal = "gate_intake_principal"

// gateIntakeUnauthorizedMessage is the one 401 message of the route: a missing,
// malformed, unknown, revoked or expired token all read the same.
const gateIntakeUnauthorizedMessage = "A valid gate-intake token is required"

// GateIntakeAuthenticator resolves a gate-intake token.
type GateIntakeAuthenticator interface {
	AuthenticateGateIntake(ctx context.Context, token string) (*domain.GateIntakePrincipal, error)
}

// gateIntakeBearer returns the mgi_ token of an "Authorization: Bearer mgi_…"
// header (scheme case-insensitive).
func gateIntakeBearer(c echo.Context) (string, bool) {
	header := c.Request().Header.Get("Authorization")
	if len(header) < 7 || !strings.EqualFold(header[:7], "Bearer ") {
		return "", false
	}
	token := strings.TrimSpace(header[7:])
	if !strings.HasPrefix(token, domain.GateIntakeTokenPrefix) {
		return "", false
	}
	return token, true
}

// GateIntakeGuard is an Echo-level middleware (e.Use): it runs on every route
// before any route group's auth. A request without an mgi_ bearer passes
// through untouched. One with an mgi_ bearer is resolved here: an invalid token
// is a 401 on any route; a valid one reaches only POST /api/v1/gate-intake and
// is a 403 everywhere else — reads, writes, admin, everything. The token
// therefore never reaches DualAuth or any other handler.
func GateIntakeGuard(auth GateIntakeAuthenticator) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			token, ok := gateIntakeBearer(c)
			if !ok {
				return next(c)
			}
			p, err := auth.AuthenticateGateIntake(c.Request().Context(), token)
			if err != nil {
				if errors.Is(err, domain.ErrGateIntakeUnauthorized) {
					return unauthorizedJSON(c, gateIntakeUnauthorizedMessage)
				}
				log.Printf("[gate-intake] authentication lookup failed: %v", err)
				return c.JSON(http.StatusServiceUnavailable, apierror.ServiceUnavailable("Gate intake is temporarily unavailable"))
			}
			if c.Request().Method != GateIntakeRouteMethod || c.Path() != GateIntakeRoutePath {
				return c.JSON(http.StatusForbidden, apierror.Forbidden("A gate-intake token reaches only POST "+GateIntakeRoutePath))
			}
			c.Set(ContextKeyGateIntakePrincipal, p)
			ctx := actorctx.WithActor(c.Request().Context(), p.ID, domain.ActorTypeSystem)
			ctx = actorctx.WithActorName(ctx, p.Name)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}

// RequireGateIntakePrincipal is the route guard of POST /api/v1/gate-intake:
// only a request GateIntakeGuard authenticated gets through. A JWT, an agent
// key, an ext_/cst_ token or no credential at all is the same 401 as a bad
// gate-intake token.
func RequireGateIntakePrincipal() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if GetGateIntakePrincipal(c) == nil {
				return unauthorizedJSON(c, gateIntakeUnauthorizedMessage)
			}
			return next(c)
		}
	}
}

// GetGateIntakePrincipal returns the principal GateIntakeGuard stored, or nil.
func GetGateIntakePrincipal(c echo.Context) *domain.GateIntakePrincipal {
	p, _ := c.Get(ContextKeyGateIntakePrincipal).(*domain.GateIntakePrincipal)
	return p
}
