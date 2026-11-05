package handler

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// NativeReviewStore is the narrow persistence surface the handler needs.
type NativeReviewStore interface {
	IssueReceipt(ctx context.Context, actor uuid.UUID, req domain.NativeIssueRequest) (*domain.NativePacket, error)
	ReadReceipt(ctx context.Context, actor, id uuid.UUID) (*domain.NativePacket, error)
	CreateAuthority(ctx context.Context, actor uuid.UUID, req domain.NativeAuthorityRequest) (uuid.UUID, error)
	RecordDelivery(ctx context.Context, actor uuid.UUID, registration, eventID string, receiptID uuid.UUID) error
	RevokeAuthority(ctx context.Context, authorityID, actor uuid.UUID, reason string) error
	Reserve(ctx context.Context, actor uuid.UUID, req domain.NativeReserveRequest) (*domain.NativeReservation, error)
	GetReservation(ctx context.Context, actor, authorityID uuid.UUID) (*domain.NativeReservation, error)
}

// NativeReviewHandler serves native-review-admission/v1. The authenticated
// credential is the only source of issuer/consumer identity; enrollment itself is
// an operator action and has no public endpoint.
type NativeReviewHandler struct{ store NativeReviewStore }

func NewNativeReviewHandler(store NativeReviewStore) *NativeReviewHandler {
	return &NativeReviewHandler{store: store}
}

func nativeActor(c echo.Context) (uuid.UUID, error) {
	id, _ := actorctx.FromContext(c.Request().Context())
	if id == uuid.Nil {
		return uuid.Nil, apierror.Unauthorized("an authenticated actor is required")
	}
	return id, nil
}

func nativeDecode(c echo.Context, out any) error {
	raw, err := io.ReadAll(io.LimitReader(c.Request().Body, 1<<20+1))
	if err != nil || len(raw) > 1<<20 {
		return domain.NativeRefuse("schema_invalid", "request body unreadable or over 1 MiB")
	}
	return domain.StrictDecodeNative(raw, out)
}

func (h *NativeReviewHandler) fail(c echo.Context, err error) error {
	var r *domain.NativeRefusal
	if errors.As(err, &r) {
		status := http.StatusUnprocessableEntity
		switch r.Code {
		case "idempotency_payload_mismatch", "authority_consumed", "delivery_conflict":
			status = http.StatusConflict
		case "commit_uncertain":
			status = http.StatusAccepted // consumed/uncertain: read back, never retry execution
		}
		return c.JSON(status, map[string]any{"code": r.Code, "error_code": r.Code, "message": r.Message})
	}
	return handleError(c, err)
}

// IssueReceipt handles POST /native-review/receipts.
func (h *NativeReviewHandler) IssueReceipt(c echo.Context) error {
	actor, err := nativeActor(c)
	if err != nil {
		return handleError(c, err)
	}
	var req domain.NativeIssueRequest
	if err = nativeDecode(c, &req); err != nil {
		return h.fail(c, err)
	}
	pk, err := h.store.IssueReceipt(c.Request().Context(), actor, req)
	if err != nil {
		return h.fail(c, err)
	}
	// The body IS the flat signed packet, no envelope: the ordinary publisher
	// forwards it unchanged and A2 admits it without edits.
	return c.JSON(http.StatusCreated, pk)
}

// GetReceipt handles GET /native-review/receipts/:receipt_id (readback, not permission).
func (h *NativeReviewHandler) GetReceipt(c echo.Context) error {
	actor, err := nativeActor(c)
	if err != nil {
		return handleError(c, err)
	}
	id, err := uuid.Parse(c.Param("receipt_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid receipt id"))
	}
	pk, err := h.store.ReadReceipt(c.Request().Context(), actor, id)
	if err != nil {
		return h.fail(c, err)
	}
	// Same flat signed packet as issued; readback is never permission.
	return c.JSON(http.StatusOK, pk)
}

// CreateAuthority handles POST /native-review/authorities.
func (h *NativeReviewHandler) CreateAuthority(c echo.Context) error {
	actor, err := nativeActor(c)
	if err != nil {
		return handleError(c, err)
	}
	var req domain.NativeAuthorityRequest
	if err = nativeDecode(c, &req); err != nil {
		return h.fail(c, err)
	}
	id, err := h.store.CreateAuthority(c.Request().Context(), actor, req)
	if err != nil {
		return h.fail(c, err)
	}
	return c.JSON(http.StatusCreated, map[string]any{"authority_id": id})
}

type nativeDeliveryRequest struct {
	Schema       string    `json:"schema"`
	Registration string    `json:"native_registration"`
	EventID      string    `json:"native_event_id"`
	ReceiptID    uuid.UUID `json:"receipt_id"`
}

// RecordDelivery handles POST /native-review/deliveries (enrolled publisher only).
func (h *NativeReviewHandler) RecordDelivery(c echo.Context) error {
	actor, err := nativeActor(c)
	if err != nil {
		return handleError(c, err)
	}
	var req nativeDeliveryRequest
	if err = nativeDecode(c, &req); err != nil {
		return h.fail(c, err)
	}
	if req.Schema != domain.NativeReviewSchema {
		return h.fail(c, domain.NativeRefuse("schema_invalid", "schema must be "+domain.NativeReviewSchema))
	}
	if err = h.store.RecordDelivery(c.Request().Context(), actor, req.Registration, req.EventID, req.ReceiptID); err != nil {
		return h.fail(c, err)
	}
	return c.JSON(http.StatusCreated, map[string]any{"linked": true})
}

type nativeRevokeRequest struct {
	Schema string `json:"schema"`
	Reason string `json:"reason"`
}

// RevokeAuthority handles POST /native-review/authorities/:authority_id/revoke.
func (h *NativeReviewHandler) RevokeAuthority(c echo.Context) error {
	actor, err := nativeActor(c)
	if err != nil {
		return handleError(c, err)
	}
	id, err := uuid.Parse(c.Param("authority_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid authority id"))
	}
	var req nativeRevokeRequest
	if err = nativeDecode(c, &req); err != nil {
		return h.fail(c, err)
	}
	if req.Schema != domain.NativeReviewSchema || req.Reason == "" {
		return h.fail(c, domain.NativeRefuse("schema_invalid", "schema and reason required"))
	}
	if err = h.store.RevokeAuthority(c.Request().Context(), id, actor, req.Reason); err != nil {
		return h.fail(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"revoked": true})
}

// Reserve handles POST /native-review/reservations: the atomic check-and-reserve.
func (h *NativeReviewHandler) Reserve(c echo.Context) error {
	actor, err := nativeActor(c)
	if err != nil {
		return handleError(c, err)
	}
	var req domain.NativeReserveRequest
	if err = nativeDecode(c, &req); err != nil {
		return h.fail(c, err)
	}
	res, err := h.store.Reserve(c.Request().Context(), actor, req)
	if err != nil {
		return h.fail(c, err)
	}
	return c.JSON(http.StatusOK, res)
}

// GetReservation handles GET /native-review/authorities/:authority_id/reservation.
func (h *NativeReviewHandler) GetReservation(c echo.Context) error {
	actor, err := nativeActor(c)
	if err != nil {
		return handleError(c, err)
	}
	id, err := uuid.Parse(c.Param("authority_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid authority id"))
	}
	res, err := h.store.GetReservation(c.Request().Context(), actor, id)
	if err != nil {
		return h.fail(c, err)
	}
	return c.JSON(http.StatusOK, res)
}
