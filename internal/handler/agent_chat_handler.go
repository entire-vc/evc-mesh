package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions/agentchat"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/internal/storage"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// AgentChatHandler serves /projects/:proj_id/chat/threads... and the chat
// settings routes of the agent-chat extension (agent-chat-openapi). Every
// thread route runs the gate first (user JWT only, extension on, role allowed)
// and then resolves the thread by the caller's own user id.
type AgentChatHandler struct {
	repo     *postgres.AgentChatRepo
	conns    repository.IntegrationRepository
	projects repository.ProjectIntegrationRepository
	events   ChatEvents
	atts     *postgres.ChatAttachmentRepo
	store    ChatAttachmentStore
	hints    *redis.Client
}

// WithAttachments lets SendMessage confirm a presigned upload by looking the
// object up in storage: the client PUTs straight to S3 and tells nobody.
func (h *AgentChatHandler) WithAttachments(repo *postgres.ChatAttachmentRepo, store ChatAttachmentStore) *AgentChatHandler {
	h.atts, h.store = repo, store
	return h
}

// confirmUploads stamps uploaded_at on the attachments whose object is in
// storage with the declared size. A missing or wrong-sized object stays
// unconfirmed, so SendMessage answers attachment_not_found for it.
func (h *AgentChatHandler) confirmUploads(ctx context.Context, s *chatScope, thread uuid.UUID, ids []uuid.UUID) error {
	if len(ids) == 0 || h.atts == nil || h.store == nil {
		return nil
	}
	pending, err := h.atts.PendingInThread(ctx, s.gate.WorkspaceID, thread, ids)
	if err != nil {
		return err
	}
	for _, att := range pending {
		size, serr := h.store.StatObject(ctx, att.S3Key)
		if errors.Is(serr, storage.ErrNotFound) {
			continue
		}
		if serr != nil {
			return apierror.ServiceUnavailable("object storage is unavailable")
		}
		if size != att.Size {
			continue
		}
		if err := h.atts.MarkUploaded(ctx, att.ID); err != nil {
			return err
		}
	}
	return nil
}

// WithRunnerHints wakes the agent's parked runner claim when a turn is queued;
// without it the claim only notices the turn on its safety-net tick.
func (h *AgentChatHandler) WithRunnerHints(rdb *redis.Client) *AgentChatHandler {
	h.hints = rdb
	return h
}

// WithEvents turns on WS events for thread, message and turn changes.
func (h *AgentChatHandler) WithEvents(e ChatEvents) *AgentChatHandler {
	h.events = e
	return h
}

// publishTurnState sends chat.turn.state with the thread's header state read
// after the change. If that read fails nothing is sent: a made-up state would
// be worse than none, and the client re-reads the thread on reconnect.
func (h *AgentChatHandler) publishTurnState(ctx context.Context, s *chatScope, thread uuid.UUID, turn postgres.ChatTurnRef) {
	th, err := h.repo.GetThread(ctx, s.gate.WorkspaceID, s.project, s.user, thread)
	if err != nil {
		chatEmit("turn state session lookup", err)
		return
	}
	chatEmit("turn state", h.events.TurnState(ctx, h.ref(s, thread), chatTurnRef(turn), th.SessionState, nil))
}

func (h *AgentChatHandler) ref(s *chatScope, thread uuid.UUID) service.ChatThreadRef {
	return chatThreadRef(s.gate.WorkspaceID, s.project, thread, s.user)
}

func NewAgentChatHandler(repo *postgres.AgentChatRepo, conns repository.IntegrationRepository, projects repository.ProjectIntegrationRepository) *AgentChatHandler {
	return &AgentChatHandler{repo: repo, conns: conns, projects: projects}
}

func chatIDParam(c echo.Context, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil || id == uuid.Nil {
		return uuid.Nil, apierror.BadRequest("invalid " + name)
	}
	return id, nil
}

// threadScope is the gate result plus the ids every thread route needs.
type chatScope struct {
	gate    *postgres.ChatGate
	project uuid.UUID
	user    uuid.UUID
}

func (h *AgentChatHandler) scope(c echo.Context) (*chatScope, error) {
	project, err := chatIDParam(c, "proj_id")
	if err != nil {
		return nil, err
	}
	actor := userRuntimeActor(c)
	gate, err := h.repo.Gate(c.Request().Context(), actor, project)
	if err != nil {
		return nil, err
	}
	return &chatScope{gate: gate, project: project, user: actor.UserID}, nil
}

func (h *AgentChatHandler) thread(c echo.Context) (*chatScope, uuid.UUID, error) {
	s, err := h.scope(c)
	if err != nil {
		return nil, uuid.Nil, err
	}
	id, err := chatIDParam(c, "thread_id")
	if err != nil {
		// A malformed id names no thread: same answer as an unknown one.
		return nil, uuid.Nil, postgres.ChatThreadNotFound()
	}
	return s, id, nil
}

// ListThreads is GET /projects/:proj_id/chat/threads.
func (h *AgentChatHandler) ListThreads(c echo.Context) error {
	s, err := h.scope(c)
	if err != nil {
		return handleError(c, err)
	}
	var agent uuid.UUID
	if raw := c.QueryParam("agent_id"); raw != "" {
		if agent, err = uuid.Parse(raw); err != nil {
			return handleError(c, apierror.BadRequest("invalid agent_id"))
		}
	}
	limit := 0
	if raw := c.QueryParam("limit"); raw != "" {
		if err = json.Unmarshal([]byte(raw), &limit); err != nil || limit < 1 {
			return handleError(c, apierror.BadRequest("invalid limit"))
		}
	}
	page, err := h.repo.ListThreads(c.Request().Context(), s.gate.WorkspaceID, s.project, s.user, agent, limit, c.QueryParam("cursor"))
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, page)
}

type createThreadBody struct {
	AgentID uuid.UUID `json:"agent_id"`
	Title   string    `json:"title"`
}

// CreateThread is POST /projects/:proj_id/chat/threads.
func (h *AgentChatHandler) CreateThread(c echo.Context) error {
	s, err := h.scope(c)
	if err != nil {
		return handleError(c, err)
	}
	var body createThreadBody
	if err = c.Bind(&body); err != nil || body.AgentID == uuid.Nil {
		return handleError(c, apierror.BadRequest("agent_id is required"))
	}
	th, err := h.repo.CreateThread(c.Request().Context(), s.gate, s.project, s.user, body.AgentID, body.Title)
	if err != nil {
		return handleError(c, err)
	}
	if h.events != nil {
		chatEmit("thread created", h.events.ThreadChanged(c.Request().Context(), h.ref(s, th.ID), "created", th))
	}
	return c.JSON(http.StatusCreated, th)
}

// GetThread is GET .../threads/:thread_id.
func (h *AgentChatHandler) GetThread(c echo.Context) error {
	s, id, err := h.thread(c)
	if err != nil {
		return handleError(c, err)
	}
	th, err := h.repo.GetThread(c.Request().Context(), s.gate.WorkspaceID, s.project, s.user, id)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, th)
}

type renameThreadBody struct {
	Title string `json:"title"`
}

// RenameThread is PATCH .../threads/:thread_id.
func (h *AgentChatHandler) RenameThread(c echo.Context) error {
	s, id, err := h.thread(c)
	if err != nil {
		return handleError(c, err)
	}
	var body renameThreadBody
	if err = c.Bind(&body); err != nil {
		return handleError(c, apierror.BadRequest("invalid body"))
	}
	th, err := h.repo.RenameThread(c.Request().Context(), s.gate.WorkspaceID, s.project, s.user, id, body.Title)
	if err != nil {
		return handleError(c, err)
	}
	if h.events != nil {
		chatEmit("thread renamed", h.events.ThreadChanged(c.Request().Context(), h.ref(s, id), "renamed", th))
	}
	return c.JSON(http.StatusOK, th)
}

// ListMessages is GET .../threads/:thread_id/messages.
func (h *AgentChatHandler) ListMessages(c echo.Context) error {
	s, id, err := h.thread(c)
	if err != nil {
		return handleError(c, err)
	}
	limit := 0
	if raw := c.QueryParam("limit"); raw != "" {
		if err = json.Unmarshal([]byte(raw), &limit); err != nil || limit < 1 {
			return handleError(c, apierror.BadRequest("invalid limit"))
		}
	}
	page, err := h.repo.ListMessages(c.Request().Context(), s.gate.WorkspaceID, s.project, s.user, id, limit, c.QueryParam("before"))
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, page)
}

type sendMessageBody struct {
	ClientMessageID string      `json:"client_message_id"`
	Body            string      `json:"body"`
	AttachmentIDs   []uuid.UUID `json:"attachment_ids"`
}

// SendMessage is POST .../threads/:thread_id/messages.
func (h *AgentChatHandler) SendMessage(c echo.Context) error {
	s, id, err := h.thread(c)
	if err != nil {
		return handleError(c, err)
	}
	var body sendMessageBody
	if err = c.Bind(&body); err != nil {
		return handleError(c, apierror.BadRequest("invalid body"))
	}
	if err = h.confirmUploads(c.Request().Context(), s, id, body.AttachmentIDs); err != nil {
		return handleError(c, err)
	}
	out, err := h.repo.SendMessage(c.Request().Context(), s.gate, s.project, s.user, id,
		postgres.SendInput{ClientMessageID: body.ClientMessageID, Body: body.Body, AttachmentIDs: body.AttachmentIDs})
	if err != nil {
		return handleError(c, err)
	}
	// After the commit: the woken claim must find the turn in the database.
	if out.AgentID != uuid.Nil && out.Turn.State == "queued" {
		service.PublishChatRunnerHint(c.Request().Context(), h.hints, out.AgentID, service.ChatHintTurn, out.Turn.ID)
	}
	if h.events != nil {
		ctx := c.Request().Context()
		chatEmit("message created", h.events.MessageCreated(ctx, h.ref(s, id), chatMessagePayload(out.Message)))
		h.publishTurnState(ctx, s, id, out.Turn)
	}
	return c.JSON(http.StatusAccepted, out)
}

// StopTurn is POST .../threads/:thread_id/stop.
func (h *AgentChatHandler) StopTurn(c echo.Context) error {
	s, id, err := h.thread(c)
	if err != nil {
		return handleError(c, err)
	}
	out, err := h.repo.StopTurn(c.Request().Context(), s.gate.WorkspaceID, s.project, s.user, id)
	if err != nil {
		return handleError(c, err)
	}
	if h.events != nil && out.Stopped && out.Turn != nil {
		h.publishTurnState(c.Request().Context(), s, id, *out.Turn)
	}
	return c.JSON(http.StatusOK, out)
}

// workspaceChatSettings is WorkspaceChatSettings of the OpenAPI.
type workspaceChatSettings struct {
	HistoryRetentionDays int `json:"history_retention_days"`
	MaxAttachmentMB      int `json:"max_attachment_mb"`
	SessionIdleMinutes   int `json:"session_idle_minutes"`
	OfflineExpiryHours   int `json:"offline_expiry_hours"`
}

func toWorkspaceChatSettings(s agentchat.WorkspaceSettings) workspaceChatSettings {
	return workspaceChatSettings{s.HistoryRetentionDays, s.MaxAttachmentMB, s.SessionIdleMinutes, s.OfflineExpiryHours}
}

func settingsMap(raw json.RawMessage) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	return m
}

// GetWorkspaceSettings is GET /workspaces/:ws_id/chat/settings.
func (h *AgentChatHandler) GetWorkspaceSettings(c echo.Context) error {
	ws, err := chatIDParam(c, "ws_id")
	if err != nil {
		return handleError(c, err)
	}
	row, err := h.conns.GetByProvider(c.Request().Context(), ws, domain.IntegrationProvider(agentchat.ID))
	if err != nil {
		return handleError(c, err)
	}
	s := agentchat.DefaultWorkspaceSettings()
	if row != nil {
		if s, err = agentchat.ParseWorkspaceSettings(settingsMap(row.Config)); err != nil {
			return handleError(c, err)
		}
	}
	return c.JSON(http.StatusOK, toWorkspaceChatSettings(s))
}

// PutWorkspaceSettings is PUT /workspaces/:ws_id/chat/settings. The body
// replaces the settings; a workspace without a row is connected by the write.
func (h *AgentChatHandler) PutWorkspaceSettings(c echo.Context) error {
	ws, err := chatIDParam(c, "ws_id")
	if err != nil {
		return handleError(c, err)
	}
	// Not c.Bind: it would merge the ws_id path parameter into the map.
	var body map[string]any
	if err = json.NewDecoder(c.Request().Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		return handleError(c, apierror.BadRequest("invalid body"))
	}
	if err = agentchat.CheckWorkspaceKeys(body); err != nil {
		return handleError(c, apierror.Unprocessable("invalid settings", err.Error()))
	}
	s, err := agentchat.ParseWorkspaceSettings(body)
	if err != nil {
		return handleError(c, apierror.Unprocessable("invalid settings", err.Error()))
	}
	if err = agentchat.ValidateWorkspaceSettings(s); err != nil {
		return handleError(c, apierror.Unprocessable("invalid settings", err.Error()))
	}
	cfg, _ := json.Marshal(toWorkspaceChatSettings(s))
	active := true
	if existing, err := h.conns.GetByProvider(c.Request().Context(), ws, domain.IntegrationProvider(agentchat.ID)); err != nil {
		return handleError(c, err)
	} else if existing != nil {
		active = existing.IsActive
	}
	now := time.Now().UTC()
	if err := h.conns.Upsert(c.Request().Context(), &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: ws,
		Provider: domain.IntegrationProvider(agentchat.ID), Config: cfg, IsActive: active, CreatedAt: now, UpdatedAt: now}); err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, toWorkspaceChatSettings(s))
}

// projectChatSettings is ProjectChatSettings of the OpenAPI.
type projectChatSettings struct {
	Enabled      bool        `json:"enabled"`
	Agents       []uuid.UUID `json:"agents"`
	AllowedRoles []string    `json:"allowed_roles,omitempty"`
}

func (h *AgentChatHandler) loadProjectSettings(c echo.Context, project uuid.UUID) (projectChatSettings, error) {
	out := projectChatSettings{Agents: []uuid.UUID{}, AllowedRoles: agentchat.DefaultAllowedRoles()}
	row, err := h.projects.Get(c.Request().Context(), project, agentchat.ID)
	if err != nil || row == nil {
		return out, err
	}
	s, err := agentchat.ParseProjectSettings(settingsMap(row.Settings))
	if err != nil {
		return out, err
	}
	out.Enabled, out.AllowedRoles = row.Enabled, s.AllowedRoles
	if s.Agents != nil {
		out.Agents = s.Agents
	}
	return out, nil
}

// GetProjectSettings is GET /projects/:proj_id/chat/settings. Workspace
// owners and admins read everything; a plain member gets enabled and agents.
func (h *AgentChatHandler) GetProjectSettings(c echo.Context) error {
	project, err := chatIDParam(c, "proj_id")
	if err != nil {
		return handleError(c, err)
	}
	out, err := h.loadProjectSettings(c, project)
	if err != nil {
		return handleError(c, err)
	}
	if role, _ := c.Get(mw.ContextKeyWorkspaceRole).(string); role != domain.RoleOwner && role != domain.RoleAdmin {
		out.AllowedRoles = nil
	}
	return c.JSON(http.StatusOK, out)
}

// PutProjectSettings is PUT /projects/:proj_id/chat/settings (workspace admin).
// Every chosen agent must be a project member that a workspace runner serves.
func (h *AgentChatHandler) PutProjectSettings(c echo.Context) error {
	project, err := chatIDParam(c, "proj_id")
	if err != nil {
		return handleError(c, err)
	}
	var body projectChatSettings
	dec := json.NewDecoder(c.Request().Body)
	dec.DisallowUnknownFields()
	if err = dec.Decode(&body); err != nil {
		return handleError(c, apierror.Unprocessable("invalid settings", err.Error()))
	}
	s := agentchat.ProjectSettings{Agents: body.Agents, AllowedRoles: body.AllowedRoles}
	if s.AllowedRoles == nil {
		s.AllowedRoles = agentchat.DefaultAllowedRoles()
	}
	if s.Agents == nil {
		s.Agents = []uuid.UUID{}
	}
	if err = agentchat.ValidateProjectSettings(s); err != nil {
		return handleError(c, apierror.Unprocessable("invalid settings", err.Error()))
	}
	if body.Enabled && len(s.Agents) == 0 {
		return handleError(c, apierror.Unprocessable("invalid settings", "agents is required to enable the chat"))
	}
	ctx := c.Request().Context()
	if len(s.Agents) > 0 {
		served, err := h.repo.ServedAgents(ctx, project, s.Agents)
		if err != nil {
			return handleError(c, err)
		}
		for _, a := range s.Agents {
			if !served[a] {
				return handleError(c, &apierror.Error{Code: http.StatusUnprocessableEntity, ErrorCode: postgres.ChatCodeAgentNA,
					Message: "agent is not served by a runner of this workspace or is not a project member", Details: a.String()})
			}
		}
	}
	settings, _ := json.Marshal(map[string]any{"agents": s.Agents, "allowed_roles": s.AllowedRoles})
	now := time.Now().UTC()
	if err := h.projects.Upsert(ctx, &domain.ProjectIntegration{ID: uuid.New(), ProjectID: project, Type: agentchat.ID,
		ExtensionID: agentchat.ID, Enabled: body.Enabled, Settings: settings, CreatedAt: now, UpdatedAt: now}); err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, projectChatSettings{Enabled: body.Enabled, Agents: s.Agents, AllowedRoles: s.AllowedRoles})
}
