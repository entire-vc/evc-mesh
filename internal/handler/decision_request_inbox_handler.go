package handler

import (
	"context"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Limits of GET /agents/me/decision-requests.
const (
	DefaultDecisionInboxLimit = 100
	MaxDecisionInboxLimit     = 200
)

// decisionRequestInbox is the read the handler needs from
// postgres.DecisionRequestRepo.
type decisionRequestInbox interface {
	ListOpenForAgent(ctx context.Context, agentID, workspaceID uuid.UUID, limit int) ([]domain.InboxDecisionRequest, error)
}

// DecisionRequestInboxHandler lets an addressee's runner discover the open
// questions other agents asked it, so it answers on the asking card
// (answer_decision) without a separate wake card per question.
type DecisionRequestInboxHandler struct {
	inbox decisionRequestInbox
}

// NewDecisionRequestInboxHandler creates the handler.
func NewDecisionRequestInboxHandler(inbox decisionRequestInbox) *DecisionRequestInboxHandler {
	return &DecisionRequestInboxHandler{inbox: inbox}
}

// ListMine handles GET /agents/me/decision-requests?state=open&limit=N.
//
// The workspace is the one the presented agent key authenticated into, never a
// request parameter: a key issued for one workspace lists only that
// workspace's questions, even for an agent that serves several.
func (h *DecisionRequestInboxHandler) ListMine(c echo.Context) error {
	agentIDVal := c.Get(mw.ContextKeyAgentID)
	if agentIDVal == nil {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("agent API key required"))
	}
	agentID, ok := agentIDVal.(uuid.UUID)
	if !ok {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid agent_id in context"))
	}
	workspaceID, err := mw.GetAgentAuthWorkspaceID(c)
	if err != nil || workspaceID == uuid.Nil {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
	}

	if state := c.QueryParam("state"); state != "" && state != string(domain.DecisionOpen) {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"state": "only open is supported"}))
	}
	limit := DefaultDecisionInboxLimit
	if raw := c.QueryParam("limit"); raw != "" {
		n, convErr := strconv.Atoi(raw)
		if convErr != nil || n < 1 {
			return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
				"limit": "must be a positive integer"}))
		}
		limit = min(n, MaxDecisionInboxLimit)
	}

	items, err := h.inbox.ListOpenForAgent(c.Request().Context(), agentID, workspaceID, limit)
	if err != nil {
		return handleError(c, err)
	}
	if items == nil {
		items = []domain.InboxDecisionRequest{}
	}
	return c.JSON(http.StatusOK, map[string]any{"items": items})
}
