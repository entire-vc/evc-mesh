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
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// GateIntaker opens gate cards (service.GateIntakeService).
type GateIntaker interface {
	Intake(ctx context.Context, p *domain.GateIntakePrincipal, req *domain.GateIntakeRequest) (*domain.GateIntakeResponse, int, error)
}

// GateIntakeHandler serves POST /api/v1/gate-intake (contract CH→Mesh
// gate-create/v1, docs/gate-intake.md). Idempotency is the approval receipt the
// service writes with the card; the route has no response cache (`idempotent`
// middleware) on purpose — a cache would expire, the receipt does not.
type GateIntakeHandler struct {
	svc GateIntaker
}

// NewGateIntakeHandler creates a GateIntakeHandler.
func NewGateIntakeHandler(svc GateIntaker) *GateIntakeHandler { return &GateIntakeHandler{svc: svc} }

// Create opens (or replays) the gate card of one approval.
func (h *GateIntakeHandler) Create(c echo.Context) error {
	p := mw.GetGateIntakePrincipal(c)
	if p == nil { // the route guard already refused this; defence in depth
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("A valid gate-intake token is required"))
	}
	if mt, _, err := mime.ParseMediaType(c.Request().Header.Get(echo.HeaderContentType)); err != nil || mt != echo.MIMEApplicationJSON {
		return c.JSON(http.StatusUnsupportedMediaType, &apierror.Error{Code: http.StatusUnsupportedMediaType,
			ErrorCode: "unsupported_media_type", Message: "Content-Type must be application/json"})
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Response(), c.Request().Body, domain.GateIntakeMaxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return c.JSON(http.StatusRequestEntityTooLarge, &apierror.Error{Code: http.StatusRequestEntityTooLarge,
				ErrorCode: "payload_too_large", Message: "the body is larger than 16 KiB"})
		}
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("the body could not be read"))
	}
	req, perr := domain.ParseGateIntakeRequest(body)
	if perr != nil {
		if perr.Status == http.StatusBadRequest && len(perr.Fields) > 0 {
			return c.JSON(http.StatusBadRequest, apierror.ValidationError(perr.Fields))
		}
		return c.JSON(perr.Status, &apierror.Error{Code: perr.Status, ErrorCode: perr.Code, Message: perr.Message})
	}
	// Presence, not value, decides: a sent but empty (or repeated) header is
	// a mismatch, never "not sent".
	if keys, sent := c.Request().Header[http.CanonicalHeaderKey("Idempotency-Key")]; sent &&
		(len(keys) != 1 || keys[0] != req.ApprovalID.String()) {
		return c.JSON(http.StatusBadRequest, &apierror.Error{Code: http.StatusBadRequest,
			ErrorCode: domain.GateIntakeCodeIdempotencyMismatch,
			Message:   "Idempotency-Key, when sent, must equal approval_id"})
	}
	resp, status, err := h.svc.Intake(c.Request().Context(), p, req)
	if err != nil {
		var ae *apierror.Error
		if errors.As(err, &ae) {
			return c.JSON(ae.Code, ae)
		}
		return err
	}
	if resp.Replayed {
		c.Response().Header().Set("Idempotent-Replayed", "true")
	}
	return c.JSON(status, resp)
}
