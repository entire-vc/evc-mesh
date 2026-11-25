package handler

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// LeadAcceptContexter is the service behind the two lead-accept routes
// (service.LeadAcceptService).
type LeadAcceptContexter interface {
	Context(ctx context.Context, p *domain.LeadAcceptPrincipal, req *domain.LeadAcceptRequest) (*domain.LeadAcceptContext, error)
	Attest(ctx context.Context, p *domain.LeadAcceptPrincipal, a *domain.LeadAcceptAttestation) (*service.LeadAcceptAttestationReceipt, error)
}

// LeadAcceptHandler serves GET /api/v1/lead-accept-context and
// POST /api/v1/lead-accept-registry/attestations (contract CH→Mesh
// lead-accept-context/v1, docs/lead-accept-context.md). Every answer carries
// Cache-Control: no-store.
type LeadAcceptHandler struct {
	svc LeadAcceptContexter
}

// NewLeadAcceptHandler creates a LeadAcceptHandler.
func NewLeadAcceptHandler(svc LeadAcceptContexter) *LeadAcceptHandler {
	return &LeadAcceptHandler{svc: svc}
}

func leadAcceptReply(c echo.Context, status int, v any) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(status, v)
}

func leadAcceptError(c echo.Context, err error) error {
	var ae *apierror.Error
	if errors.As(err, &ae) {
		return leadAcceptReply(c, ae.Code, ae)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return err
}

// Context answers the narrow projection for the reader's bound request.
func (h *LeadAcceptHandler) Context(c echo.Context) error {
	p := mw.GetLeadAcceptPrincipal(c)
	if p == nil || p.Kind != domain.LeadAcceptKindReader { // the guards already refused this
		return leadAcceptReply(c, http.StatusUnauthorized, apierror.Unauthorized("A valid lead-accept token is required"))
	}
	req, bad := domain.ParseLeadAcceptQuery(c.Request().URL.RawQuery)
	if bad != nil {
		return leadAcceptReply(c, http.StatusBadRequest, apierror.ValidationError(bad))
	}
	out, err := h.svc.Context(c.Request().Context(), p, req)
	if err != nil {
		return leadAcceptError(c, err)
	}
	return leadAcceptReply(c, http.StatusOK, out)
}

// Attest accepts one registry observation of the publisher.
func (h *LeadAcceptHandler) Attest(c echo.Context) error {
	p := mw.GetLeadAcceptPrincipal(c)
	if p == nil || p.Kind != domain.LeadAcceptKindPublisher {
		return leadAcceptReply(c, http.StatusUnauthorized, apierror.Unauthorized("A valid lead-accept token is required"))
	}
	if mt, _, err := mime.ParseMediaType(c.Request().Header.Get(echo.HeaderContentType)); err != nil || mt != echo.MIMEApplicationJSON {
		return leadAcceptReply(c, http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"content_type": "must be application/json"}))
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Response(), c.Request().Body, domain.LeadAcceptMaxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return leadAcceptReply(c, http.StatusBadRequest, apierror.ValidationError(map[string]string{
				"body": "must be at most 16 KiB"}))
		}
		return leadAcceptReply(c, http.StatusBadRequest, apierror.BadRequest("the body could not be read"))
	}
	a, bad := domain.ParseLeadAcceptAttestation(body)
	if bad != nil {
		return leadAcceptReply(c, http.StatusBadRequest, apierror.ValidationError(bad))
	}
	out, err := h.svc.Attest(c.Request().Context(), p, a)
	if err != nil {
		return leadAcceptError(c, err)
	}
	return leadAcceptReply(c, http.StatusCreated, out)
}
