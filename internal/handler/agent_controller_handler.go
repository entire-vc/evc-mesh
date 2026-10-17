package handler

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// AgentControllerLease is how long an announcement holds without a heartbeat.
const AgentControllerLease = 2 * time.Minute

// RunnerInstanceHeader lets a runner tie its heartbeats to its own lease, so a
// second process sharing the key cannot keep the first one's lease alive.
const RunnerInstanceHeader = "X-Runner-Instance"

// AgentControllerHandler serves POST /agents/me/controller.
type AgentControllerHandler struct {
	repo repository.AgentControllerRepository
}

// NewAgentControllerHandler creates an AgentControllerHandler.
func NewAgentControllerHandler(repo repository.AgentControllerRepository) *AgentControllerHandler {
	return &AgentControllerHandler{repo: repo}
}

type announceControllerRequest struct {
	RunnerID string `json:"runner_id"`
	Name     string `json:"name"`
	Host     string `json:"host"`
	Version  string `json:"version"`
}

// Announce takes (or renews) the agent's controller lease. Agent key only.
func (h *AgentControllerHandler) Announce(c echo.Context) error {
	agentID, ok := c.Get("agent_id").(uuid.UUID)
	if !ok {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("agent API key required"))
	}
	var req announceControllerRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	req.RunnerID = strings.TrimSpace(req.RunnerID)
	req.Name = strings.TrimSpace(req.Name)
	req.Host = strings.TrimSpace(req.Host)
	if req.RunnerID == "" || req.Name == "" || req.Host == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("runner_id, name and host are required"))
	}
	if len(req.RunnerID) > 128 || len(req.Name) > 128 || len(req.Host) > 255 || len(req.Version) > 64 {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("runner_id/name/host/version too long"))
	}

	cur, held, err := h.repo.Claim(c.Request().Context(), repository.AgentController{
		AgentID: agentID, RunnerID: req.RunnerID, RunnerName: req.Name, Host: req.Host, Version: req.Version,
	}, AgentControllerLease)
	if err != nil {
		return handleError(c, err)
	}
	if !held {
		e := apierror.Conflict(fmt.Sprintf("agent is led by runner %s on host %s", cur.RunnerName, cur.Host))
		e.Details = fmt.Sprintf("runner=%s host=%s version=%s lease_expires_at=%s", cur.RunnerName, cur.Host, cur.Version, cur.LeaseExpiresAt.UTC().Format(time.RFC3339))
		return c.JSON(http.StatusConflict, e)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"agent_id":         agentID,
		"runner_id":        cur.RunnerID,
		"runner":           cur.RunnerName,
		"host":             cur.Host,
		"version":          cur.Version,
		"lease_expires_at": cur.LeaseExpiresAt,
		"lease_seconds":    int(AgentControllerLease.Seconds()),
	})
}
