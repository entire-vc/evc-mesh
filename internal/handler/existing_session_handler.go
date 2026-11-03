package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ExistingSessionHandler serves strict existing-session metadata delivery
// (contract runner-existing-session-metadata-v1): receiver routes under
// /runner/existing-session and sender routes under /existing-session.
type ExistingSessionHandler struct {
	repo    *postgres.ExistingSessionRepo
	runners *postgres.ChatRunnerRepo
}

func NewExistingSessionHandler(repo *postgres.ExistingSessionRepo, runners *postgres.ChatRunnerRepo) *ExistingSessionHandler {
	return &ExistingSessionHandler{repo: repo, runners: runners}
}

var (
	existingSessionErrorCode = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	existingSessionSHA256    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// existingSessionCaller is the plain agent key of the call: a chat-session
// token or an OAuth connector is an agent acting for a user, never a sender or
// a receiver.
func existingSessionCaller(c echo.Context) (ws, agent uuid.UUID, err error) {
	if mw.IsChatSession(c) || c.Get(mw.ContextKeyAuthType) != mw.AuthTypeAgent {
		return uuid.Nil, uuid.Nil, apierror.Unauthorized("agent API key required")
	}
	if _, connector := mw.GetOAuthConnectorUserID(c); connector {
		return uuid.Nil, uuid.Nil, apierror.Forbidden("existing-session routes are not available to OAuth connectors")
	}
	agent, ok := c.Get(mw.ContextKeyAgentID).(uuid.UUID)
	if !ok || agent == uuid.Nil {
		return uuid.Nil, uuid.Nil, apierror.Unauthorized("agent API key required")
	}
	ws, werr := agentFeedWorkspace(c)
	if werr != nil {
		return uuid.Nil, uuid.Nil, apierror.Forbidden("workspace access denied")
	}
	return ws, agent, nil
}

// receiverCaller adds the verified runner of the call: the bearer is mandatory,
// a runner_id the body names must equal it.
func (h *ExistingSessionHandler) receiverCaller(c echo.Context, named string) (ws, agent, runner uuid.UUID, err error) {
	ws, agent, err = existingSessionCaller(c)
	if err != nil {
		return
	}
	token := strings.TrimSpace(c.Request().Header.Get(ChatRunnerTokenHeader))
	if token == "" {
		err = apierror.ForbiddenWithDetails("existing-session receiver calls need the runner credential", "chat_runner_forbidden")
		return
	}
	runner, err = h.runners.AuthenticateRunner(c.Request().Context(), agent, token)
	if err != nil {
		return
	}
	if named != "" {
		if id, perr := uuid.Parse(named); perr != nil || id != runner {
			err = apierror.ForbiddenWithDetails("runner_id does not match the runner credential", "chat_runner_forbidden")
		}
	}
	return
}

func boundedText(s string, limit int) bool {
	return s != "" && len(s) <= limit && utf8.ValidString(s) && strings.TrimSpace(s) == s
}

type existingSessionRegisterRequest struct {
	RunnerID       string      `json:"runner_id"`
	HostID         string      `json:"host_id"`
	Hostname       string      `json:"hostname"`
	SessionID      string      `json:"session_id"`
	Capability     string      `json:"capability"`
	AllowedSenders []uuid.UUID `json:"allowed_sender_agent_ids"`
}

// RegisterReceiver is POST /runner/existing-session/receivers.
func (h *ExistingSessionHandler) RegisterReceiver(c echo.Context) error {
	var req existingSessionRegisterRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, apierror.BadRequest("invalid request body"))
	}
	ws, agent, runner, err := h.receiverCaller(c, req.RunnerID)
	if err != nil {
		return handleError(c, err)
	}
	if !boundedText(req.HostID, 128) || !boundedText(req.Hostname, 255) || !boundedText(req.SessionID, 128) {
		return handleError(c, apierror.BadRequest("host_id, hostname and session_id are required and bounded"))
	}
	if req.Capability != postgres.ExistingSessionCapability {
		return handleError(c, apierror.BadRequest("capability must be readonly_metadata"))
	}
	if len(req.AllowedSenders) > postgres.ExistingSessionMaxSenders {
		return handleError(c, apierror.BadRequest("allowed_sender_agent_ids takes at most 32 agents"))
	}
	for _, id := range req.AllowedSenders {
		if id == uuid.Nil {
			return handleError(c, apierror.BadRequest("allowed_sender_agent_ids has an invalid id"))
		}
	}
	rec, err := h.repo.Register(c.Request().Context(), ws, agent, runner, req.HostID, req.Hostname, req.SessionID, req.AllowedSenders)
	if err != nil {
		return handleError(c, err)
	}
	noStore(c)
	return c.JSON(http.StatusCreated, rec)
}

// RevokeReceiver is DELETE /runner/existing-session/receivers/:receiver_id.
func (h *ExistingSessionHandler) RevokeReceiver(c echo.Context) error {
	ws, agent, runner, err := h.receiverCaller(c, "")
	if err != nil {
		return handleError(c, err)
	}
	id, perr := uuid.Parse(c.Param("receiver_id"))
	if perr != nil || id == uuid.Nil {
		return handleError(c, apierror.BadRequest("invalid receiver_id"))
	}
	if err := h.repo.Revoke(c.Request().Context(), ws, agent, runner, id); err != nil {
		return handleError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// ListDeliveries is GET /runner/existing-session/deliveries?host_id=&session_id=.
func (h *ExistingSessionHandler) ListDeliveries(c echo.Context) error {
	ws, agent, runner, err := h.receiverCaller(c, c.QueryParam("runner_id"))
	if err != nil {
		return handleError(c, err)
	}
	hostID, sessionID := c.QueryParam("host_id"), c.QueryParam("session_id")
	if !boundedText(hostID, 128) || !boundedText(sessionID, 128) {
		return handleError(c, apierror.BadRequest("host_id and session_id are required"))
	}
	out, err := h.repo.Pending(c.Request().Context(), ws, agent, runner, hostID, sessionID)
	if err != nil {
		return handleError(c, err)
	}
	noStore(c)
	return c.JSON(http.StatusOK, map[string]any{"deliveries": out})
}

type existingSessionAckRequest struct {
	RunnerID         string `json:"runner_id"`
	HostID           string `json:"host_id"`
	SessionID        string `json:"session_id"`
	AdmissionRev     int64  `json:"admission_revision"`
	State            string `json:"state"`
	ResultCommentURL string `json:"result_comment_url"`
	ErrorCode        string `json:"error_code"`
}

// AckDelivery is POST /runner/existing-session/deliveries/:delivery_id/ack.
func (h *ExistingSessionHandler) AckDelivery(c echo.Context) error {
	var req existingSessionAckRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, apierror.BadRequest("invalid request body"))
	}
	ws, agent, runner, err := h.receiverCaller(c, req.RunnerID)
	if err != nil {
		return handleError(c, err)
	}
	id, perr := uuid.Parse(c.Param("delivery_id"))
	if perr != nil || id == uuid.Nil {
		return handleError(c, apierror.BadRequest("invalid delivery_id"))
	}
	if !boundedText(req.HostID, 128) || !boundedText(req.SessionID, 128) || req.AdmissionRev <= 0 {
		return handleError(c, apierror.BadRequest("host_id, session_id and admission_revision are required"))
	}
	switch req.State {
	case postgres.ExistingSessionReceived, postgres.ExistingSessionAcknowledged, postgres.ExistingSessionFailed:
	default:
		return handleError(c, apierror.BadRequest("state must be received, acknowledged or failed"))
	}
	if req.ResultCommentURL != "" {
		u, uerr := url.Parse(req.ResultCommentURL)
		if len(req.ResultCommentURL) > 2048 || uerr != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return handleError(c, apierror.BadRequest("result_comment_url must be an https URL"))
		}
	}
	if req.ErrorCode != "" && !existingSessionErrorCode.MatchString(req.ErrorCode) {
		return handleError(c, apierror.BadRequest("error_code must be lowercase letters, digits and underscores"))
	}
	if (req.State == postgres.ExistingSessionFailed) != (req.ErrorCode != "") {
		return handleError(c, apierror.BadRequest("error_code is required for failed and only for failed"))
	}
	rec, err := h.repo.Ack(c.Request().Context(), ws, agent, runner, id, postgres.ExistingSessionAck{
		HostID: req.HostID, SessionID: req.SessionID, Revision: req.AdmissionRev, State: req.State,
		ResultCommentURL: req.ResultCommentURL, ErrorCode: req.ErrorCode})
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, rec)
}

type existingSessionSendRequest struct {
	RequestID string `json:"request_id"`
	Target    struct {
		WorkspaceID uuid.UUID `json:"workspace_id"`
		AgentID     uuid.UUID `json:"agent_id"`
		RunnerID    uuid.UUID `json:"runner_id"`
		HostID      string    `json:"host_id"`
		Hostname    string    `json:"hostname"`
		SessionID   string    `json:"session_id"`
	} `json:"target"`
	ExpectedRevision int64  `json:"expected_admission_revision"`
	Capability       string `json:"capability"`
	Text             string `json:"text"`
	TextSHA256       string `json:"text_sha256"`
}

// SendDelivery is POST /existing-session/deliveries.
func (h *ExistingSessionHandler) SendDelivery(c echo.Context) error {
	ws, sender, err := existingSessionCaller(c)
	if err != nil {
		return handleError(c, err)
	}
	var req existingSessionSendRequest
	if bindErr := c.Bind(&req); bindErr != nil {
		return handleError(c, apierror.BadRequest("invalid request body"))
	}
	rid, perr := uuid.Parse(req.RequestID)
	if perr != nil || rid == uuid.Nil {
		return handleError(c, apierror.BadRequest("request_id must be a UUID"))
	}
	t := req.Target
	if t.WorkspaceID == uuid.Nil || t.AgentID == uuid.Nil || t.RunnerID == uuid.Nil ||
		!boundedText(t.HostID, 128) || !boundedText(t.Hostname, 255) || !boundedText(t.SessionID, 128) {
		return handleError(c, apierror.BadRequest("target must be a complete identity"))
	}
	if req.ExpectedRevision <= 0 {
		return handleError(c, apierror.BadRequest("expected_admission_revision is required"))
	}
	if req.Capability != postgres.ExistingSessionCapability {
		return handleError(c, apierror.BadRequest("capability must be readonly_metadata"))
	}
	if req.Text == "" || len(req.Text) > postgres.ExistingSessionMaxText || !utf8.ValidString(req.Text) {
		return handleError(c, apierror.BadRequest("text must be non-empty UTF-8 up to 16 KiB"))
	}
	sum := sha256.Sum256([]byte(req.Text))
	if !existingSessionSHA256.MatchString(req.TextSHA256) || req.TextSHA256 != hex.EncodeToString(sum[:]) {
		return handleError(c, apierror.BadRequest("text_sha256 does not match the text"))
	}
	rec, created, err := h.repo.Enqueue(c.Request().Context(), ws, sender, postgres.ExistingSessionRequest{
		RequestID: rid, Expected: req.ExpectedRevision, Text: req.Text, TextSHA256: req.TextSHA256,
		Target: postgres.ExistingSessionTarget{WorkspaceID: t.WorkspaceID, AgentID: t.AgentID, RunnerID: t.RunnerID,
			HostID: t.HostID, Hostname: t.Hostname, SessionID: t.SessionID}})
	if err != nil {
		return handleError(c, err)
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	return c.JSON(status, rec)
}

// GetDelivery is GET /existing-session/deliveries/:delivery_id.
func (h *ExistingSessionHandler) GetDelivery(c echo.Context) error {
	ws, sender, err := existingSessionCaller(c)
	if err != nil {
		return handleError(c, err)
	}
	id, perr := uuid.Parse(c.Param("delivery_id"))
	if perr != nil || id == uuid.Nil {
		return handleError(c, apierror.BadRequest("invalid delivery_id"))
	}
	rec, err := h.repo.Get(c.Request().Context(), ws, sender, id)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, rec)
}
