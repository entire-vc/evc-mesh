package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"

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

// Desired lets a controller read its own slice of the desired catalog with the
// reporter key it already reports with. The digest is the ETag: a controller
// that is up to date gets 304 without the body.
func (h *RuntimeHandler) Desired(c echo.Context) error {
	ws, err := runtimeWorkspace(c)
	if err != nil {
		return handleError(c, err)
	}
	value, err := h.repo.Desired(c.Request().Context(), ws, c.Param("controller_ref"), runtimeActor(c))
	if err != nil {
		return handleError(c, err)
	}
	// The tag covers the whole body (revision, enabled and the controller's
	// slice), not just the catalog digest: a save that only flips enabled
	// keeps the digest but must not answer 304.
	body, err := json.Marshal(value)
	if err != nil {
		return handleError(c, err)
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	c.Response().Header().Set("ETag", etag)
	c.Response().Header().Set("Cache-Control", "private, no-cache")
	for _, candidate := range strings.Split(c.Request().Header.Get("If-None-Match"), ",") {
		candidate = strings.TrimPrefix(strings.TrimSpace(candidate), "W/")
		if candidate == etag || candidate == "*" {
			return c.NoContent(http.StatusNotModified)
		}
	}
	return c.JSONBlob(http.StatusOK, body)
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

// runtimeReservationScope parses the path and query shared by every
// reservation route: receiving workspace, explicit owner and binding ref.
func runtimeReservationScope(c echo.Context, withID bool) (ws, owner, id uuid.UUID, err error) {
	if ws, err = runtimeWorkspace(c); err != nil {
		return
	}
	if owner, err = runtimeOwner(c); err != nil {
		return
	}
	if withID {
		id, err = uuid.Parse(c.Param("reservation_id"))
		if err != nil || id == uuid.Nil {
			err = apierror.BadRequest("invalid reservation_id")
		}
	}
	return
}

// AcquireReservation atomically reserves identity cap, task writer, worker and
// every server-derived pool. 201 on creation, 200 on an idempotent replay.
func (h *RuntimeHandler) AcquireReservation(c echo.Context) error {
	ws, owner, _, err := runtimeReservationScope(c, false)
	if err != nil {
		return handleError(c, err)
	}
	var input domain.RuntimeAcquireInput
	if err = runtimeBody(c, &input, "idempotency_key", "profile_id", "task_id", "checkout_generation", "checkout_request_id", "worker_ref",
		"expected_catalog_revision", "expected_catalog_digest", "expected_admission_revision", "expected_profile_revision",
		"expected_pool_set_digest", "ttl_seconds"); err != nil {
		return handleError(c, err)
	}
	value, created, err := h.repo.AcquireReservation(c.Request().Context(), owner, ws, c.Param("binding_id"), runtimeActor(c), input)
	if err != nil {
		return handleError(c, err)
	}
	if created {
		return c.JSON(http.StatusCreated, value)
	}
	return c.JSON(http.StatusOK, value)
}

func (h *RuntimeHandler) ConsumeReservation(c echo.Context) error {
	ws, owner, id, err := runtimeReservationScope(c, true)
	if err != nil {
		return handleError(c, err)
	}
	var input domain.RuntimeConsumeInput
	if err = runtimeBody(c, &input, "fence", "checkout_generation", "run_lease_seconds"); err != nil {
		return handleError(c, err)
	}
	value, err := h.repo.ConsumeReservation(c.Request().Context(), owner, ws, c.Param("binding_id"), id, runtimeActor(c), input)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, value)
}

// RenewReservation is the run-lease heartbeat of the exact original writer.
func (h *RuntimeHandler) RenewReservation(c echo.Context) error {
	ws, owner, id, err := runtimeReservationScope(c, true)
	if err != nil {
		return handleError(c, err)
	}
	var input domain.RuntimeRenewInput
	if err = runtimeBody(c, &input, "fence", "checkout_request_id", "checkout_generation", "run_lease_seconds"); err != nil {
		return handleError(c, err)
	}
	value, err := h.repo.RenewReservation(c.Request().Context(), owner, ws, c.Param("binding_id"), id, runtimeActor(c), input)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, value)
}

func (h *RuntimeHandler) ReleaseReservation(c echo.Context) error {
	ws, owner, id, err := runtimeReservationScope(c, true)
	if err != nil {
		return handleError(c, err)
	}
	var input domain.RuntimeReleaseInput
	if err = runtimeBody(c, &input, "fence", "checkout_request_id", "checkout_generation", "stopped", "proof"); err != nil {
		return handleError(c, err)
	}
	value, err := h.repo.ReleaseReservation(c.Request().Context(), owner, ws, c.Param("binding_id"), id, runtimeActor(c), input)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, value)
}

func (h *RuntimeHandler) GetReservation(c echo.Context) error {
	ws, owner, id, err := runtimeReservationScope(c, true)
	if err != nil {
		return handleError(c, err)
	}
	value, err := h.repo.GetReservation(c.Request().Context(), owner, ws, c.Param("binding_id"), id, runtimeActor(c))
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, value)
}

// Capacity is the read-only execution-state projection; it admits nothing.
func (h *RuntimeHandler) Capacity(c echo.Context) error {
	ws, err := runtimeWorkspace(c)
	if err != nil {
		return handleError(c, err)
	}
	var agent *uuid.UUID
	if raw := c.QueryParam("agent_id"); raw != "" {
		id, parseErr := uuid.Parse(raw)
		if parseErr != nil || id == uuid.Nil {
			return handleError(c, apierror.BadRequest("invalid agent_id"))
		}
		agent = &id
	}
	value, err := h.repo.Capacity(c.Request().Context(), ws, runtimeActor(c), agent)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, value)
}
