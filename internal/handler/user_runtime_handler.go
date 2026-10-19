package handler

import (
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// UserRuntimeHandler serves operator-level runtime ("My Runner"): the user's own
// document, the agents' read of it by key, and the runtime_manager assignment.
type UserRuntimeHandler struct{ repo *postgres.UserRuntimeRepo }

func NewUserRuntimeHandler(repo *postgres.UserRuntimeRepo) *UserRuntimeHandler {
	return &UserRuntimeHandler{repo: repo}
}

func userRuntimeActor(c echo.Context) postgres.UserRuntimeActor {
	user, _ := mw.GetUserID(c)
	agent, _ := mw.GetAgentID(c)
	_, connector := mw.GetOAuthConnectorUserID(c)
	return postgres.UserRuntimeActor{UserID: user, AgentID: agent, Connector: connector}
}

func userRuntimeAgentParam(c echo.Context) (uuid.UUID, error) {
	id, err := uuid.Parse(c.Param("agent_id"))
	if err != nil || id == uuid.Nil {
		return uuid.Nil, apierror.BadRequest("invalid agent_id")
	}
	return id, nil
}

type limitsBody struct {
	MaxConcurrentTasks *int `json:"max_concurrent_tasks"`
}

func (h *UserRuntimeHandler) AgentRuntime(c echo.Context) error {
	v, err := h.repo.AgentRuntime(c.Request().Context(), userRuntimeActor(c))
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, v)
}

func (h *UserRuntimeHandler) Get(c echo.Context) error {
	v, err := h.repo.Get(c.Request().Context(), userRuntimeActor(c))
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, v)
}

func (h *UserRuntimeHandler) Save(c echo.Context) error {
	var in postgres.UserRuntimeSave
	if err := runtimeBody(c, &in, "if_revision", "config"); err != nil {
		return handleError(c, err)
	}
	v, err := h.repo.Save(c.Request().Context(), userRuntimeActor(c), in)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, v)
}

// SaveAsManager is PUT /agents/me/operator-runtime.
func (h *UserRuntimeHandler) SaveAsManager(c echo.Context) error {
	var in postgres.UserRuntimeSave
	if err := runtimeBody(c, &in, "if_revision", "config"); err != nil {
		return handleError(c, err)
	}
	v, err := h.repo.SaveAsManager(c.Request().Context(), userRuntimeActor(c), in)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, v)
}

func (h *UserRuntimeHandler) History(c echo.Context) error {
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	v, err := h.repo.History(c.Request().Context(), userRuntimeActor(c), limit)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"history": v})
}

func (h *UserRuntimeHandler) limits(c echo.Context, asManager bool) error {
	agent, err := userRuntimeAgentParam(c)
	if err != nil {
		return handleError(c, err)
	}
	var in limitsBody
	if err = runtimeBody(c, &in, "max_concurrent_tasks"); err != nil || in.MaxConcurrentTasks == nil {
		return handleError(c, apierror.BadRequest("max_concurrent_tasks is required"))
	}
	if asManager {
		err = h.repo.SetLimitsAsManager(c.Request().Context(), userRuntimeActor(c), agent, *in.MaxConcurrentTasks)
	} else {
		err = h.repo.SetLimits(c.Request().Context(), userRuntimeActor(c), agent, *in.MaxConcurrentTasks)
	}
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"agent_id": agent, "max_concurrent_tasks": *in.MaxConcurrentTasks})
}

func (h *UserRuntimeHandler) SetLimits(c echo.Context) error { return h.limits(c, false) }

// SetLimitsAsManager is PUT /agents/me/operator-runtime/agents/:agent_id/limits.
func (h *UserRuntimeHandler) SetLimitsAsManager(c echo.Context) error { return h.limits(c, true) }

func (h *UserRuntimeHandler) Managers(c echo.Context) error {
	v, err := h.repo.Managers(c.Request().Context(), userRuntimeActor(c))
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"managers": v})
}

type managerBody struct {
	AgentID uuid.UUID `json:"agent_id"`
}

func managerAgent(c echo.Context) (uuid.UUID, error) {
	var in managerBody
	if err := runtimeBody(c, &in, "agent_id"); err != nil {
		return uuid.Nil, err
	}
	if in.AgentID == uuid.Nil {
		return uuid.Nil, apierror.BadRequest("invalid agent_id")
	}
	return in.AgentID, nil
}

// GrantManager is POST /me/runtime/managers {agent_id}.
func (h *UserRuntimeHandler) GrantManager(c echo.Context) error {
	agent, err := managerAgent(c)
	if err != nil {
		return handleError(c, err)
	}
	if err = h.repo.GrantManager(c.Request().Context(), userRuntimeActor(c), agent); err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"agent_id": agent, "runtime_manager": true})
}

// RevokeManager is POST /me/runtime/managers/revoke {agent_id}.
func (h *UserRuntimeHandler) RevokeManager(c echo.Context) error {
	agent, err := managerAgent(c)
	if err != nil {
		return handleError(c, err)
	}
	if err = h.repo.RevokeManager(c.Request().Context(), userRuntimeActor(c), agent); err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"agent_id": agent, "runtime_manager": false})
}

// OperatorAgents is GET /me/runtime/agents: agents the user operates.
func (h *UserRuntimeHandler) OperatorAgents(c echo.Context) error {
	v, err := h.repo.OperatorAgents(c.Request().Context(), userRuntimeActor(c))
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"agents": v})
}

// OperatorRunners is GET /me/runtime/runners: the user's agents grouped by runner.
func (h *UserRuntimeHandler) OperatorRunners(c echo.Context) error {
	v, err := h.repo.OperatorRunners(c.Request().Context(), userRuntimeActor(c))
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"runners": v})
}
