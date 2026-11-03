package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// TransitionReceiptHandler serves the opt-in receipt-bound product done path
// and the per-project policy that enables it.
type TransitionReceiptHandler struct {
	svc     *service.TransitionReceiptService
	taskSvc service.TaskService
}

func NewTransitionReceiptHandler(svc *service.TransitionReceiptService, taskSvc service.TaskService) *TransitionReceiptHandler {
	return &TransitionReceiptHandler{svc: svc, taskSvc: taskSvc}
}

// ProductDone handles POST /tasks/:task_id/transitions/product-done.
// 200 carries the receipt; replayed=true marks an idempotent repeat.
func (h *TransitionReceiptHandler) ProductDone(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskSvc)
	if err != nil {
		return handleError(c, err)
	}
	var req domain.ProductDoneRequest
	raw, rerr := io.ReadAll(io.LimitReader(c.Request().Body, 1<<20))
	if rerr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if derr := dec.Decode(&req); derr != nil {
		if field, ok := unknownJSONField(derr); ok {
			e := apierror.BadRequestWithDetails("unknown field in request body", field)
			return c.JSON(e.StatusCode(), e)
		}
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	// Canonical digest of the body as received (wire contract v1), key excluded.
	if d, derr := domain.PayloadDigestV1(raw); derr == nil {
		req.PayloadDigestV1 = d
	}
	receipt, replayed, err := h.svc.ProductDone(c.Request().Context(), taskID, req)
	if err != nil {
		return h.refusal(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"receipt": receipt, "replayed": replayed})
}

// ListReceipts handles GET /tasks/:task_id/transition-receipts.
func (h *TransitionReceiptHandler) ListReceipts(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskSvc)
	if err != nil {
		return handleError(c, err)
	}
	items, err := h.svc.ListReceipts(c.Request().Context(), taskID)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"receipts": items})
}

type transitionPolicyRequest struct {
	Enabled           bool                     `json:"enabled"`
	EnforceDone       bool                     `json:"enforce_done"`
	AllowedProducers  []domain.AllowedProducer `json:"allowed_producers"`
	AllowedAcceptors  []uuid.UUID              `json:"allowed_acceptors"`
	AllowedExemptions []string                 `json:"allowed_exemptions"`
	ScopeID           *string                  `json:"scope_id"`
}

// GetPolicy handles GET /projects/:proj_id/transition-receipt-policy.
func (h *TransitionReceiptHandler) GetPolicy(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project id"))
	}
	p, err := h.svc.GetPolicy(c.Request().Context(), projID)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, p)
}

// SetPolicy handles PUT /projects/:proj_id/transition-receipt-policy.
func (h *TransitionReceiptHandler) SetPolicy(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project id"))
	}
	var req transitionPolicyRequest
	if apiErr := bindStrictJSON(c, &req); apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}
	if req.EnforceDone && !req.Enabled {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("enforce_done requires enabled"))
	}
	for _, p := range req.AllowedProducers {
		if p.Role != "" && p.Role != domain.ProducerRoleLead {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("producer role may only be empty or lead"))
		}
	}
	if req.ScopeID != nil && (len(*req.ScopeID) > 120 || strings.TrimSpace(*req.ScopeID) != *req.ScopeID) {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("scope_id must be at most 120 chars without surrounding spaces"))
	}
	for _, e := range req.AllowedExemptions {
		if e != domain.TransitionClassResearch && e != domain.TransitionClassTrivial {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("allowed_exemptions may contain only research, trivial"))
		}
	}
	p, err := h.svc.SetPolicy(c.Request().Context(), &domain.TransitionReceiptPolicy{
		ProjectID: projID, Enabled: req.Enabled, EnforceDone: req.EnforceDone, AllowedProducers: req.AllowedProducers,
		AllowedAcceptors: req.AllowedAcceptors, AllowedExemptions: req.AllowedExemptions,
		ScopeID: scopeValue(req.ScopeID), ScopeIDSet: req.ScopeID != nil,
	})
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, p)
}

// RecordAcceptance handles POST /tasks/:task_id/acceptance-intents.
func (h *TransitionReceiptHandler) RecordAcceptance(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskSvc)
	if err != nil {
		return handleError(c, err)
	}
	var req domain.AcceptanceIntentRequest
	if apiErr := bindStrictJSON(c, &req); apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}
	it, err := h.svc.RecordAcceptance(c.Request().Context(), taskID, req)
	if err != nil {
		return h.refusal(c, err)
	}
	return c.JSON(http.StatusCreated, it)
}

// ListAcceptances handles GET /tasks/:task_id/acceptance-intents.
func (h *TransitionReceiptHandler) ListAcceptances(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskSvc)
	if err != nil {
		return handleError(c, err)
	}
	items, err := h.svc.ListAcceptances(c.Request().Context(), taskID)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"acceptance_intents": items})
}

// RevokeAcceptance handles POST /tasks/:task_id/acceptance-intents/:intent_id/revoke.
func (h *TransitionReceiptHandler) RevokeAcceptance(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskSvc)
	if err != nil {
		return handleError(c, err)
	}
	intentID, err := uuid.Parse(c.Param("intent_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid intent id"))
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if apiErr := bindStrictJSON(c, &body); apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}
	if err := h.svc.RevokeAcceptance(c.Request().Context(), taskID, intentID, body.Reason); err != nil {
		return h.refusal(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// refusal maps a typed business refusal to a stable machine code; everything
// else goes through the shared error mapping (CAS conflict -> 409).
func (h *TransitionReceiptHandler) refusal(c echo.Context, err error) error {
	var r *domain.TransitionRefusal
	if errors.As(err, &r) {
		status := http.StatusUnprocessableEntity
		if r == domain.ErrReceiptKeyConflict {
			status = http.StatusConflict
		}
		return c.JSON(status, map[string]any{"code": r.Code, "error_code": r.Code, "message": r.Message})
	}
	return handleError(c, err)
}

func scopeValue(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Capabilities handles GET /projects/:proj_id/product-done-capabilities.
func (h *TransitionReceiptHandler) Capabilities(c echo.Context) error {
	projID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project id"))
	}
	caps, err := h.svc.Capabilities(c.Request().Context(), projID)
	if err != nil {
		return h.refusal(c, err)
	}
	return c.JSON(http.StatusOK, caps)
}

// ProductDoneAcceptance handles GET /tasks/:task_id/product-done-acceptance.
func (h *TransitionReceiptHandler) ProductDoneAcceptance(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskSvc)
	if err != nil {
		return handleError(c, err)
	}
	view, err := h.svc.ProductDoneAcceptance(c.Request().Context(), taskID)
	if err != nil {
		return h.refusal(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"acceptance": view})
}
