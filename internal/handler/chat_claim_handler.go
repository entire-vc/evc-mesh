package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

const (
	chatProtocolVersion = "1"
	chatClaimMaxAgents  = 50
	chatClaimMaxTurns   = 5
	chatClaimDefTimeout = 30
	chatClaimMaxTimeout = 120

	// Without Redis the claim re-reads the database this often (N20). With
	// Redis the same re-check is only a safety net for a lost hint and for a
	// lease that lapses while nobody publishes.
	chatClaimPollNoRedis = 2 * time.Second
	chatClaimPollRedis   = 5 * time.Second
	// How long the claim waits for Redis to confirm its subscription.
	chatClaimSubscribeWait = time.Second

	// RunnerIDHeader names the runner instance; with the agent key it is the
	// holder identity every later call is fenced against.
	chatRunnerIDHeader = "X-Chat-Runner-Id"
)

// ChatRunnerStore is the database half of the runner protocol (postgres.ChatRunnerRepo).
type ChatRunnerStore interface {
	ClaimTurns(ctx context.Context, p postgres.ChatClaimParams) ([]postgres.ChatClaimedTurn, error)
	RenewLease(ctx context.Context, workspaceID uuid.UUID, agentIDs []uuid.UUID, runnerID, turnID uuid.UUID,
		claimToken string, leaseSeconds int) (*postgres.ChatLease, error)
	ActiveTurns(ctx context.Context, workspaceID uuid.UUID, agentIDs []uuid.UUID, runnerID uuid.UUID) ([]postgres.ChatActiveTurn, error)
}

// ChatClaimHandler serves /runner/chat/*: the runner side of Chat with Agent
// (Mesh Doc agent-chat-runner-protocol). The runner authenticates with the API
// key of the agent it serves (protocol Q1, per-agent key option): the key is
// the proof of which agent it may claim for, so `agents=` is limited to the
// key's own agent and a runner serving several agents polls once per agent.
type ChatClaimHandler struct {
	repo     ChatRunnerStore
	rdb      *redis.Client
	store    ChatAttachmentStore
	shutdown <-chan struct{}
}

func NewChatClaimHandler(repo ChatRunnerStore, rdb *redis.Client, store ChatAttachmentStore) *ChatClaimHandler {
	return &ChatClaimHandler{repo: repo, rdb: rdb, store: store}
}

// SetShutdownSignal makes parked long-polls answer 204 when the server stops.
func (h *ChatClaimHandler) SetShutdownSignal(ch <-chan struct{}) { h.shutdown = ch }

type chatClaimCaller struct {
	workspaceID uuid.UUID
	agentID     uuid.UUID
	runnerID    uuid.UUID
}

// authorize runs the checks every runner call starts with: protocol version,
// then who is calling. A chat-session token (or anything else that is not a
// plain agent key) is refused: it is the agent acting for a user, not the runner.
func (h *ChatClaimHandler) authorize(c echo.Context) (*chatClaimCaller, error) {
	return authorizeChatRunner(c)
}

// authorizeChatRunner is the runner identity check shared by every /runner/chat route.
func authorizeChatRunner(c echo.Context) (*chatClaimCaller, error) {
	hdr := c.Response().Header()
	hdr.Set("X-Chat-Protocol", chatProtocolVersion)
	hdr.Set("X-Chat-Protocol-Supported", chatProtocolVersion)
	if c.Request().Header.Get("X-Chat-Protocol") != chatProtocolVersion {
		return nil, &apierror.Error{Code: http.StatusBadRequest, ErrorCode: "chat_protocol_unsupported",
			Message: "unsupported chat protocol version", Context: map[string]any{"supported": []int{1}}}
	}
	if mw.IsChatSession(c) || c.Get(mw.ContextKeyAuthType) != mw.AuthTypeAgent {
		return nil, apierror.Unauthorized("agent API key required")
	}
	// An OAuth connector is an agent identity borrowed by a user's consent; the
	// runner is the agent's own key, never a delegated one.
	if _, connector := mw.GetOAuthConnectorUserID(c); connector {
		return nil, apierror.Forbidden("runner routes are not available to OAuth connectors")
	}
	agentID, ok := c.Get(mw.ContextKeyAgentID).(uuid.UUID)
	if !ok || agentID == uuid.Nil {
		return nil, apierror.Unauthorized("agent API key required")
	}
	wsID, err := agentFeedWorkspace(c)
	if err != nil {
		return nil, apierror.Forbidden("workspace access denied")
	}
	runnerID, err := uuid.Parse(strings.TrimSpace(c.Request().Header.Get(chatRunnerIDHeader)))
	if err != nil || runnerID == uuid.Nil {
		return nil, apierror.BadRequest("X-Chat-Runner-Id must be a UUID")
	}
	return &chatClaimCaller{workspaceID: wsID, agentID: agentID, runnerID: runnerID}, nil
}

func runnerError(c echo.Context, err error) error {
	if apiErr, ok := err.(*apierror.Error); ok {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}
	return handleError(c, err)
}

// Claim handles GET /runner/chat/claim?agents=a,b&timeout=30&max=1 (§4.2).
func (h *ChatClaimHandler) Claim(c echo.Context) error {
	caller, err := h.authorize(c)
	if err != nil {
		return runnerError(c, err)
	}

	// Everything is validated before the caller is parked: a malformed request
	// is reported at once, not after the timeout.
	var agents []uuid.UUID
	var foreign []string
	seen := map[uuid.UUID]bool{}
	for _, raw := range strings.Split(c.QueryParam("agents"), ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		id, perr := uuid.Parse(raw)
		if perr != nil {
			return runnerError(c, apierror.BadRequest("agents must be a comma-separated list of UUIDs"))
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		agents = append(agents, id)
		if id != caller.agentID {
			foreign = append(foreign, id.String())
		}
	}
	if len(agents) == 0 {
		return runnerError(c, apierror.BadRequest("agents is required"))
	}
	if len(agents) > chatClaimMaxAgents {
		return runnerError(c, apierror.BadRequest("at most 50 agents per claim"))
	}
	if len(foreign) > 0 {
		return c.JSON(http.StatusForbidden, &apierror.Error{Code: http.StatusForbidden, ErrorCode: "chat_agent_not_served",
			Message: "the runner does not serve these agents", Context: map[string]any{"agent_ids": foreign}})
	}
	timeoutSecs, maxTurns := chatClaimDefTimeout, 1
	if raw := c.QueryParam("timeout"); raw != "" {
		n, perr := strconv.Atoi(raw)
		if perr != nil {
			return runnerError(c, apierror.BadRequest("timeout must be an integer"))
		}
		timeoutSecs = n
	}
	if timeoutSecs < 1 {
		timeoutSecs = 1
	}
	if timeoutSecs > chatClaimMaxTimeout {
		timeoutSecs = chatClaimMaxTimeout
	}
	if raw := c.QueryParam("max"); raw != "" {
		n, perr := strconv.Atoi(raw)
		if perr != nil || n < 1 {
			return runnerError(c, apierror.BadRequest("max must be a positive integer"))
		}
		maxTurns = n
	}
	if maxTurns > chatClaimMaxTurns {
		maxTurns = chatClaimMaxTurns
	}

	ctx := c.Request().Context()
	// Subscribe first, check the database second: a hint published between the
	// two is then still waiting in the channel (no lost wake-up).
	var hints <-chan *redis.Message
	poll := chatClaimPollNoRedis
	if h.rdb != nil {
		sub := h.rdb.Subscribe(ctx, service.ChatRunnerChannel(caller.agentID))
		defer func() { _ = sub.Close() }()
		// Subscribe only sends the command; until Redis confirms it a publish is
		// dropped. Without the confirmation the claim keeps the short poll.
		subCtx, cancel := context.WithTimeout(ctx, chatClaimSubscribeWait)
		_, rerr := sub.Receive(subCtx)
		cancel()
		if rerr == nil {
			hints = sub.Channel()
			poll = chatClaimPollRedis
		}
	}
	deadline := time.NewTimer(time.Duration(timeoutSecs) * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(poll)
	defer tick.Stop()

	for {
		claimed, cerr := h.repo.ClaimTurns(ctx, postgres.ChatClaimParams{
			WorkspaceID: caller.workspaceID, AgentIDs: agents, RunnerID: caller.runnerID, Max: maxTurns,
			LeaseSeconds: postgres.ChatLeaseSeconds, MaxAttempts: postgres.ChatMaxAttempts,
		})
		if cerr != nil {
			if ctx.Err() != nil {
				return nil
			}
			return handleError(c, cerr)
		}
		if len(claimed) > 0 {
			return c.JSON(http.StatusOK, h.claimResponse(c, claimed))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-h.shutdown:
			return c.NoContent(http.StatusNoContent)
		case <-deadline.C:
			return c.NoContent(http.StatusNoContent)
		case <-hints:
		case <-tick.C:
		}
	}
}

func (h *ChatClaimHandler) claimResponse(c echo.Context, claimed []postgres.ChatClaimedTurn) map[string]any {
	turns := make([]map[string]any, 0, len(claimed))
	for _, t := range claimed {
		atts := make([]map[string]any, 0, len(t.Attachments))
		for _, a := range t.Attachments {
			item := map[string]any{"attachment_id": a.ID, "file_name": a.FileName, "mime": a.Mime, "size": a.Size,
				"width": a.Width, "height": a.Height}
			if h.store != nil {
				if url, err := h.store.GetPresignedURL(c.Request().Context(), chatAttachmentKey(t.WorkspaceID, a.ID),
					chatDownloadTTL, a.Mime, a.FileName); err == nil {
					item["download_url"] = url
				}
			}
			atts = append(atts, item)
		}
		turns = append(turns, map[string]any{
			"turn_id": t.TurnID, "thread_id": t.ThreadID, "agent_id": t.AgentID, "project_id": t.ProjectID,
			"workspace_id": t.WorkspaceID, "attempt": t.Attempt, "claim_token": t.ClaimToken,
			"execution_epoch": t.ExecutionEpoch, "lease_seconds": postgres.ChatLeaseSeconds, "lease_until": t.LeaseUntil,
			"created_at": t.CreatedAt, "expires_at": t.ExpiresAt,
			"requested_by":   map[string]any{"user_id": t.RequestedBy, "display_name": t.DisplayName},
			"message":        map[string]any{"message_id": t.MessageID, "body": t.Body, "attachments": atts},
			"thread_runtime": runtimeForRunner(t.Runtime),
			"settings":       map[string]any{"session_idle_minutes": 10, "max_attachment_mb": 25},
		})
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return map[string]any{
		"protocol": 1, "turns": turns,
		// Runner orders (stop_session, delete_transcript, cancel_turn) arrive with
		// the commands unit; the field is present so runners can rely on it.
		"commands":    []any{},
		"server_time": time.Now().UTC(),
	}
}

type chatLeaseRequest struct {
	ClaimToken string `json:"claim_token"`
}

// RenewLease handles POST /runner/chat/turns/:id/lease (§5.2).
func (h *ChatClaimHandler) RenewLease(c echo.Context) error {
	caller, err := h.authorize(c)
	if err != nil {
		return runnerError(c, err)
	}
	turnID, perr := uuid.Parse(c.Param("id"))
	if perr != nil || turnID == uuid.Nil {
		// Same answer as an unknown turn: no existence oracle.
		return c.JSON(http.StatusNotFound, &apierror.Error{Code: http.StatusNotFound, ErrorCode: postgres.ChatErrTurnNotFound, Message: "turn not found"})
	}
	var req chatLeaseRequest
	if berr := c.Bind(&req); berr != nil || strings.TrimSpace(req.ClaimToken) == "" {
		return runnerError(c, apierror.BadRequest("claim_token is required"))
	}
	lease, err := h.repo.RenewLease(c.Request().Context(), caller.workspaceID, []uuid.UUID{caller.agentID},
		caller.runnerID, turnID, req.ClaimToken, postgres.ChatLeaseSeconds)
	if err != nil {
		return runnerError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"lease_until": lease.LeaseUntil, "state": lease.State,
		"cancel_requested": lease.CancelRequested, "execution_epoch": lease.ExecutionEpoch,
	})
}

// Active handles GET /runner/chat/turns/active: the claimed or streaming turns
// this runner id still holds, so a restarted runner can resume or abandon them
// instead of waiting for the lease to lapse.
func (h *ChatClaimHandler) Active(c echo.Context) error {
	caller, err := h.authorize(c)
	if err != nil {
		return runnerError(c, err)
	}
	turns, err := h.repo.ActiveTurns(c.Request().Context(), caller.workspaceID, []uuid.UUID{caller.agentID}, caller.runnerID)
	if err != nil {
		return runnerError(c, err)
	}
	items := make([]map[string]any, 0, len(turns))
	for _, t := range turns {
		items = append(items, map[string]any{
			"turn_id": t.TurnID, "thread_id": t.ThreadID, "agent_id": t.AgentID, "state": t.State,
			"attempt": t.Attempt, "claim_token": t.ClaimToken, "lease_until": t.LeaseUntil.UTC(),
			"execution_epoch": t.ExecutionEpoch,
		})
	}
	noStore(c)
	return c.JSON(http.StatusOK, map[string]any{"protocol": chatProtocolVersion, "turns": items})
}

// runtimeForRunner exposes exactly the four fields of §4.2 thread_runtime, null
// when unknown; anything else the column may hold stays server-side.
func runtimeForRunner(raw []byte) map[string]any {
	out := map[string]any{"runner_id": nil, "harness": nil, "profile": nil, "session_id": nil}
	var stored map[string]any
	if json.Unmarshal(raw, &stored) != nil {
		return out
	}
	for k := range out {
		if v, ok := stored[k]; ok {
			out[k] = v
		}
	}
	return out
}
