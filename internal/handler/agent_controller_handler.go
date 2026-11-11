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

// ChatSnapshotMaxSessions bounds the sessions one announce may report.
const ChatSnapshotMaxSessions = 256

// AgentControllerHandler serves POST /agents/me/controller.
type AgentControllerHandler struct {
	repo      repository.AgentControllerRepository
	snapshots repository.ChatRunnerSnapshotRepository
}

// WithChatSnapshots stores the chat-session snapshot an announce may carry.
// Without it the snapshot is validated and dropped.
func (h *AgentControllerHandler) WithChatSnapshots(repo repository.ChatRunnerSnapshotRepository) *AgentControllerHandler {
	h.snapshots = repo
	return h
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

	// The chat-session snapshot (docs/extensions/runner-chat-protocol.md §12).
	// Absent (or null) sessions leave the stored snapshot as it is; [] says
	// the runner holds no chat session for this agent.
	Sessions   *[]chatSnapshotSession `json:"sessions"`
	Slots      *chatSnapshotSlots     `json:"slots"`
	ReportedAt *time.Time             `json:"reported_at"`
}

type chatSnapshotSession struct {
	ThreadID     string     `json:"thread_id"`
	Agent        string     `json:"agent"`
	ProcessAlive bool       `json:"process_alive"`
	SlotHeld     *bool      `json:"slot_held"`
	Busy         bool       `json:"busy"`
	LastEndAt    *time.Time `json:"last_end_at"`
}

type chatSnapshotSlots struct {
	MaxPerAgent *int `json:"max_per_agent"`
	Used        *int `json:"used"`
}

// chatSnapshot validates the optional snapshot; nil when the announce has none.
func (req announceControllerRequest) chatSnapshot(agentID uuid.UUID) (*repository.ChatRunnerSnapshot, error) {
	if req.Sessions == nil {
		return nil, nil
	}
	if len(*req.Sessions) > ChatSnapshotMaxSessions {
		return nil, fmt.Errorf("sessions: at most %d entries", ChatSnapshotMaxSessions)
	}
	out := &repository.ChatRunnerSnapshot{AgentID: agentID, RunnerID: req.RunnerID, ReportedAt: time.Now().UTC(),
		Sessions: make([]repository.ChatSessionEntry, 0, len(*req.Sessions))}
	if req.ReportedAt != nil {
		out.ReportedAt = req.ReportedAt.UTC()
	}
	seen := make(map[uuid.UUID]bool, len(*req.Sessions))
	for i, s := range *req.Sessions {
		id, err := uuid.Parse(strings.TrimSpace(s.ThreadID))
		if err != nil || id == uuid.Nil {
			return nil, fmt.Errorf("sessions[%d].thread_id must be a UUID", i)
		}
		if seen[id] {
			return nil, fmt.Errorf("sessions[%d].thread_id is listed twice", i)
		}
		seen[id] = true
		agent := strings.TrimSpace(s.Agent)
		if len(agent) > 128 {
			return nil, fmt.Errorf("sessions[%d].agent too long", i)
		}
		// Older snapshot senders list held sessions only. Preserve that default
		// while accepting explicit false for retained, stopped sessions.
		held := true
		if s.SlotHeld != nil {
			held = *s.SlotHeld
		}
		out.Sessions = append(out.Sessions, repository.ChatSessionEntry{
			ThreadID: id, Agent: agent, ProcessAlive: s.ProcessAlive, SlotHeld: held, Busy: s.Busy, LastEndAt: s.LastEndAt})
	}
	if req.Slots != nil {
		if (req.Slots.MaxPerAgent != nil && *req.Slots.MaxPerAgent < 0) || (req.Slots.Used != nil && *req.Slots.Used < 0) {
			return nil, fmt.Errorf("slots must not be negative")
		}
		out.SlotsMax, out.SlotsUsed = req.Slots.MaxPerAgent, req.Slots.Used
	}
	return out, nil
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
	snap, err := req.chatSnapshot(agentID)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest(err.Error()))
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
	out := map[string]any{
		"agent_id":         agentID,
		"runner_id":        cur.RunnerID,
		"runner":           cur.RunnerName,
		"host":             cur.Host,
		"version":          cur.Version,
		"lease_expires_at": cur.LeaseExpiresAt,
		"lease_seconds":    int(AgentControllerLease.Seconds()),
	}
	if snap != nil && h.snapshots != nil {
		saved, err := h.snapshots.SaveChatSnapshot(c.Request().Context(), *snap)
		if err != nil {
			return handleError(c, err)
		}
		out["chat_snapshot_saved"] = saved
	}
	return c.JSON(http.StatusOK, out)
}
