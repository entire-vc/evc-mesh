package handler

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/oautherror"
)

// oauthConsentPath is where the frontend SPA (task MCP-OAuth 2/5) renders
// the consent screen. Deliberately NOT under /oauth/* — task MCP-OAuth 4/5
// routes that whole prefix past the SPA straight to this backend (Caddy), so
// a path under it would never reach the frontend's router at all.
const oauthConsentPath = "/connect/consent"

// OAuthHandler serves the OAuth 2.0 Authorization Server endpoints (task
// MCP-OAuth 1/5): RFC 8414 metadata, RFC 7591 DCR, the RFC 6749 authorize +
// token + revoke endpoints, and the consent/grants API the frontend SPA
// consumes.
type OAuthHandler struct {
	oauthService service.OAuthService
	// issuer is cfg.Email.BaseURL trimmed of any trailing slash — the same
	// base URL invite links are already built from (see cmd/api/main.go),
	// reused here as both the RFC 8414 `issuer` and the frontend origin the
	// consent screen lives on.
	issuer string
}

// NewOAuthHandler creates a new OAuthHandler. issuer should be the server's
// own public base URL (e.g. "https://mesh.entire.host"), with or without a
// trailing slash.
func NewOAuthHandler(oauthService service.OAuthService, issuer string) *OAuthHandler {
	return &OAuthHandler{oauthService: oauthService, issuer: strings.TrimRight(issuer, "/")}
}

// --- RFC 8414 authorization server metadata ---

func (h *OAuthHandler) ServerMetadata(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]interface{}{
		"issuer":                                h.issuer,
		"authorization_endpoint":                h.issuer + "/oauth/authorize",
		"token_endpoint":                        h.issuer + "/oauth/token",
		"registration_endpoint":                 h.issuer + "/oauth/register",
		"revocation_endpoint":                   h.issuer + "/oauth/revoke",
		"code_challenge_methods_supported":      []string{"S256"},
		"client_id_metadata_document_supported": true,
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{"mesh", "offline_access"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"response_types_supported":              []string{"code"},
	})
}

// writeOAuthError writes an RFC 6749 error response. A server_error's own
// Description (usually a raw err.Error() from the database or an outbound
// fetch) goes to the server log only; Body() hands the caller a fixed text.
func writeOAuthError(c echo.Context, e *oautherror.Error) error {
	if e.IsServerError() {
		c.Logger().Errorf("oauth %s %s: server_error: %s", c.Request().Method, c.Request().URL.Path, e.Description)
	}
	return c.JSON(e.Status, e.Body())
}

// --- RFC 7591 dynamic client registration ---

func (h *OAuthHandler) Register(c echo.Context) error {
	var in service.DCRRegisterInput
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, oautherror.InvalidClientMetadata("request body is not valid JSON").Body())
	}
	client, oerr := h.oauthService.RegisterClientDCR(c.Request().Context(), in)
	if oerr != nil {
		return writeOAuthError(c, oerr)
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{
		"client_id":                  client.ClientID,
		"client_id_issued_at":        client.CreatedAt.Unix(),
		"client_name":                client.ClientName,
		"redirect_uris":              []string(client.RedirectURIs),
		"token_endpoint_auth_method": client.TokenEndpointAuthMethod,
		"grant_types":                []string(client.GrantTypes),
	})
}

// --- RFC 6749 authorize ---

func authorizeParamsFromQuery(c echo.Context) service.AuthorizeParams {
	return service.AuthorizeParams{
		ClientID:            c.QueryParam("client_id"),
		RedirectURI:         c.QueryParam("redirect_uri"),
		ResponseType:        c.QueryParam("response_type"),
		CodeChallenge:       c.QueryParam("code_challenge"),
		CodeChallengeMethod: c.QueryParam("code_challenge_method"),
		Scope:               c.QueryParam("scope"),
		State:               c.QueryParam("state"),
	}
}

// Authorize is GET /oauth/authorize. It never itself renders a consent
// screen — see the OAuthService.Decide doc comment on why that lives in the
// frontend (task MCP-OAuth 2/5). Its only three possible outcomes:
//  1. client_id/redirect_uri invalid → render the error directly (RFC
//     6749 §4.1.2.1: an untrusted redirect_uri must NEVER be redirected to).
//  2. redirect_uri trusted but some other param invalid → 302 back to it
//     with ?error=...&state=....
//  3. fully valid → 302 to the frontend consent screen with the same query
//     string, so the SPA's own session state (task 2/5) decides whether to
//     show the consent screen now or send the user to /login first.
func (h *OAuthHandler) Authorize(c echo.Context) error {
	p := authorizeParamsFromQuery(c)
	v := h.oauthService.ValidateAuthorize(c.Request().Context(), p)
	if v.ClientErr != nil {
		return writeOAuthError(c, v.ClientErr)
	}
	if v.RequestErr != nil {
		return c.Redirect(http.StatusFound, appendErrorParam(v.RedirectURI, v.RequestErr.Code, p.State))
	}
	return c.Redirect(http.StatusFound, h.issuer+oauthConsentPath+"?"+c.QueryString())
}

// appendErrorParam is a tiny local helper (not reused from oauth_service.go,
// which is a different package) for building the authorize-error redirect:
// base + error=<code>, plus state=<state> when the client sent one.
func appendErrorParam(base, code, state string) string {
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	out := base + sep + "error=" + url.QueryEscape(code)
	if state != "" {
		out += "&state=" + url.QueryEscape(state)
	}
	return out
}

// --- Consent API (task 2/5 consumes this; JWT-user-only, see main.go's
// mw.RequireUserAuth() on these routes) ---

func (h *OAuthHandler) ConsentInfo(c echo.Context) error {
	userID, err := mw.GetUserID(c)
	if err != nil {
		return apierror.Unauthorized("user authentication required")
	}
	info, oerr := h.oauthService.ConsentInfo(c.Request().Context(), userID, authorizeParamsFromQuery(c))
	if oerr != nil {
		return writeOAuthError(c, oerr)
	}
	return c.JSON(http.StatusOK, info)
}

// consentDecisionBody is POST /api/v1/oauth/consent's JSON body: the same
// authorize params the frontend received on its own query string, round-
// tripped back, plus the user's actual choice. OAuthService.Decide
// re-validates every one of these — the frontend round-trip is not trusted
// blindly.
type consentDecisionBody struct {
	ClientID            string    `json:"client_id"`
	RedirectURI         string    `json:"redirect_uri"`
	ResponseType        string    `json:"response_type"`
	CodeChallenge       string    `json:"code_challenge"`
	CodeChallengeMethod string    `json:"code_challenge_method"`
	Scope               string    `json:"scope"`
	State               string    `json:"state"`
	WorkspaceID         uuid.UUID `json:"workspace_id"`
	Allow               bool      `json:"allow"`
}

func (h *OAuthHandler) Decide(c echo.Context) error {
	userID, err := mw.GetUserID(c)
	if err != nil {
		return apierror.Unauthorized("user authentication required")
	}
	var body consentDecisionBody
	if err := c.Bind(&body); err != nil {
		return apierror.BadRequest("invalid request body")
	}

	redirectURL, decErr := h.oauthService.Decide(c.Request().Context(), service.ConsentDecisionInput{
		AuthorizeParams: service.AuthorizeParams{
			ClientID:            body.ClientID,
			RedirectURI:         body.RedirectURI,
			ResponseType:        body.ResponseType,
			CodeChallenge:       body.CodeChallenge,
			CodeChallengeMethod: body.CodeChallengeMethod,
			Scope:               body.Scope,
			State:               body.State,
		},
		UserID:      userID,
		WorkspaceID: body.WorkspaceID,
		Allow:       body.Allow,
	})
	if decErr != nil {
		if oerr, ok := decErr.(*oautherror.Error); ok {
			return writeOAuthError(c, oerr)
		}
		// Anything else (apierror.Error or a plain wrapped error) goes
		// through the global handler installed by NewHTTPErrorHandler.
		return decErr
	}
	return c.JSON(http.StatusOK, map[string]string{"redirect_uri": redirectURL})
}

// --- Token + revoke ---

// Token is POST /oauth/token. Reads via r.PostFormValue — NOT c.FormValue,
// which (net/http's http.Request.FormValue) also accepts a URL query
// parameter of the same name, letting a client_id or refresh_token be
// smuggled in via a logged/cached/Referer-leaked query string instead of the
// POST body RFC 6749 §3.2 actually requires. A client sending
// application/json instead of the required application/x-www-form-urlencoded
// still gets a clean invalid_request (grant_type reads empty, ParseForm
// simply finds no form body to parse) rather than a server error — the
// spec's "application/json на /token не ломает сервер" requirement.
func (h *OAuthHandler) Token(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	c.Response().Header().Set("Pragma", "no-cache")

	r := c.Request()
	grantType := r.PostFormValue("grant_type")
	clientID := r.PostFormValue("client_id")

	switch grantType {
	case "authorization_code":
		tr, oerr := h.oauthService.ExchangeCode(
			c.Request().Context(),
			clientID,
			r.PostFormValue("redirect_uri"),
			r.PostFormValue("code"),
			r.PostFormValue("code_verifier"),
		)
		if oerr != nil {
			return writeOAuthError(c, oerr)
		}
		return c.JSON(http.StatusOK, tr)
	case "refresh_token":
		tr, oerr := h.oauthService.RefreshTokenGrant(c.Request().Context(), clientID, r.PostFormValue("refresh_token"))
		if oerr != nil {
			return writeOAuthError(c, oerr)
		}
		return c.JSON(http.StatusOK, tr)
	case "":
		e := oautherror.InvalidRequest("grant_type is required")
		return writeOAuthError(c, e)
	default:
		e := oautherror.UnsupportedGrantType("unsupported grant_type: " + grantType)
		return writeOAuthError(c, e)
	}
}

// Revoke is POST /oauth/revoke. RFC 7009 §2.2: always 200, even for an
// unknown or already-revoked token — the client cannot distinguish "revoked"
// from "was never valid" from the response, by design. r.PostFormValue, not
// c.FormValue — same query-string-smuggling reason as Token above.
func (h *OAuthHandler) Revoke(c echo.Context) error {
	if err := h.oauthService.RevokeToken(c.Request().Context(), c.Request().PostFormValue("token")); err != nil {
		e := oautherror.ServerError(err.Error())
		return writeOAuthError(c, e)
	}
	return c.NoContent(http.StatusOK)
}

// --- User-facing "your connected apps" API ---

func (h *OAuthHandler) ListGrants(c echo.Context) error {
	userID, err := mw.GetUserID(c)
	if err != nil {
		return apierror.Unauthorized("user authentication required")
	}
	grants, err := h.oauthService.ListMyGrants(c.Request().Context(), userID)
	if err != nil {
		// Not apierror.Wrap: it copies err.Error() (driver/SQL text) into
		// the response's details field.
		c.Logger().Errorf("oauth list grants for user %s: %v", userID, err)
		return c.JSON(http.StatusInternalServerError, apierror.InternalError("failed to list connected apps"))
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"grants": grants})
}

func (h *OAuthHandler) RevokeGrant(c echo.Context) error {
	userID, err := mw.GetUserID(c)
	if err != nil {
		return apierror.Unauthorized("user authentication required")
	}
	grantID, err := uuid.Parse(c.Param("oauth_grant_id"))
	if err != nil {
		return apierror.BadRequest("invalid grant_id")
	}
	if err := h.oauthService.RevokeMyGrant(c.Request().Context(), userID, grantID); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}
