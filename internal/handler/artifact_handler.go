package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// ArtifactHandler handles HTTP requests for artifact management.
type ArtifactHandler struct {
	artifactService service.ArtifactService
}

// NewArtifactHandler creates a new ArtifactHandler with the given service.
func NewArtifactHandler(as service.ArtifactService) *ArtifactHandler {
	return &ArtifactHandler{artifactService: as}
}

// List handles GET /tasks/:task_id/artifacts
func (h *ArtifactHandler) List(c echo.Context) error {
	taskIDStr := c.Param("task_id")
	taskID, err := uuid.Parse(taskIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
	}

	var pg pagination.Params
	if err := c.Bind(&pg); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid pagination parameters"))
	}
	pg.Normalize()

	page, err := h.artifactService.ListByTask(c.Request().Context(), taskID, pg)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, page)
}

// Upload handles POST /tasks/:task_id/artifacts (multipart form)
func (h *ArtifactHandler) Upload(c echo.Context) error {
	taskIDStr := c.Param("task_id")
	taskID, err := uuid.Parse(taskIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
	}

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
	defer file.Close()

	// Determine uploader from context.
	var uploadedBy uuid.UUID
	var uploadedByType domain.UploaderType

	if agentIDVal := c.Get("agent_id"); agentIDVal != nil {
		if aid, ok := agentIDVal.(uuid.UUID); ok {
			uploadedBy = aid
			uploadedByType = domain.UploaderTypeAgent
		}
	} else if userIDVal := c.Get("user_id"); userIDVal != nil {
		if uid, ok := userIDVal.(uuid.UUID); ok {
			uploadedBy = uid
			uploadedByType = domain.UploaderTypeUser
		}
	}

	// Validate metadata JSON if provided.
	if metadataStr != "" {
		if !json.Valid([]byte(metadataStr)) {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("metadata must be valid JSON"))
		}
	}

	input := service.UploadArtifactInput{
		TaskID:         taskID,
		Name:           name,
		ArtifactType:   domain.ArtifactType(artifactType),
		MimeType:       fileHeader.Header.Get("Content-Type"),
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

// GetByID handles GET /artifacts/:artifact_id
func (h *ArtifactHandler) GetByID(c echo.Context) error {
	artifactIDStr := c.Param("artifact_id")
	artifactID, err := uuid.Parse(artifactIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid artifact_id"))
	}

	artifact, err := h.artifactService.GetByID(c.Request().Context(), artifactID)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, artifact)
}

// Download handles GET /artifacts/:artifact_id/download
func (h *ArtifactHandler) Download(c echo.Context) error {
	artifactIDStr := c.Param("artifact_id")
	artifactID, err := uuid.Parse(artifactIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid artifact_id"))
	}

	url, err := h.artifactService.GetDownloadURL(c.Request().Context(), artifactID)
	if err != nil {
		return handleError(c, err)
	}

	return c.Redirect(http.StatusTemporaryRedirect, url)
}

// Delete handles DELETE /artifacts/:artifact_id
func (h *ArtifactHandler) Delete(c echo.Context) error {
	artifactIDStr := c.Param("artifact_id")
	artifactID, err := uuid.Parse(artifactIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid artifact_id"))
	}

	if err := h.artifactService.Delete(c.Request().Context(), artifactID); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}
