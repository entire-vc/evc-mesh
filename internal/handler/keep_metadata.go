package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/extensions/secretstore"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// KeepMetadataSecrets is the one read the probe makes of Mesh's own data: the
// Keep ref of a current workspace-scoped secret. SecretRefRepo implements it.
type KeepMetadataSecrets interface {
	KeepMetadataRef(ctx context.Context, workspaceID, secretID uuid.UUID) (string, error)
}

// keepMetadataTimeout bounds the whole Keep call; there is no retry.
const keepMetadataTimeout = 5 * time.Second

// KeepMetadataHandler serves GET /workspaces/:ws_id/secrets/:secret_id/keep-metadata
// (protocol mesh-keep-metadata-v1): the version and length of the current Keep
// value of a workspace secret, and nothing else.
//
// What it holds is deliberately narrow. Secrets can only return a ref, and Keep
// is the KeepMetadataReader interface, which has no value-bearing method. There
// is no SecretStore, materializer, GetValue or PutValue here for a later edit to
// reach without adding a dependency review would see.
//
// Authorization is the route's middleware chain (RequireUserAuth, then
// rbac(PermManageSecrets) on the current workspace membership, behind the
// group's WorkspaceRLS); the checks in Get repeat the actor and workspace
// bindings so the handler is safe on its own. A nil Keep (not configured) is a
// 503, never a fallback to the builtin store.
type KeepMetadataHandler struct {
	secrets KeepMetadataSecrets
	keep    secretstore.KeepMetadataReader
}

// NewKeepMetadataHandler returns the handler. keep may be nil (Keep not
// configured on this server).
func NewKeepMetadataHandler(secrets KeepMetadataSecrets, keep secretstore.KeepMetadataReader) *KeepMetadataHandler {
	return &KeepMetadataHandler{secrets: secrets, keep: keep}
}

// canonicalUUID parses raw only if it is the canonical lowercase form.
func canonicalUUID(raw string) (uuid.UUID, bool) {
	id, err := uuid.Parse(raw)
	if err != nil || id.String() != raw {
		return uuid.Nil, false
	}
	return id, true
}

// Head answers HEAD, which is never a probe.
func (h *KeepMetadataHandler) Head(c echo.Context) error {
	return middleware.KeepMetadataFailure(c, http.StatusMethodNotAllowed)
}

// Get handles the probe.
func (h *KeepMetadataHandler) Get(c echo.Context) error {
	req := c.Request()
	if req.URL.RawQuery != "" || req.URL.ForceQuery || req.URL.Fragment != "" ||
		req.ContentLength != 0 || len(req.TransferEncoding) > 0 {
		return h.fail(c, http.StatusBadRequest, "bad_request")
	}
	wsID, okW := canonicalUUID(c.Param("ws_id"))
	secretID, okS := canonicalUUID(c.Param("secret_id"))
	if !okW || !okS {
		return h.fail(c, http.StatusBadRequest, "bad_id")
	}

	// A human session of this workspace only: not an agent key, an extension,
	// a connector or a chat token. The route middleware already refused them;
	// this is the same rule held by the handler itself.
	if middleware.IsAgent(c) || middleware.IsExtension(c) {
		return h.fail(c, http.StatusNotFound, "actor")
	}
	if _, err := middleware.GetUserID(c); err != nil {
		return h.fail(c, http.StatusNotFound, "actor")
	}
	if ctxWS, err := middleware.GetWorkspaceID(c); err != nil || ctxWS != wsID {
		return h.fail(c, http.StatusNotFound, "workspace")
	}

	if h.keep == nil || h.secrets == nil {
		return h.fail(c, http.StatusServiceUnavailable, "keep_not_configured")
	}

	ref, err := h.secrets.KeepMetadataRef(c.Request().Context(), wsID, secretID)
	switch {
	case errors.Is(err, repository.ErrKeepMetadataNotFound):
		return h.fail(c, http.StatusNotFound, "not_found")
	case err != nil:
		return h.fail(c, http.StatusServiceUnavailable, "lookup_failed")
	}

	ctx, cancel := context.WithTimeout(c.Request().Context(), keepMetadataTimeout)
	defer cancel()
	md, err := h.keep.Metadata(ctx, ref)
	switch {
	case err == nil:
	case errors.Is(err, secretstore.ErrKeepNotFound), errors.Is(err, secretstore.ErrKeepBadID):
		return h.fail(c, http.StatusNotFound, "keep_not_found")
	case errors.Is(err, secretstore.ErrKeepRateLimited):
		return h.fail(c, http.StatusTooManyRequests, "keep_rate_limited")
	default:
		return h.fail(c, http.StatusServiceUnavailable, "keep_unavailable")
	}
	return middleware.KeepMetadataSuccess(c, md.Version, md.Length)
}

// fail logs the outcome label only (no ids, no ref, no upstream text) and
// writes the closed envelope.
func (h *KeepMetadataHandler) fail(c echo.Context, status int, why string) error {
	log.Printf("[keep-metadata] status=%d why=%s", status, why)
	return middleware.KeepMetadataFailure(c, status)
}
