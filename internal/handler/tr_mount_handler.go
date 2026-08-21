package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// TrMountHandler triggers materializing a project's Team Relay share into its
// Docs tree — R3's SyncMount. This is an explicit action, not something a Docs
// tree read ever does on its own: walking a whole share is real cost (one
// download per file the first time it's seen), and doing that implicitly on
// every GET of the tree is the storm §3.6 of the spec doc warns about.
type TrMountHandler struct {
	mountService service.TeamRelayMountService
}

func NewTrMountHandler(mountService service.TeamRelayMountService) *TrMountHandler {
	return &TrMountHandler{mountService: mountService}
}

// trMountStatusHTTP maps a MountStatus to the HTTP status code its caller
// sees. Every one of these is a distinct, named outcome in the JSON body too —
// AC-4 is specifically that a protoken key or an unreachable relay must not
// read like "this share has no documents", so the mapping never collapses two
// different statuses onto the same response shape.
var trMountStatusHTTP = map[service.MountStatus]int{
	service.MountStatusOK:            http.StatusOK,
	service.MountStatusNotConfigured: http.StatusOK,
	service.MountStatusKeyRejected:   http.StatusUnauthorized,
	service.MountStatusForeignShare:  http.StatusForbidden,
	service.MountStatusUnreachable:   http.StatusBadGateway,
	service.MountStatusShareNotFound: http.StatusNotFound,
	service.MountStatusError:         http.StatusInternalServerError,
}

// Sync handles POST /projects/:proj_id/tr/mount.
func (h *TrMountHandler) Sync(c echo.Context) error {
	projectID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid proj_id"))
	}

	result, err := h.mountService.SyncMount(c.Request().Context(), projectID)
	if err != nil {
		return handleError(c, err)
	}

	code, ok := trMountStatusHTTP[result.Status]
	if !ok {
		code = http.StatusInternalServerError
	}

	body := map[string]any{
		"status":  result.Status,
		"mounted": result.Mounted,
		"skipped": result.Skipped,
	}
	if result.Err != nil {
		// The underlying error, not just the classified status — useful for an
		// operator diagnosing WHY a share is unreachable, without exposing
		// anything the status itself doesn't already imply.
		body["detail"] = result.Err.Error()
	}
	return c.JSON(code, body)
}
