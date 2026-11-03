package handler

import (
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/hostlocalqa"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

const hostLocalQAMaxBody = 32 * 1024

// HostLocalQAHandler serves host_local_qa_assignment_v1: receiver and guard
// routes under /runner/host-local-qa/v1 and sender routes under
// /host-local-qa/v1. It is a separate surface from ExistingSessionHandler: no
// legacy admission, payload or setting is read here or accepted there.
//
// The capability is OFF unless enabled: every route then answers an
// authenticated caller 403 host_local_qa_disabled before it reads the body or
// touches state.
type HostLocalQAHandler struct {
	repo    *postgres.HostLocalQARepo
	runners *postgres.ChatRunnerRepo
	policy  *hostlocalqa.Policy
	enabled bool
}

func NewHostLocalQAHandler(repo *postgres.HostLocalQARepo, runners *postgres.ChatRunnerRepo, policy *hostlocalqa.Policy, enabled bool) *HostLocalQAHandler {
	return &HostLocalQAHandler{repo: repo, runners: runners, policy: policy, enabled: enabled}
}

func (h *HostLocalQAHandler) disabled() error {
	if h.enabled {
		return nil
	}
	return apierror.ForbiddenWithDetails("host-local QA assignment is not enabled", "host_local_qa_disabled")
}

// caller is the plain agent key of the call; chat-session tokens and OAuth
// connectors are never a sender or a receiver.
func (h *HostLocalQAHandler) caller(c echo.Context) (ws, agent uuid.UUID, err error) {
	return existingSessionCaller(c)
}

// receiver adds the verified runner credential of the call.
func (h *HostLocalQAHandler) receiver(c echo.Context) (ws, agent, runner uuid.UUID, err error) {
	ws, agent, err = h.caller(c)
	if err != nil {
		return
	}
	token := strings.TrimSpace(c.Request().Header.Get(ChatRunnerTokenHeader))
	if token == "" {
		err = apierror.ForbiddenWithDetails("host-local QA receiver calls need the runner credential", "chat_runner_forbidden")
		return
	}
	runner, err = h.runners.AuthenticateRunner(c.Request().Context(), agent, token)
	return
}

func readQABody(c echo.Context) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, hostLocalQAMaxBody+1))
	if err != nil || len(body) > hostLocalQAMaxBody {
		return nil, apierror.BadRequestWithDetails("invalid request body", "host_local_qa_invalid_body")
	}
	return body, nil
}

func qaInvalid(err error) error {
	return apierror.BadRequestWithDetails("request body is not the frozen host-local QA shape: "+err.Error(), "host_local_qa_invalid_body")
}

func deliveryParam(c echo.Context) (uuid.UUID, error) {
	id, err := uuid.Parse(c.Param("delivery_id"))
	if err != nil || id == uuid.Nil {
		return uuid.Nil, apierror.BadRequest("invalid delivery_id")
	}
	return id, nil
}

// RegisterReceiver is POST /runner/host-local-qa/v1/receivers.
func (h *HostLocalQAHandler) RegisterReceiver(c echo.Context) error {
	ws, agent, runner, err := h.receiver(c)
	if err != nil {
		return handleError(c, err)
	}
	if err = h.disabled(); err != nil {
		return handleError(c, err)
	}
	body, err := readQABody(c)
	if err != nil {
		return handleError(c, err)
	}
	reg, err := h.policy.ParseRegistration(body)
	if err != nil {
		return handleError(c, qaInvalid(err))
	}
	// Workspace and receiver identity come from the credentials; the body must
	// agree with them and with the one fixed policy, never select them.
	t := reg.Target
	if !h.policy.Admitted(t) || t.WorkspaceID != ws || t.AgentID != agent || t.RunnerID != runner {
		return handleError(c, apierror.ForbiddenWithDetails("host-local QA target is not available to this caller", "host_local_qa_target_denied"))
	}
	rec, err := h.repo.Register(c.Request().Context(), ws, agent, runner, reg)
	if err != nil {
		return handleError(c, err)
	}
	noStore(c)
	return c.JSON(http.StatusCreated, rec)
}

// RevokeReceiver is DELETE /runner/host-local-qa/v1/receivers/:receiver_id.
func (h *HostLocalQAHandler) RevokeReceiver(c echo.Context) error {
	ws, agent, runner, err := h.receiver(c)
	if err != nil {
		return handleError(c, err)
	}
	if err = h.disabled(); err != nil {
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

// ListDeliveries is GET /runner/host-local-qa/v1/deliveries?host_id=&session_id=.
func (h *HostLocalQAHandler) ListDeliveries(c echo.Context) error {
	ws, agent, runner, err := h.receiver(c)
	if err != nil {
		return handleError(c, err)
	}
	if err = h.disabled(); err != nil {
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

// AckDelivery is POST /runner/host-local-qa/v1/deliveries/:delivery_id/ack.
func (h *HostLocalQAHandler) AckDelivery(c echo.Context) error {
	ws, agent, runner, err := h.receiver(c)
	if err != nil {
		return handleError(c, err)
	}
	if err = h.disabled(); err != nil {
		return handleError(c, err)
	}
	id, err := deliveryParam(c)
	if err != nil {
		return handleError(c, err)
	}
	body, err := readQABody(c)
	if err != nil {
		return handleError(c, err)
	}
	b, err := h.policy.ParseAck(body)
	if err != nil {
		return handleError(c, qaInvalid(err))
	}
	rec, err := h.repo.Ack(c.Request().Context(), ws, agent, runner, id, b)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, rec)
}

// CheckDelivery is POST /runner/host-local-qa/v1/deliveries/:delivery_id/check,
// the authoritative guard consulted before every normal mutation. The scoped
// credential travels only in the request headers, never in the body.
func (h *HostLocalQAHandler) CheckDelivery(c echo.Context) error {
	ws, agent, runner, err := h.receiver(c)
	if err != nil {
		return handleError(c, err)
	}
	if err = h.disabled(); err != nil {
		return handleError(c, err)
	}
	id, err := deliveryParam(c)
	if err != nil {
		return handleError(c, err)
	}
	body, err := readQABody(c)
	if err != nil {
		return handleError(c, err)
	}
	b, err := h.policy.ParseCheck(body)
	if err != nil {
		return handleError(c, qaInvalid(err))
	}
	res, err := h.repo.Check(c.Request().Context(), ws, agent, runner, id, b)
	if err != nil {
		return handleError(c, err)
	}
	noStore(c)
	return c.JSON(http.StatusOK, res)
}

// SendDelivery is POST /host-local-qa/v1/deliveries.
func (h *HostLocalQAHandler) SendDelivery(c echo.Context) error {
	ws, sender, err := h.caller(c)
	if err != nil {
		return handleError(c, err)
	}
	if err = h.disabled(); err != nil {
		return handleError(c, err)
	}
	body, err := readQABody(c)
	if err != nil {
		return handleError(c, err)
	}
	env, err := h.policy.ParseSend(body)
	if err != nil {
		return handleError(c, qaInvalid(err))
	}
	// The sender's workspace comes from its key; the fixed policy names the one
	// admitted workspace, agent and hostname, and only listed senders may send.
	if !h.policy.Admitted(env.Target) || env.Target.WorkspaceID != ws || !h.policy.SenderAllowed(sender) {
		return handleError(c, apierror.ForbiddenWithDetails("host-local QA target is not available to this caller", "host_local_qa_target_denied"))
	}
	rec, created, err := h.repo.Send(c.Request().Context(), ws, sender, env)
	if err != nil {
		return handleError(c, err)
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	return c.JSON(status, rec)
}

// GetDelivery is GET /host-local-qa/v1/deliveries/:delivery_id.
func (h *HostLocalQAHandler) GetDelivery(c echo.Context) error {
	ws, sender, err := h.caller(c)
	if err != nil {
		return handleError(c, err)
	}
	if err = h.disabled(); err != nil {
		return handleError(c, err)
	}
	id, err := deliveryParam(c)
	if err != nil {
		return handleError(c, err)
	}
	rec, err := h.repo.Get(c.Request().Context(), ws, sender, id)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, rec)
}
