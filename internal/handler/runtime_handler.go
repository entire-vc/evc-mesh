package handler

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type RuntimeHandler struct{ repo *postgres.RuntimeRepo }

func NewRuntimeHandler(repo *postgres.RuntimeRepo) *RuntimeHandler {
	return &RuntimeHandler{repo: repo}
}

func runtimeActor(c echo.Context) postgres.RuntimeActor {
	user, _ := mw.GetUserID(c)
	agent, _ := mw.GetAgentID(c)
	authWS, _ := mw.GetAgentAuthWorkspaceID(c)
	_, connector := mw.GetOAuthConnectorUserID(c)
	return postgres.RuntimeActor{UserID: user, AgentID: agent, AuthWorkspaceID: authWS, Connector: connector}
}

func runtimeWorkspace(c echo.Context) (uuid.UUID, error) {
	ws, err := uuid.Parse(c.Param("ws_id"))
	if err != nil || ws == uuid.Nil {
		return uuid.Nil, apierror.BadRequest("invalid workspace_id")
	}
	return ws, nil
}

func runtimeOwner(c echo.Context) (uuid.UUID, error) {
	owner, err := uuid.Parse(c.QueryParam("resource_owner_workspace_id"))
	if err != nil || owner == uuid.Nil {
		return uuid.Nil, apierror.BadRequest("resource_owner_workspace_id is required")
	}
	return owner, nil
}

func runtimeBody(c echo.Context, target any, required ...string) error {
	data, err := io.ReadAll(http.MaxBytesReader(c.Response(), c.Request().Body, 1<<20))
	if err != nil || domain.DecodeRuntimeJSON(data, target) != nil {
		return apierror.BadRequest("invalid runtime request")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return apierror.BadRequest("invalid runtime request")
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return apierror.BadRequest("missing required runtime field")
		}
	}
	return nil
}

func (h *RuntimeHandler) Inventory(c echo.Context) error {
	ws, err := runtimeWorkspace(c)
	if err != nil {
		return handleError(c, err)
	}
	value, err := h.repo.Inventory(c.Request().Context(), ws, runtimeActor(c))
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, value)
}

func (h *RuntimeHandler) Save(c echo.Context) error {
	ws, err := runtimeWorkspace(c)
	if err != nil {
		return handleError(c, err)
	}
	var input postgres.RuntimeSave
	if err = runtimeBody(c, &input, "if_revision", "enabled", "config"); err != nil {
		return handleError(c, err)
	}
	value, err := h.repo.Save(c.Request().Context(), ws, runtimeActor(c), input)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, value)
}

func (h *RuntimeHandler) Report(c echo.Context) error {
	ws, err := runtimeWorkspace(c)
	if err != nil {
		return handleError(c, err)
	}
	var input domain.RuntimeReport
	if err = runtimeBody(c, &input, "schema_version", "revision", "digest", "status", "capabilities", "emergency_paused", "pools"); err != nil {
		return handleError(c, err)
	}
	if err = h.repo.Report(c.Request().Context(), ws, c.Param("controller_ref"), runtimeActor(c), input); err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]bool{"recorded": true, "launch_authorized": false})
}

func (h *RuntimeHandler) Binding(c echo.Context) error {
	ws, err := runtimeWorkspace(c)
	if err != nil {
		return handleError(c, err)
	}
	owner, err := runtimeOwner(c)
	if err != nil {
		return handleError(c, err)
	}
	value, err := h.repo.Binding(c.Request().Context(), owner, ws, c.Param("binding_id"), runtimeActor(c))
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, value)
}

func (h *RuntimeHandler) Admit(c echo.Context) error {
	ws, err := runtimeWorkspace(c)
	if err != nil {
		return handleError(c, err)
	}
	owner, err := runtimeOwner(c)
	if err != nil {
		return handleError(c, err)
	}
	var input postgres.RuntimeAdmissionInput
	if err = runtimeBody(c, &input, "if_revision", "enabled", "permitted_profiles"); err != nil {
		return handleError(c, err)
	}
	value, err := h.repo.Admit(c.Request().Context(), owner, ws, c.Param("binding_id"), runtimeActor(c), input)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, value)
}

// Preview explains which profile the resolver would pick. It is advisory:
// the response always carries launch_authorized=false.
func (h *RuntimeHandler) Preview(c echo.Context) error {
	ws, err := runtimeWorkspace(c)
	if err != nil {
		return handleError(c, err)
	}
	owner, err := runtimeOwner(c)
	if err != nil {
		return handleError(c, err)
	}
	var input domain.RuntimePreviewInput
	if err = runtimeBody(c, &input, "purpose", "required_capabilities"); err != nil {
		return handleError(c, err)
	}
	value, err := h.repo.Preview(c.Request().Context(), owner, ws, c.Param("binding_id"), runtimeActor(c), input)
	if err != nil {
		return handleError(c, err)
	}
	value.LaunchAuthorized = false
	return c.JSON(http.StatusOK, value)
}

// Provenance records controller-attested authorship of one exact artifact
// revision. Only the controller's exact owner-workspace key may call it.
func (h *RuntimeHandler) Provenance(c echo.Context) error {
	ws, err := runtimeWorkspace(c)
	if err != nil {
		return handleError(c, err)
	}
	artifact, err := uuid.Parse(c.Param("provenance_artifact_id"))
	if err != nil || artifact == uuid.Nil {
		return handleError(c, apierror.BadRequest("invalid artifact_id"))
	}
	var input domain.RuntimeProvenanceReport
	if err = runtimeBody(c, &input, "controller_ref", "artifact_revision", "complete", "authors"); err != nil {
		return handleError(c, err)
	}
	input.ArtifactID = artifact
	if err = h.repo.Attest(c.Request().Context(), ws, input.ControllerRef, runtimeActor(c), input); err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]bool{"recorded": true, "launch_authorized": false})
}
