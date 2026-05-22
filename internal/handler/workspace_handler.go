package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

const (
	maxIconSize = 500 * 1024 // 500 KB
	iconExpiry  = time.Hour
	pngMagic    = "\x89PNG"
)

// WorkspaceHandler handles HTTP requests for workspace management.
type WorkspaceHandler struct {
	workspaceService service.WorkspaceService
	storage          service.StorageClient // nil when S3 is not configured
}

// NewWorkspaceHandler creates a new WorkspaceHandler with the given service.
func NewWorkspaceHandler(ws service.WorkspaceService) *WorkspaceHandler {
	return &WorkspaceHandler{workspaceService: ws}
}

// WithStorage attaches a storage client for icon uploads.
func (h *WorkspaceHandler) WithStorage(s service.StorageClient) {
	h.storage = s
}

// workspaceResponse is the JSON shape returned by all workspace endpoints.
// icon_url is the redirect endpoint path when an icon is stored, otherwise null.
type workspaceResponse struct {
	ID                uuid.UUID       `json:"id"`
	Name              string          `json:"name"`
	Slug              string          `json:"slug"`
	OwnerID           uuid.UUID       `json:"owner_id"`
	Settings          json.RawMessage `json:"settings"`
	BillingPlanID     string          `json:"billing_plan_id"`
	BillingCustomerID string          `json:"billing_customer_id"`
	IconURL           *string         `json:"icon_url"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

func toResponse(ws *domain.Workspace) *workspaceResponse {
	r := &workspaceResponse{
		ID:                ws.ID,
		Name:              ws.Name,
		Slug:              ws.Slug,
		OwnerID:           ws.OwnerID,
		Settings:          ws.Settings,
		BillingPlanID:     ws.BillingPlanID,
		BillingCustomerID: ws.BillingCustomerID,
		CreatedAt:         ws.CreatedAt,
		UpdatedAt:         ws.UpdatedAt,
	}
	if ws.IconStorageKey != nil {
		u := fmt.Sprintf("/api/v1/workspaces/%s/icon", ws.ID)
		r.IconURL = &u
	}
	return r
}

func toResponseList(wss []domain.Workspace) []*workspaceResponse {
	out := make([]*workspaceResponse, len(wss))
	for i := range wss {
		out[i] = toResponse(&wss[i])
	}
	return out
}

// createWorkspaceRequest represents the JSON body for creating a workspace.
type createWorkspaceRequest struct {
	Name     string          `json:"name"`
	Slug     string          `json:"slug"`
	Settings json.RawMessage `json:"settings"`
}

// updateWorkspaceRequest represents the JSON body for partially updating a workspace.
type updateWorkspaceRequest struct {
	Name     *string          `json:"name"`
	Slug     *string          `json:"slug"`
	Settings *json.RawMessage `json:"settings"`
}

// List handles GET /workspaces
func (h *WorkspaceHandler) List(c echo.Context) error {
	userIDVal := c.Get("user_id")
	if userIDVal == nil {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("user_id not found in context"))
	}

	userID, ok := userIDVal.(uuid.UUID)
	if !ok {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid user_id in context"))
	}

	workspaces, err := h.workspaceService.ListByOwner(c.Request().Context(), userID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, toResponseList(workspaces))
}

// Create handles POST /workspaces
func (h *WorkspaceHandler) Create(c echo.Context) error {
	var req createWorkspaceRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"name": "name is required",
		}))
	}

	userIDVal := c.Get("user_id")
	var ownerID uuid.UUID
	if userIDVal != nil {
		if uid, ok := userIDVal.(uuid.UUID); ok {
			ownerID = uid
		}
	}

	workspace := &domain.Workspace{
		ID:       uuid.New(),
		Name:     req.Name,
		Slug:     req.Slug,
		OwnerID:  ownerID,
		Settings: req.Settings,
	}

	if err := h.workspaceService.Create(c.Request().Context(), workspace); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, toResponse(workspace))
}

// GetByID handles GET /workspaces/:ws_id
func (h *WorkspaceHandler) GetByID(c echo.Context) error {
	wsIDStr := c.Param("ws_id")
	wsID, err := uuid.Parse(wsIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	workspace, err := h.workspaceService.GetByID(c.Request().Context(), wsID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, toResponse(workspace))
}

// Update handles PATCH /workspaces/:ws_id
func (h *WorkspaceHandler) Update(c echo.Context) error {
	wsIDStr := c.Param("ws_id")
	wsID, err := uuid.Parse(wsIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	var req updateWorkspaceRequest
	if err = c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	// Fetch existing workspace first.
	workspace, err := h.workspaceService.GetByID(c.Request().Context(), wsID)
	if err != nil {
		return handleError(c, err)
	}

	// Apply partial updates.
	if req.Name != nil {
		workspace.Name = *req.Name
	}
	if req.Slug != nil {
		workspace.Slug = *req.Slug
	}
	if req.Settings != nil {
		workspace.Settings = *req.Settings
	}

	if err := h.workspaceService.Update(c.Request().Context(), workspace); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, toResponse(workspace))
}

// Delete handles DELETE /workspaces/:ws_id
func (h *WorkspaceHandler) Delete(c echo.Context) error {
	wsIDStr := c.Param("ws_id")
	wsID, err := uuid.Parse(wsIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	if err := h.workspaceService.Delete(c.Request().Context(), wsID); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// UploadIcon handles PUT /workspaces/:ws_id/icon
// Accepts multipart/form-data with a "file" field (PNG only, ≤500 KB).
func (h *WorkspaceHandler) UploadIcon(c echo.Context) error {
	if h.storage == nil {
		return c.JSON(http.StatusServiceUnavailable, apierror.BadRequest("icon storage is not configured"))
	}

	wsIDStr := c.Param("ws_id")
	wsID, err := uuid.Parse(wsIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	file, err := c.FormFile("file")
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("file field is required"))
	}

	if file.Size > maxIconSize {
		return c.JSON(http.StatusRequestEntityTooLarge, apierror.BadRequest("icon must be ≤ 500 KB"))
	}

	src, err := file.Open()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierror.InternalError("failed to open upload"))
	}
	defer src.Close()

	// Verify PNG magic bytes without consuming the stream.
	header := make([]byte, 4)
	_, err = io.ReadFull(src, header)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("could not read file"))
	}
	if string(header) != pngMagic {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("only PNG files are accepted"))
	}
	// Reconstruct full reader: prepend the 4 header bytes we already consumed.
	reader := io.MultiReader(bytes.NewReader(header), src)

	storageKey := fmt.Sprintf("workspaces/%s/icon.png", wsID)
	err = h.storage.Upload(c.Request().Context(), storageKey, reader, file.Size, "image/png")
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierror.InternalError("upload failed"))
	}

	workspace, err := h.workspaceService.GetByID(c.Request().Context(), wsID)
	if err != nil {
		return handleError(c, err)
	}
	workspace.IconStorageKey = &storageKey
	if err := h.workspaceService.Update(c.Request().Context(), workspace); err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, toResponse(workspace))
}

// GetIcon handles GET /workspaces/:ws_id/icon
// Redirects (302) to a presigned S3 URL for the workspace icon image.
func (h *WorkspaceHandler) GetIcon(c echo.Context) error {
	wsIDStr := c.Param("ws_id")
	wsID, err := uuid.Parse(wsIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	workspace, err := h.workspaceService.GetByID(c.Request().Context(), wsID)
	if err != nil {
		return handleError(c, err)
	}
	if workspace.IconStorageKey == nil {
		return c.JSON(http.StatusNotFound, apierror.NotFound("Workspace icon"))
	}
	if h.storage == nil {
		return c.JSON(http.StatusServiceUnavailable, apierror.BadRequest("icon storage is not configured"))
	}

	url, err := h.storage.GetPresignedURL(c.Request().Context(), *workspace.IconStorageKey, iconExpiry)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierror.InternalError("could not generate icon URL"))
	}

	return c.Redirect(http.StatusFound, url)
}
