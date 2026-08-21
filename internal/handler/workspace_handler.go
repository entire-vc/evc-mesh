package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/internal/storage"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

const (
	maxIconSize = 500 * 1024 // 500 KB
	pngMagic    = "\x89PNG"

	// Short enough that a re-uploaded icon appears without a hard refresh even
	// for clients that ignore the ETag, long enough to keep the icon out of the
	// request path on ordinary navigation.
	iconCacheControl = "public, max-age=300"
)

// IconStorage is the slice of object storage the workspace handler needs.
// Declared here, at the point of use, so the handler is testable with a fake
// and does not drag the artifact-service contract along.
type IconStorage interface {
	Upload(ctx context.Context, key string, reader io.Reader, size int64, contentType string) error
	// GetObject opens the object for reading and returns its metadata.
	// The caller closes the reader.
	GetObject(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error)
}

// WorkspaceHandler handles HTTP requests for workspace management.
type WorkspaceHandler struct {
	workspaceService service.WorkspaceService
	storage          IconStorage // nil when S3 is not configured
}

// NewWorkspaceHandler creates a new WorkspaceHandler with the given service.
func NewWorkspaceHandler(ws service.WorkspaceService) *WorkspaceHandler {
	return &WorkspaceHandler{workspaceService: ws}
}

// WithStorage attaches a storage client for icon uploads.
func (h *WorkspaceHandler) WithStorage(s IconStorage) {
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

	workspaces, err := h.workspaceService.ListForUser(c.Request().Context(), userID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, toResponseList(workspaces))
}

// Create handles POST /workspaces
//
// Agent-key callers are refused outright rather than routed through the same
// path a user takes. There is no scenario where an agent needs its own
// workspace, and the naive alternative — resolve owner_id from whatever
// identity is on the request — has no good answer for an agent: an agent has
// no user_id to fall back to, so that path used to silently create a
// workspace with owner_id all-zero and zero rows in workspace_members. Every
// subsequent call (read, write, delete) against that workspace 403'd,
// including for its own creator: a write-only orphan reachable by nothing
// (task #85fd1ef2; live instance repaired by hand, see that task's comments).
func (h *WorkspaceHandler) Create(c echo.Context) error {
	if mw.IsAgent(c) {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("agents cannot create workspaces"))
	}

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
		// Full detail (endpoint, bucket, underlying S3 error) goes to the log;
		// the operator gets a message that names the actual fault.
		c.Logger().Errorf("workspace icon upload failed for %s (key %s): %v", wsID, storageKey, err)
		return c.JSON(http.StatusInternalServerError, apierror.InternalError(storageFailureMessage(err, "Icon upload failed")))
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
//
// It streams the PNG bytes straight from object storage rather than redirecting
// to a presigned URL. A presigned URL is generated from S3_ENDPOINT, which on
// every bundled deployment is the compose-internal host `minio:9000` — a name
// that does not resolve in the visitor's browser and a port that is not
// published. The redirect therefore pointed at an unreachable address unless
// the operator also set S3_PUBLIC_URL *and* published MinIO or proxied it, none
// of which the install instructions asked for. Serving the bytes ourselves
// keeps the icon working behind any reverse proxy, tunnel or path prefix with
// no extra configuration, and never leaks the internal storage topology.
//
// The cost is bounded: the icon is capped at 500 KB by UploadIcon, it is
// requested about once per page load, and the ETag below turns repeat requests
// into 304s.
func (h *WorkspaceHandler) GetIcon(c echo.Context) error {
	wsIDStr := c.Param("ws_id")
	wsID, err := uuid.Parse(wsIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	workspace, err := h.workspaceService.GetByID(c.Request().Context(), wsID)
	if err != nil {
		// A workspace that does not exist must answer exactly as a workspace
		// that simply has no icon — see iconNotFound.
		var apiErr *apierror.Error
		if errors.As(err, &apiErr) && apiErr.Code == http.StatusNotFound {
			return iconNotFound(c)
		}
		return handleError(c, err)
	}
	if workspace == nil || workspace.IconStorageKey == nil {
		return iconNotFound(c)
	}
	if h.storage == nil {
		return c.JSON(http.StatusServiceUnavailable, apierror.BadRequest("icon storage is not configured"))
	}

	body, info, err := h.storage.GetObject(c.Request().Context(), *workspace.IconStorageKey)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			// The row points at a key the bucket no longer holds (bucket
			// recreated, object pruned). Not a server fault — there is no icon.
			return iconNotFound(c)
		}
		c.Logger().Errorf("workspace icon fetch failed for %s (key %s): %v", wsID, *workspace.IconStorageKey, err)
		return c.JSON(http.StatusInternalServerError, apierror.InternalError(storageFailureMessage(err, "Could not read the workspace icon")))
	}
	defer body.Close()

	// ETag comes from storage and changes whenever the object does, so a
	// re-uploaded icon invalidates browser caches immediately.
	etag := info.ETag
	if etag != "" {
		if !strings.HasPrefix(etag, `"`) {
			etag = `"` + etag + `"`
		}
		c.Response().Header().Set("ETag", etag)
		if match := c.Request().Header.Get("If-None-Match"); match == etag {
			return c.NoContent(http.StatusNotModified)
		}
	}
	c.Response().Header().Set("Cache-Control", iconCacheControl)

	contentType := info.ContentType
	if contentType == "" {
		contentType = "image/png"
	}

	return c.Stream(http.StatusOK, contentType, body)
}

// iconNotFound is the single 404 every "no icon here" outcome answers with:
// unknown workspace, existing workspace with no icon, and a stored key whose
// object has gone missing.
//
// They must be byte-identical. This route is readable without authentication
// (a browser cannot put a bearer token on an <img>), so a distinguishable
// "Workspace not found" would turn it into an existence oracle: anyone could
// probe ids and learn which workspaces are real. Answering the same thing to
// all three keeps the workspace id the only secret involved.
func iconNotFound(c echo.Context) error {
	return c.JSON(http.StatusNotFound, apierror.NotFound("Workspace icon"))
}

// storageFailureMessage turns a classified storage error into a sentence that
// tells the operator what to fix. Anything unrecognised falls back to the
// caller's generic text — better a vague message than a confidently wrong one.
func storageFailureMessage(err error, fallback string) string {
	switch {
	case errors.Is(err, storage.ErrBucketMissing):
		return "Icon storage bucket does not exist and could not be created — check that the S3 credentials may create buckets, or create the bucket named by S3_BUCKET manually."
	case errors.Is(err, storage.ErrAccessDenied):
		return "Object storage rejected the credentials — check S3_ACCESS_KEY_ID and S3_SECRET_ACCESS_KEY."
	case errors.Is(err, storage.ErrUnreachable):
		return "Object storage is unreachable — check that the storage service is running and that S3_ENDPOINT points at it."
	default:
		return fallback + " — see the API log for the storage error."
	}
}
