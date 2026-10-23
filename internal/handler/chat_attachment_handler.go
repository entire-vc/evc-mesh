package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/storage"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

const (
	chatPresignTTL  = 15 * time.Minute
	chatDownloadTTL = 5 * time.Minute
)

// ChatAttachmentStore is the object storage the chat attachments use.
type ChatAttachmentStore interface {
	PresignPutURL(ctx context.Context, key string, expiry time.Duration, headers http.Header) (string, error)
	GetPresignedURL(ctx context.Context, key string, expiry time.Duration, contentType, filename string) (string, error)
	StatObject(ctx context.Context, key string) (int64, error)
}

type ChatAttachmentHandler struct {
	repo  *postgres.ChatAttachmentRepo
	store ChatAttachmentStore
}

func NewChatAttachmentHandler(repo *postgres.ChatAttachmentRepo, store ChatAttachmentStore) *ChatAttachmentHandler {
	return &ChatAttachmentHandler{repo: repo, store: store}
}

func chatAttachmentKey(ws, id uuid.UUID) string { return "chat/" + ws.String() + "/" + id.String() }

type chatPresignRequest struct {
	ThreadID uuid.UUID `json:"thread_id"`
	FileName string    `json:"file_name"`
	Mime     string    `json:"mime"`
	Size     int64     `json:"size"`
	Width    *int      `json:"width"`
	Height   *int      `json:"height"`
}

// Presign is POST /projects/:proj_id/chat/attachments. The size is checked
// against the workspace limit before any byte is uploaded.
func (h *ChatAttachmentHandler) Presign(c echo.Context) error {
	if h.store == nil {
		return handleError(c, apierror.ServiceUnavailable("object storage is not configured"))
	}
	project, err := uuid.Parse(c.Param("proj_id"))
	if err != nil || project == uuid.Nil {
		return handleError(c, apierror.BadRequest("invalid proj_id"))
	}
	var req chatPresignRequest
	if err = c.Bind(&req); err != nil {
		return handleError(c, apierror.BadRequest("invalid body"))
	}
	req.FileName, req.Mime = strings.TrimSpace(req.FileName), strings.TrimSpace(req.Mime)
	fields := map[string]string{}
	if req.ThreadID == uuid.Nil {
		fields["thread_id"] = "required"
	}
	if req.FileName == "" || len(req.FileName) > 255 {
		fields["file_name"] = "1..255 characters"
	}
	if req.Mime == "" || len(req.Mime) > 127 || strings.ContainsAny(req.Mime, "\r\n") {
		fields["mime"] = "required"
	}
	if req.Size < 1 {
		fields["size"] = "must be positive"
	}
	if (req.Width != nil && *req.Width < 1) || (req.Height != nil && *req.Height < 1) ||
		((req.Width != nil || req.Height != nil) && !strings.HasPrefix(strings.ToLower(req.Mime), "image/")) {
		fields["width"] = "dimensions are for images and must be positive"
	}
	if len(fields) > 0 {
		// The contract answers 422 for a bad presign body.
		e := apierror.Unprocessable("validation failed", "validation_failed")
		e.ErrorCode, e.Validation = apierror.CodeValidationFailed, fields
		return handleError(c, e)
	}
	att, err := h.repo.Create(c.Request().Context(), userRuntimeActor(c), project, req.ThreadID,
		req.FileName, req.Mime, req.Size, req.Width, req.Height, chatAttachmentKey)
	if err != nil {
		return handleError(c, err)
	}
	headers := http.Header{}
	headers.Set("Content-Type", att.Mime)
	headers.Set("Content-Length", strconv.FormatInt(att.Size, 10))
	url, err := h.store.PresignPutURL(c.Request().Context(), att.S3Key, chatPresignTTL, headers)
	if err != nil {
		return handleError(c, apierror.ServiceUnavailable("object storage is unavailable"))
	}
	return c.JSON(http.StatusCreated, map[string]any{
		"attachment_id":  att.ID,
		"upload_url":     url,
		"upload_method":  "PUT",
		"upload_headers": map[string]string{"Content-Type": att.Mime, "Content-Length": strconv.FormatInt(att.Size, 10)},
		"expires_at":     time.Now().Add(chatPresignTTL).UTC(),
	})
}

// Download is GET /projects/:proj_id/chat/attachments/:attachment_id/download:
// a 302 to a presigned URL valid five minutes. Only the thread's owner reaches it.
func (h *ChatAttachmentHandler) Download(c echo.Context) error {
	if h.store == nil {
		return handleError(c, apierror.ServiceUnavailable("object storage is not configured"))
	}
	project, perr := uuid.Parse(c.Param("proj_id"))
	id, aerr := uuid.Parse(c.Param("attachment_id"))
	if perr != nil || aerr != nil || project == uuid.Nil || id == uuid.Nil {
		return handleError(c, apierror.NotFoundWithDetails("attachment", "attachment_not_found"))
	}
	ctx := c.Request().Context()
	att, err := h.repo.GetOwned(ctx, userRuntimeActor(c), project, id)
	if err != nil {
		return handleError(c, err)
	}
	// The client PUT straight to S3; confirm the object is there on every
	// download, not only the first, so a removed object is a 404, not a dead link.
	size, serr := h.store.StatObject(ctx, att.S3Key)
	if errors.Is(serr, storage.ErrNotFound) || (serr == nil && size != att.Size) {
		return handleError(c, apierror.NotFoundWithDetails("attachment", "attachment_not_found"))
	}
	if serr != nil {
		return handleError(c, apierror.ServiceUnavailable("object storage is unavailable"))
	}
	if att.UploadedAt == nil {
		if err = h.repo.MarkUploaded(ctx, att.ID); err != nil {
			return handleError(c, err)
		}
	}
	url, err := h.store.GetPresignedURL(ctx, att.S3Key, chatDownloadTTL, att.Mime, att.FileName)
	if err != nil {
		return handleError(c, apierror.ServiceUnavailable("object storage is unavailable"))
	}
	return c.Redirect(http.StatusFound, url)
}
