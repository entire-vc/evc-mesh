package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ChatSnapshotFreshFor is how long a runner's chat snapshot describes the
// present: two announce periods of the runner (ANNOUNCE_EVERY = 60 s in
// evc-mesh-runner chat_announce.py). An older snapshot answers "unknown",
// never "process gone": a silent runner is not a stopped process.
const ChatSnapshotFreshFor = 2 * 60 * time.Second

// WithRunnerSnapshots turns on the read-only runtime routes; without it they
// answer 503.
func (h *AgentChatHandler) WithRunnerSnapshots(repo repository.ChatRunnerSnapshotRepository) *AgentChatHandler {
	h.snapshots = repo
	return h
}

func chatSnapshotFresh(s *repository.ChatRunnerSnapshot) bool {
	return s != nil && s.Age <= ChatSnapshotFreshFor
}

type chatThreadRuntime struct {
	ThreadID        uuid.UUID  `json:"thread_id"`
	AgentID         uuid.UUID  `json:"agent_id"`
	RunnerID        *string    `json:"runner_id"`
	Fresh           bool       `json:"fresh"`
	ProcessAlive    *bool      `json:"process_alive"`
	SlotHeld        *bool      `json:"slot_held"`
	Busy            *bool      `json:"busy"`
	LastEndAt       *time.Time `json:"last_end_at"`
	ReportedAt      *time.Time `json:"reported_at"`
	ReceivedAt      *time.Time `json:"received_at"`
	AgeSeconds      *int64     `json:"age_seconds"`
	FreshForSeconds int64      `json:"fresh_for_seconds"`
}

// ThreadRuntime is GET /projects/:proj_id/chat/threads/:thread_id/runtime: what
// the newest runner snapshot of the thread's agent says about this thread. The
// thread is resolved like GetThread (own thread, this project), so another
// user's or another workspace's thread is the same 404 thread_not_found.
func (h *AgentChatHandler) ThreadRuntime(c echo.Context) error {
	if h.snapshots == nil {
		return c.JSON(http.StatusServiceUnavailable, apierror.ServiceUnavailable("chat runtime is not configured"))
	}
	s, id, err := h.thread(c)
	if err != nil {
		return handleError(c, err)
	}
	ctx := c.Request().Context()
	th, err := h.repo.GetThread(ctx, s.gate.WorkspaceID, s.project, s.user, id)
	if err != nil {
		return handleError(c, err)
	}
	out := chatThreadRuntime{ThreadID: th.ID, AgentID: th.AgentID, FreshForSeconds: int64(ChatSnapshotFreshFor.Seconds())}
	snap, err := h.snapshots.LatestChatSnapshot(ctx, th.AgentID)
	if err != nil {
		return handleError(c, err)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	if snap == nil {
		return c.JSON(http.StatusOK, out)
	}
	age := int64(snap.Age.Seconds())
	out.RunnerID, out.ReportedAt, out.ReceivedAt, out.AgeSeconds = &snap.RunnerID, &snap.ReportedAt, &snap.ReceivedAt, &age
	out.Fresh = chatSnapshotFresh(snap)
	if !out.Fresh {
		return c.JSON(http.StatusOK, out)
	}
	alive, held, busy := false, false, false
	for _, e := range snap.Sessions {
		if e.ThreadID == th.ID {
			alive, held, busy = e.ProcessAlive, e.SlotHeld, e.Busy
			out.LastEndAt = e.LastEndAt
			break
		}
	}
	out.ProcessAlive, out.SlotHeld, out.Busy = &alive, &held, &busy
	return c.JSON(http.StatusOK, out)
}

type chatRunnerSlots struct {
	MaxPerAgent *int `json:"max_per_agent"`
	Used        *int `json:"used"`
}

type chatRunnerAgentSnapshot struct {
	AgentID       uuid.UUID                     `json:"agent_id"`
	Fresh         bool                          `json:"fresh"`
	ReportedAt    time.Time                     `json:"reported_at"`
	ReceivedAt    time.Time                     `json:"received_at"`
	AgeSeconds    int64                         `json:"age_seconds"`
	Slots         chatRunnerSlots               `json:"slots"`
	SessionsTotal *int                          `json:"sessions_total"`
	AliveTotal    *int                          `json:"alive_total"`
	Sessions      []repository.ChatSessionEntry `json:"sessions"`
}

// RunnerSessions is GET /projects/:proj_id/chat/runners/:runner_id/sessions:
// the runner's latest snapshot for each agent of this workspace it reported.
// Counts cover every session of the agent; entries list only the caller's own
// threads in this project (threads are private to their author). A stale
// snapshot keeps its entries as history but its counts are null (unknown).
func (h *AgentChatHandler) RunnerSessions(c echo.Context) error {
	if h.snapshots == nil {
		return c.JSON(http.StatusServiceUnavailable, apierror.ServiceUnavailable("chat runtime is not configured"))
	}
	s, err := h.scope(c)
	if err != nil {
		return handleError(c, err)
	}
	runner := strings.TrimSpace(c.Param("runner_id"))
	if runner == "" || len(runner) > 128 {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid runner_id"))
	}
	ctx := c.Request().Context()
	snaps, err := h.snapshots.RunnerChatSnapshots(ctx, s.gate.WorkspaceID, runner)
	if err != nil {
		return handleError(c, err)
	}
	if len(snaps) == 0 {
		return c.JSON(http.StatusNotFound, apierror.NotFound("chat runner snapshot"))
	}
	var ids []uuid.UUID
	for _, snap := range snaps {
		for _, e := range snap.Sessions {
			ids = append(ids, e.ThreadID)
		}
	}
	own, err := h.repo.OwnThreadIDs(ctx, s.gate.WorkspaceID, s.project, s.user, ids)
	if err != nil {
		return handleError(c, err)
	}
	agents := make([]chatRunnerAgentSnapshot, 0, len(snaps))
	allFresh := true
	for i := range snaps {
		snap := &snaps[i]
		a := chatRunnerAgentSnapshot{AgentID: snap.AgentID, Fresh: chatSnapshotFresh(snap), ReportedAt: snap.ReportedAt,
			ReceivedAt: snap.ReceivedAt, AgeSeconds: int64(snap.Age.Seconds()),
			Slots: chatRunnerSlots{MaxPerAgent: snap.SlotsMax, Used: snap.SlotsUsed}, Sessions: []repository.ChatSessionEntry{}}
		total, alive := len(snap.Sessions), 0
		for _, e := range snap.Sessions {
			if e.ProcessAlive {
				alive++
			}
			if own[e.ThreadID] {
				a.Sessions = append(a.Sessions, e)
			}
		}
		if a.Fresh {
			a.SessionsTotal, a.AliveTotal = &total, &alive
		} else {
			allFresh = false
		}
		agents = append(agents, a)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, map[string]any{
		"runner_id":         runner,
		"fresh":             allFresh,
		"fresh_for_seconds": int64(ChatSnapshotFreshFor.Seconds()),
		"agents":            agents,
	})
}
