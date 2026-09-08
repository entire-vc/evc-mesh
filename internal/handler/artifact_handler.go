package handler

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// ArtifactHandler handles HTTP requests for artifact management.
type ArtifactHandler struct {
	artifactService service.ArtifactService
	taskSvc         taskIDResolver
}

// NewArtifactHandler creates a new ArtifactHandler with the given service.
func NewArtifactHandler(as service.ArtifactService, ts taskIDResolver) *ArtifactHandler {
	return &ArtifactHandler{artifactService: as, taskSvc: ts}
}

// List handles GET /tasks/:task_id/artifacts
func (h *ArtifactHandler) List(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskSvc)
	if err != nil {
		return handleError(c, err)
	}

	var pg pagination.Params
	if err = c.Bind(&pg); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid pagination parameters"))
	}
	pg.Normalize()

	page, err := h.artifactService.ListByTask(c.Request().Context(), taskID, pg)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, page)
}

// Upload handles POST /tasks/:task_id/artifacts. Dispatches on Content-Type:
// multipart/form-data (the documented, MCP-client format) or application/json
// (a direct-REST convenience for callers that already have the file content
// in memory — e.g. a screenshot captured and base64-encoded in one step, with
// no multipart writer at hand).
func (h *ArtifactHandler) Upload(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskSvc)
	if err != nil {
		return handleError(c, err)
	}

	ct := c.Request().Header.Get(echo.HeaderContentType)
	if strings.HasPrefix(ct, echo.MIMEApplicationJSON) {
		return h.uploadJSON(c, taskID)
	}
	return h.uploadMultipart(c, taskID)
}

func (h *ArtifactHandler) uploadMultipart(c echo.Context, taskID uuid.UUID) error {
	// Read multipart form fields.
	name := c.FormValue("name")
	artifactType := c.FormValue("artifact_type")
	metadataStr := c.FormValue("metadata")

	if name == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"name": "name is required",
		}))
	}

	// Get uploaded file.
	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("file is required"))
	}

	file, err := fileHeader.Open()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierror.InternalError("failed to open uploaded file"))
	}
	defer func() { _ = file.Close() }()

	// Validate metadata JSON if provided.
	if metadataStr != "" {
		if !json.Valid([]byte(metadataStr)) {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("metadata must be valid JSON"))
		}
	}

	uploadedBy, uploadedByType := uploaderFromContext(c)

	input := service.UploadArtifactInput{
		TaskID:         taskID,
		Name:           name,
		ArtifactType:   normalizeArtifactType(artifactType),
		MimeType:       inferMimeType(fileHeader.Header.Get("Content-Type"), fileHeader.Filename),
		UploadedBy:     uploadedBy,
		UploadedByType: uploadedByType,
		Reader:         file,
		Size:           fileHeader.Size,
	}

	artifact, err := h.artifactService.Upload(c.Request().Context(), input)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, artifact)
}

// uploadArtifactJSONBody is the application/json request shape for
// POST /tasks/:task_id/artifacts. content_type is accepted as an alias for
// mime_type — both names have been seen in the wild from direct-REST callers.
type uploadArtifactJSONBody struct {
	Name         string          `json:"name"`
	ArtifactType string          `json:"artifact_type"`
	MimeType     string          `json:"mime_type"`
	ContentType  string          `json:"content_type"`
	Content      string          `json:"content"`
	Encoding     string          `json:"encoding"` // "base64" (default) or "text"
	Metadata     json.RawMessage `json:"metadata"`
}

func (h *ArtifactHandler) uploadJSON(c echo.Context, taskID uuid.UUID) error {
	var body uploadArtifactJSONBody
	if err := json.NewDecoder(c.Request().Body).Decode(&body); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid JSON body"))
	}

	if body.Name == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"name": "name is required",
		}))
	}
	if body.Content == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"content": "content is required",
		}))
	}

	var data []byte
	switch strings.ToLower(strings.TrimSpace(body.Encoding)) {
	case "text":
		data = []byte(body.Content)
	case "", "base64":
		decoded, err := base64.StdEncoding.DecodeString(body.Content)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest(
				`content is not valid base64 (pass "encoding": "text" for raw text content)`))
		}
		data = decoded
	default:
		return c.JSON(http.StatusBadRequest, apierror.BadRequest(
			`invalid encoding `+strings.TrimSpace(body.Encoding)+`: must be "base64" or "text"`))
	}

	if len(body.Metadata) > 0 && !json.Valid(body.Metadata) {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("metadata must be valid JSON"))
	}

	mimeType := body.MimeType
	if mimeType == "" {
		mimeType = body.ContentType
	}

	uploadedBy, uploadedByType := uploaderFromContext(c)

	input := service.UploadArtifactInput{
		TaskID:         taskID,
		Name:           body.Name,
		ArtifactType:   normalizeArtifactType(body.ArtifactType),
		MimeType:       inferMimeType(mimeType, body.Name),
		UploadedBy:     uploadedBy,
		UploadedByType: uploadedByType,
		Reader:         bytes.NewReader(data),
		Size:           int64(len(data)),
	}

	artifact, err := h.artifactService.Upload(c.Request().Context(), input)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, artifact)
}

// uploaderFromContext resolves the agent/user identity attached to the
// request by the auth middleware.
func uploaderFromContext(c echo.Context) (uuid.UUID, domain.UploaderType) {
	if agentIDVal := c.Get("agent_id"); agentIDVal != nil {
		if aid, ok := agentIDVal.(uuid.UUID); ok {
			return aid, domain.UploaderTypeAgent
		}
	} else if userIDVal := c.Get("user_id"); userIDVal != nil {
		if uid, ok := userIDVal.(uuid.UUID); ok {
			return uid, domain.UploaderTypeUser
		}
	}
	return uuid.UUID{}, ""
}

// normalizeArtifactType defaults an empty artifact_type to "file" — the same
// default the artifact_type column carries in Postgres (migration
// 20260224012_create_artifacts.sql). The Go layer must apply the same
// default explicitly: the INSERT always sets the column, so the DB default
// never fires, and an empty string is not a valid value of the artifact_type
// enum — it was reaching Postgres as "" and failing with 22P02, surfaced to
// the caller as the generic "invalid value for field" (task #82ce594a).
func normalizeArtifactType(raw string) domain.ArtifactType {
	if raw == "" {
		return domain.ArtifactTypeFile
	}
	return domain.ArtifactType(raw)
}

// GetByID handles GET /artifacts/:artifact_id
func (h *ArtifactHandler) GetByID(c echo.Context) error {
	artifactIDStr := c.Param("artifact_id")
	artifactID, err := uuid.Parse(artifactIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid artifact_id"))
	}

	// Defense-in-depth: restrict to the caller's workspace even though wsAccess
	// middleware already enforces this at the route level.
	wsID, err := mw.GetWorkspaceID(c)
	if err != nil {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
	}

	artifact, err := h.artifactService.GetByIDInWorkspace(c.Request().Context(), artifactID, wsID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, artifact)
}

// Download handles GET /artifacts/:artifact_id/download and the task-scoped alias.
func (h *ArtifactHandler) Download(c echo.Context) error {
	artifactIDStr := c.Param("artifact_id")
	artifactID, err := uuid.Parse(artifactIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid artifact_id"))
	}

	// Defense-in-depth: when workspace is resolvable from context (i.e. wsAccess
	// middleware ran on this route), verify the artifact belongs to that workspace.
	if wsID, wsErr := mw.GetWorkspaceID(c); wsErr == nil {
		if _, werr := h.artifactService.GetByIDInWorkspace(c.Request().Context(), artifactID, wsID); werr != nil {
			return handleError(c, werr)
		}
	}

	inline := c.QueryParam("disposition") == "inline"
	url, err := h.artifactService.GetDownloadURL(c.Request().Context(), artifactID, inline)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]string{"url": url})
}

// Delete handles DELETE /artifacts/:artifact_id
func (h *ArtifactHandler) Delete(c echo.Context) error {
	artifactIDStr := c.Param("artifact_id")
	artifactID, err := uuid.Parse(artifactIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid artifact_id"))
	}

	// Defense-in-depth: verify the artifact belongs to the caller's workspace
	// before allowing the delete, even though wsAccess already checked this.
	wsID, err := mw.GetWorkspaceID(c)
	if err != nil {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
	}
	if _, err := h.artifactService.GetByIDInWorkspace(c.Request().Context(), artifactID, wsID); err != nil {
		return handleError(c, err)
	}

	if err := h.artifactService.Delete(c.Request().Context(), artifactID); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// inferMimeType returns headerMime unless it is empty or generic octet-stream,
// in which case it infers from the filename extension.
func inferMimeType(headerMime, filename string) string {
	if headerMime != "" && headerMime != "application/octet-stream" {
		return headerMime
	}
	if ext := filepath.Ext(filename); ext != "" {
		if inferred := mime.TypeByExtension(ext); inferred != "" {
			return inferred
		}
	}
	return headerMime
}
