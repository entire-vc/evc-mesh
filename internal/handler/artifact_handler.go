package handler

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
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
	maxFileBytes    int64
}

// NewArtifactHandler creates a new ArtifactHandler with the given service.
func NewArtifactHandler(as service.ArtifactService, ts taskIDResolver) *ArtifactHandler {
	return &ArtifactHandler{artifactService: as, taskSvc: ts, maxFileBytes: service.DefaultArtifactMaxBytes}
}

// WithMaxFileBytes sets the per-file artifact limit the request body is capped
// by; it must match the service's limit. A non-positive value keeps the default.
func (h *ArtifactHandler) WithMaxFileBytes(n int64) *ArtifactHandler {
	if n > 0 {
		h.maxFileBytes = n
	}
	return h
}

// uploadBodySlack is room in the request body for everything around the file:
// multipart boundaries and fields, the JSON envelope and metadata.
const uploadBodySlack = 1 << 20

// bodyTooLarge is the 413 for a request body cut off at its cap, before the
// file size itself is known.
func (h *ArtifactHandler) bodyTooLarge(c echo.Context) error {
	return c.JSON(http.StatusRequestEntityTooLarge, service.ArtifactTooLargeError(fmt.Sprintf(
		"request body is larger than the artifact limit of %d bytes per file", h.maxFileBytes), h.maxFileBytes))
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

	// Cap the body at the file limit plus its envelope (base64 for JSON), so an
	// oversized upload is refused without spooling all of it to a temp file.
	ct := c.Request().Header.Get(echo.HeaderContentType)
	isJSON := strings.HasPrefix(ct, echo.MIMEApplicationJSON)
	bodyCap := h.maxFileBytes + uploadBodySlack
	if isJSON {
		bodyCap = base64PaddedLen(h.maxFileBytes) + uploadBodySlack
	}
	req := c.Request()
	if req.ContentLength > bodyCap {
		return h.bodyTooLarge(c)
	}
	req.Body = http.MaxBytesReader(c.Response(), req.Body, bodyCap)

	if isJSON {
		return h.uploadJSON(c, taskID)
	}
	return h.uploadMultipart(c, taskID)
}

// multipartUpload is a streamed multipart upload: the form fields and the
// file part, the latter spooled to a temp file of at most the per-file limit.
type multipartUpload struct {
	fields   map[string]string
	file     *os.File
	size     int64
	filename string
	mimeType string
	tooLarge bool
}

func (u *multipartUpload) close() {
	if u.file != nil {
		_ = u.file.Close()
		_ = os.Remove(u.file.Name())
	}
}

// maxFormFieldBytes bounds one non-file form field (name, metadata, ...).
const maxFormFieldBytes = 1 << 20

// readMultipartUpload streams the form instead of ParseMultipartForm, which
// spools a whole file part to /tmp past its memory threshold before anyone
// can look at its size. The file part is copied through LimitReader(max+1):
// one byte past the limit is enough to refuse it, so no more than the limit
// is ever written to disk.
func readMultipartUpload(r *http.Request, maxFileBytes int64) (*multipartUpload, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, err
	}
	u := &multipartUpload{fields: map[string]string{}}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return u, nil
		}
		if err != nil {
			u.close()
			return nil, err
		}
		field := part.FormName()
		if field == "file" && part.FileName() != "" && u.file == nil {
			if spoolErr := u.spool(part, maxFileBytes); spoolErr != nil {
				_ = part.Close()
				u.close()
				return nil, spoolErr
			}
			_ = part.Close()
			if u.tooLarge {
				return u, nil
			}
			continue
		}
		value, err := io.ReadAll(io.LimitReader(part, maxFormFieldBytes))
		_ = part.Close()
		if err != nil {
			u.close()
			return nil, err
		}
		if _, seen := u.fields[field]; !seen {
			u.fields[field] = string(value)
		}
	}
}

func (u *multipartUpload) spool(part *multipart.Part, maxFileBytes int64) error {
	f, err := os.CreateTemp("", "artifact-upload-*")
	if err != nil {
		return err
	}
	u.file = f
	u.filename = part.FileName()
	u.mimeType = part.Header.Get("Content-Type")
	n, err := io.Copy(f, io.LimitReader(part, maxFileBytes+1))
	if err != nil {
		return err
	}
	if n > maxFileBytes {
		u.tooLarge = true
		return nil
	}
	u.size = n
	_, err = f.Seek(0, io.SeekStart)
	return err
}

func (h *ArtifactHandler) uploadMultipart(c echo.Context, taskID uuid.UUID) error {
	form, err := readMultipartUpload(c.Request(), h.maxFileBytes)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return h.bodyTooLarge(c)
		}
		// A malformed or non-multipart body: report the missing field, as before.
		form = &multipartUpload{fields: map[string]string{}}
	}
	defer form.close()
	if form.tooLarge {
		return c.JSON(http.StatusRequestEntityTooLarge, service.ArtifactTooLargeError(fmt.Sprintf(
			"artifact is larger than the limit of %d bytes per file", h.maxFileBytes), h.maxFileBytes))
	}

	name := form.fields["name"]
	artifactType := form.fields["artifact_type"]
	metadataStr := form.fields["metadata"]

	if name == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"name": "name is required",
		}))
	}

	if form.file == nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("file is required"))
	}

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
		MimeType:       inferMimeType(form.mimeType, form.filename),
		UploadedBy:     uploadedBy,
		UploadedByType: uploadedByType,
		Reader:         form.file,
		Size:           form.size,
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
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return h.bodyTooLarge(c)
		}
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
	// An extension actor has neither agent_id nor user_id in the echo context.
	if p := mw.GetExtensionPrincipal(c); p != nil {
		return p.ActorID, domain.UploaderTypeExtension
	}
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
// the caller as the generic "invalid value for field".
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
