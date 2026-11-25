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

// The two routes of the lead-accept context (docs/lead-accept-context.md).
const (
	LeadAcceptContextPath     = "/api/v1/lead-accept-context"
	LeadAcceptAttestationPath = "/api/v1/lead-accept-registry/attestations"
)

// ContextKeyLeadAcceptPrincipal holds the *domain.LeadAcceptPrincipal of an
// authenticated lead-accept request.
const ContextKeyLeadAcceptPrincipal = "lead_accept_principal"

const leadAcceptUnauthorizedMessage = "A valid lead-accept token is required"

// LeadAcceptAuthenticator resolves an mlr_/mlp_ token.
type LeadAcceptAuthenticator interface {
	AuthenticateLeadAccept(ctx context.Context, token string) (*domain.LeadAcceptPrincipal, error)
}

// leadAcceptRouteKind returns the principal kind the route takes, or "" when
// the request is not on a lead-accept route.
func leadAcceptRouteKind(c echo.Context) string {
	switch {
	case c.Request().Method == http.MethodGet && c.Path() == LeadAcceptContextPath:
		return domain.LeadAcceptKindReader
	case c.Request().Method == http.MethodPost && c.Path() == LeadAcceptAttestationPath:
		return domain.LeadAcceptKindPublisher
	}
	return ""
}

// leadAcceptBearer returns the token of an "Authorization: Bearer mlr_…|mlp_…"
// header (scheme case-insensitive).
func leadAcceptBearer(c echo.Context) (string, bool) {
	header := c.Request().Header.Get("Authorization")
	if len(header) < 7 || !strings.EqualFold(header[:7], "Bearer ") {
		return "", false
	}
	token := strings.TrimSpace(header[7:])
	if domain.LeadAcceptTokenKind(token) == "" {
		return "", false
	}
	return token, true
}

// LeadAcceptForbidden is the 403 of the lead-accept routes.
func leadAcceptForbidden(c echo.Context, msg string) error {
	return c.JSON(http.StatusForbidden, &apierror.Error{Code: http.StatusForbidden,
		ErrorCode: domain.LeadAcceptCodeScopeForbidden, Message: msg})
}

// LeadAcceptGuard is an Echo-level middleware (e.Use), registered before the
// gate-intake guard so it answers its own two routes for every credential.
//
//   - Both lead-accept routes answer with Cache-Control: no-store, refusals
//     included.
//   - An mlr_/mlp_ bearer is resolved here on every route: invalid → 401; valid
//     → only its own route (reader: GET context, publisher: POST attestations),
//     403 everywhere else. The token never reaches DualAuth or a handler.
//   - On a lead-accept route any other credential (an agent key, a JWT, an
//     mgi_/ext_/cst_ token, a lane or admin key) is a 403 on its presence alone:
//     it is not validated, so the route is no oracle of other keys. No
//     credential at all is a 401.
func LeadAcceptGuard(auth LeadAcceptAuthenticator) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			want := leadAcceptRouteKind(c)
			if want != "" {
				c.Response().Header().Set("Cache-Control", "no-store")
			}
			token, ok := leadAcceptBearer(c)
			if !ok {
				if want == "" {
					return next(c)
				}
				if c.Request().Header.Get("Authorization") != "" || c.Request().Header.Get("X-Agent-Key") != "" {
					return leadAcceptForbidden(c, "this route takes only a lead-accept "+want+" token")
				}
				return unauthorizedJSON(c, leadAcceptUnauthorizedMessage)
			}
			p, err := auth.AuthenticateLeadAccept(c.Request().Context(), token)
			if err != nil {
				if errors.Is(err, domain.ErrLeadAcceptUnauthorized) {
					return unauthorizedJSON(c, leadAcceptUnauthorizedMessage)
				}
				log.Printf("[lead-accept] authentication lookup failed: %v", err)
				return c.JSON(http.StatusServiceUnavailable, apierror.ServiceUnavailable("Lead-accept context is temporarily unavailable"))
			}
			if want == "" || p.Kind != want {
				return leadAcceptForbidden(c, "a lead-accept "+p.Kind+" token reaches only its own route")
			}
			c.Set(ContextKeyLeadAcceptPrincipal, p)
			ctx := actorctx.WithActor(c.Request().Context(), p.ID, domain.ActorTypeSystem)
			ctx = actorctx.WithActorName(ctx, p.Name)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}

// RequireLeadAcceptPrincipal is the route guard of both routes: only a request
// LeadAcceptGuard authenticated with the route's kind gets through.
func RequireLeadAcceptPrincipal(kind string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Response().Header().Set("Cache-Control", "no-store")
			p := GetLeadAcceptPrincipal(c)
			if p == nil {
				return unauthorizedJSON(c, leadAcceptUnauthorizedMessage)
			}
			if p.Kind != kind {
				return leadAcceptForbidden(c, "a lead-accept "+p.Kind+" token reaches only its own route")
			}
			return next(c)
		}
	}
}

// GetLeadAcceptPrincipal returns the principal LeadAcceptGuard stored, or nil.
func GetLeadAcceptPrincipal(c echo.Context) *domain.LeadAcceptPrincipal {
	p, _ := c.Get(ContextKeyLeadAcceptPrincipal).(*domain.LeadAcceptPrincipal)
	return p
}
