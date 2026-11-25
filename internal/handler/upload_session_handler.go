package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/internal/storage"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Two-step uploads (docs/mcp-reference.md "Uploading files"). An agent on a
// remote MCP transport cannot hand the server a local path, and base64 in a
// tool argument breaks in its context, so the bytes go straight to object
// storage on a presigned PUT and the API only confirms them: same shape as the
// chat attachments (chat_attachment_handler.go), but the confirm hands the
// staged bytes to the ordinary document-attachment / artifact upload, so there
// is still one write path for both.
const (
	// UploadSessionTTL is how long the presigned PUT and the session live.
	UploadSessionTTL = 15 * time.Minute
	// uploadConfirmLease bounds one confirm: longer than the artifact upload's
	// own reservation (15 min), so a lease never lapses under a live confirm.
	uploadConfirmLease = 20 * time.Minute
	// uploadCleanupTimeout bounds the bookkeeping after the upload itself.
	uploadCleanupTimeout = 10 * time.Second
	// sniffBytes is what http.DetectContentType reads.
	sniffBytes = 512
)

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// sniffedTypes are the declared types whose first bytes confirm checks: the
// ones a browser renders inline (service.IsInlineSafeMimeType), so a file
// declared as one of them really is one.
var sniffedTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true, "application/pdf": true,
}

// markdownImageTypes render as an image in a document body; everything else
// is a link.
var markdownImageTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
}

// UploadStore is the object storage two-step uploads use.
type UploadStore interface {
	PresignPutURL(ctx context.Context, key string, expiry time.Duration, headers http.Header) (string, error)
	StatObjectInfo(ctx context.Context, key string) (storage.ObjectInfo, error)
	DownloadIfMatch(ctx context.Context, key, etag string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

// uploadSessions is the session store (postgres.UploadSessionRepo).
type uploadSessions interface {
	Create(ctx context.Context, s *postgres.UploadSession) error
	GetOwned(ctx context.Context, id, createdBy uuid.UUID, createdByType string) (*postgres.UploadSession, error)
	Claim(ctx context.Context, id uuid.UUID, lease time.Duration, candidate uuid.UUID) (uuid.UUID, bool, error)
	Release(ctx context.Context, id uuid.UUID) error
	MarkConfirmed(ctx context.Context, id, resultID uuid.UUID) error
}

// artifactTaskBytes reads what a task's artifacts hold (postgres.ArtifactRepo).
type artifactTaskBytes interface {
	TaskBytesUsed(ctx context.Context, taskID uuid.UUID) (int64, error)
}

// UploadSessionHandler serves the two-step upload routes.
type UploadSessionHandler struct {
	sessions    uploadSessions
	store       UploadStore
	documents   repository.DocumentRepository
	attachments repository.DocumentAttachmentRepository
	attachSvc   service.DocumentAttachmentService
	artifactSvc service.ArtifactService
	taskBytes   artifactTaskBytes
	taskSvc     taskIDResolver
	members     repository.WorkspaceMemberRepository
	maxArtifact int64
	maxTask     int64
}

// UploadSessionDeps wires UploadSessionHandler.
type UploadSessionDeps struct {
	Sessions         *postgres.UploadSessionRepo
	Store            UploadStore
	Documents        repository.DocumentRepository
	Attachments      repository.DocumentAttachmentRepository
	AttachmentSvc    service.DocumentAttachmentService
	ArtifactSvc      service.ArtifactService
	TaskBytes        *postgres.ArtifactRepo
	TaskSvc          taskIDResolver
	Members          repository.WorkspaceMemberRepository
	MaxArtifactBytes int64
	MaxTaskBytes     int64
}

// NewUploadSessionHandler returns the handler; a nil Store answers 503.
func NewUploadSessionHandler(d UploadSessionDeps) *UploadSessionHandler {
	h := &UploadSessionHandler{
		sessions: d.Sessions, documents: d.Documents, attachments: d.Attachments,
		attachSvc: d.AttachmentSvc, artifactSvc: d.ArtifactSvc, taskBytes: d.TaskBytes, taskSvc: d.TaskSvc,
		members: d.Members, maxArtifact: d.MaxArtifactBytes, maxTask: d.MaxTaskBytes,
	}
	if d.Store != nil {
		h.store = d.Store
	}
	if h.maxArtifact <= 0 {
		h.maxArtifact = service.DefaultArtifactMaxBytes
	}
	if h.maxTask <= 0 {
		h.maxTask = service.DefaultArtifactTaskMaxBytes
	}
	return h
}

// UploadStagingKey is the object key of a staged upload.
func UploadStagingKey(ws, id uuid.UUID) string { return "uploads/" + ws.String() + "/" + id.String() }

type createUploadRequest struct {
	Name         string `json:"name"`
	MimeType     string `json:"mime_type"`
	SizeBytes    int64  `json:"size_bytes"`
	SHA256       string `json:"sha256"`
	ArtifactType string `json:"artifact_type"`
}

func (r *createUploadRequest) validate(artifact bool) *apierror.Error {
	r.Name, r.MimeType = strings.TrimSpace(r.Name), strings.TrimSpace(r.MimeType)
	r.SHA256 = strings.ToLower(strings.TrimSpace(r.SHA256))
	fields := map[string]string{}
	if r.Name == "" || len(r.Name) > 255 || strings.ContainsAny(r.Name, "\r\n\x00") {
		fields["name"] = "1..255 characters, no line breaks"
	}
	if _, _, err := mime.ParseMediaType(r.MimeType); err != nil || len(r.MimeType) > 127 {
		fields["mime_type"] = "a media type, for example image/png"
	}
	if r.SizeBytes < 1 {
		fields["size_bytes"] = "must be positive"
	}
	if !sha256Hex.MatchString(r.SHA256) {
		fields["sha256"] = "64 hex characters"
	}
	if artifact {
		switch domain.ArtifactType(r.ArtifactType) {
		case "", domain.ArtifactTypeFile, domain.ArtifactTypeCode, domain.ArtifactTypeLog, domain.ArtifactTypeReport,
			domain.ArtifactTypeLink, domain.ArtifactTypeImage, domain.ArtifactTypeData:
		default:
			fields["artifact_type"] = "one of file, code, log, report, link, image, data"
		}
	}
	if len(fields) == 0 {
		return nil
	}
	e := apierror.Unprocessable("validation failed", "validation_failed")
	e.ErrorCode, e.Validation = apierror.CodeValidationFailed, fields
	return e
}

func fileTooLarge(message string, limit int64) *apierror.Error {
	return &apierror.Error{Code: http.StatusUnprocessableEntity, ErrorCode: "file_too_large", Message: message,
		Context: map[string]any{"limit_bytes": limit}}
}

// CreateDocumentUpload is POST /documents/:doc_id/attachments/uploads.
func (h *UploadSessionHandler) CreateDocumentUpload(c echo.Context) error {
	docID, wsID, apiErr := documentScope(c)
	if apiErr != nil {
		return handleError(c, apiErr)
	}
	var req createUploadRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, apierror.BadRequest("invalid body"))
	}
	if e := req.validate(false); e != nil {
		return handleError(c, e)
	}
	limit := service.MaxDocumentAttachmentBytes
	if req.SizeBytes > limit {
		return handleError(c, fileTooLarge(fmt.Sprintf("file is %d bytes, the limit is %d bytes", req.SizeBytes, limit), limit))
	}
	doc, err := h.documents.GetByIDInWorkspace(c.Request().Context(), docID, wsID)
	if err != nil {
		return handleError(c, err)
	}
	if doc == nil {
		return handleError(c, apierror.NotFound("Document"))
	}
	return h.create(c, wsID, postgres.UploadKindDocumentAttachment, docID, req, limit)
}

// CreateArtifactUpload is POST /tasks/:task_id/artifacts/uploads.
func (h *UploadSessionHandler) CreateArtifactUpload(c echo.Context) error {
	ctx := c.Request().Context()
	taskID, err := resolveTaskID(ctx, c.Param("task_id"), h.taskSvc)
	if err != nil {
		return handleError(c, err)
	}
	wsID, err := mw.GetWorkspaceID(c)
	if err != nil {
		return handleError(c, apierror.Forbidden("workspace access denied"))
	}
	var req createUploadRequest
	if err = c.Bind(&req); err != nil {
		return handleError(c, apierror.BadRequest("invalid body"))
	}
	if e := req.validate(true); e != nil {
		return handleError(c, e)
	}
	if req.SizeBytes > h.maxArtifact {
		return handleError(c, fileTooLarge(fmt.Sprintf(
			"artifact is %d bytes, the limit is %d bytes per file", req.SizeBytes, h.maxArtifact), h.maxArtifact))
	}
	// A pre-check only: the reservation the real upload takes on confirm is
	// what enforces the task limit.
	used, err := h.taskBytes.TaskBytesUsed(ctx, taskID)
	if err != nil {
		return handleError(c, err)
	}
	if used+req.SizeBytes > h.maxTask {
		return handleError(c, fileTooLarge(fmt.Sprintf(
			"task already holds %d bytes of artifacts, this %d-byte file would exceed the limit of %d bytes per task",
			used, req.SizeBytes, h.maxTask), h.maxTask))
	}
	req.ArtifactType = string(normalizeArtifactType(req.ArtifactType))
	return h.create(c, wsID, postgres.UploadKindArtifact, taskID, req, h.maxArtifact)
}

func (h *UploadSessionHandler) create(c echo.Context, wsID uuid.UUID, kind string, target uuid.UUID,
	req createUploadRequest, maxBytes int64,
) error {
	if h.store == nil {
		return handleError(c, apierror.ServiceUnavailable("object storage is not configured"))
	}
	callerID, callerType := callerActor(c)
	if callerID == uuid.Nil || (callerType != domain.ActorTypeAgent && callerType != domain.ActorTypeUser) {
		return handleError(c, apierror.Forbidden("uploads need an agent or user credential"))
	}
	s := &postgres.UploadSession{
		ID: uuid.New(), WorkspaceID: wsID, Kind: kind, TargetID: target,
		Name: req.Name, MimeType: req.MimeType, SizeBytes: req.SizeBytes, SHA256: req.SHA256,
		CreatedBy: callerID, CreatedByType: string(callerType),
		ExpiresAt: time.Now().Add(UploadSessionTTL).UTC(),
	}
	if kind == postgres.UploadKindArtifact {
		s.ArtifactType = &req.ArtifactType
	}
	s.StagingKey = UploadStagingKey(wsID, s.ID)
	ctx := c.Request().Context()
	// Only Content-Length is signed: it pins the size, and every HTTP client
	// sends it on its own. Content-Type is not: an agent does not set headers
	// by hand, and the stored object's type comes from mime_type anyway.
	headers := http.Header{}
	headers.Set("Content-Length", strconv.FormatInt(s.SizeBytes, 10))
	url, err := h.store.PresignPutURL(ctx, s.StagingKey, UploadSessionTTL, headers)
	if err != nil {
		return handleError(c, apierror.ServiceUnavailable("object storage is unavailable"))
	}
	if err = h.sessions.Create(ctx, s); err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusCreated, map[string]any{
		"upload_id":      s.ID,
		"upload_url":     url,
		"upload_method":  http.MethodPut,
		"upload_headers": map[string]string{},
		"expires_at":     s.ExpiresAt,
		"max_bytes":      maxBytes,
	})
}

func uploadNotFound() *apierror.Error {
	return apierror.NotFoundWithDetails("Upload", "upload_not_found")
}

func uploadRefusal(status int, code, message string, details map[string]any) *apierror.Error {
	return &apierror.Error{Code: status, ErrorCode: code, Message: message, Context: details}
}

// Confirm is POST /uploads/:upload_id/confirm.
//
// The route carries no workspace resolver on purpose: an upload is visible
// only to the actor that declared it, so an unknown id, another workspace's
// and another member's all answer the same 404 here.
//
// The result row's id is fixed by the first confirm that takes the lease
// (pending_result_id), and confirmed_at is recorded only after that row is
// written. A confirm interrupted in between leaves the session open with the
// row already there; the next confirm finds the row by that id and records it
// instead of writing the file again.
func (h *UploadSessionHandler) Confirm(c echo.Context) error {
	if h.store == nil {
		return handleError(c, apierror.ServiceUnavailable("object storage is not configured"))
	}
	id, err := uuid.Parse(c.Param("upload_id"))
	if err != nil || id == uuid.Nil {
		return handleError(c, uploadNotFound())
	}
	ctx := c.Request().Context()
	callerID, callerType := callerActor(c)
	if callerID == uuid.Nil {
		return handleError(c, uploadNotFound())
	}
	s, err := h.sessions.GetOwned(ctx, id, callerID, string(callerType))
	if err != nil {
		return handleError(c, err)
	}
	if s == nil {
		return handleError(c, uploadNotFound())
	}
	if apiErr := h.authorizeConfirm(c, s); apiErr != nil {
		return handleError(c, apiErr)
	}
	if s.ConfirmedAt != nil {
		return h.replay(c, s)
	}
	if done, doneErr := h.recordWritten(c, s); done {
		return doneErr
	}
	if !time.Now().Before(s.ExpiresAt) {
		return h.expired(c, s)
	}
	resultID, claimed, err := h.sessions.Claim(ctx, s.ID, uploadConfirmLease, uuid.New())
	if err != nil {
		return handleError(c, err)
	}
	if !claimed {
		// Lost to a concurrent confirm, or it expired in between: re-read.
		if s, err = h.sessions.GetOwned(ctx, id, callerID, string(callerType)); err != nil || s == nil {
			return handleError(c, uploadNotFound())
		}
		if s.ConfirmedAt != nil {
			return h.replay(c, s)
		}
		if !time.Now().Before(s.ExpiresAt) {
			return h.expired(c, s)
		}
		return handleError(c, uploadRefusal(http.StatusConflict, apierror.CodeConflict,
			"another confirm of this upload is in progress; retry when it finishes", nil))
	}
	// A no-op once the session is confirmed.
	defer h.release(ctx, s)
	s.PendingResultID = &resultID
	// A holder whose lease lapsed may have written the row in the meantime.
	if done, doneErr := h.recordWritten(c, s); done {
		return doneErr
	}

	etag, apiErr := h.verifyStaged(ctx, s)
	if apiErr != nil {
		return handleError(c, apiErr)
	}
	rc, err := h.store.DownloadIfMatch(ctx, s.StagingKey, etag)
	if err != nil {
		return handleError(c, apierror.ServiceUnavailable("object storage is unavailable"))
	}
	defer func() { _ = rc.Close() }()

	var body map[string]any
	switch s.Kind {
	case postgres.UploadKindDocumentAttachment:
		uploadedBy, uploadedByType := callerActor(c)
		att, upErr := h.attachSvc.Upload(ctx, service.UploadDocumentAttachmentInput{
			DocumentID: s.TargetID, WorkspaceID: s.WorkspaceID, Name: s.Name, MimeType: s.MimeType,
			Size: s.SizeBytes, Reader: rc, UploadedBy: uploadedBy, UploadedByType: uploadedByType, ID: resultID,
		})
		if upErr != nil {
			return handleError(c, upErr)
		}
		body = documentUploadResult(s, att)
	case postgres.UploadKindArtifact:
		fence, fErr := resultFenceFromRequest(c, s.TargetID)
		if fErr != nil {
			return handleError(c, fErr)
		}
		uploadedBy, uploadedByType := uploaderFromContext(c)
		artifactType := domain.ArtifactTypeFile
		if s.ArtifactType != nil {
			artifactType = normalizeArtifactType(*s.ArtifactType)
		}
		art, upErr := h.artifactSvc.Upload(ctx, service.UploadArtifactInput{
			TaskID: s.TargetID, Name: s.Name, ArtifactType: artifactType, MimeType: s.MimeType,
			UploadedBy: uploadedBy, UploadedByType: uploadedByType, Reader: rc, Size: s.SizeBytes,
			Fence: fence, ChecksumSHA256: s.SHA256, ID: resultID,
		})
		if upErr != nil {
			var ae *apierror.Error
			if errors.As(upErr, &ae) && ae.Code == http.StatusRequestEntityTooLarge {
				limit, _ := ae.Context["limit_bytes"].(int64)
				return handleError(c, fileTooLarge(ae.Message, limit))
			}
			return handleError(c, upErr)
		}
		body = artifactUploadResult(s, art)
	default:
		return handleError(c, apierror.InternalError("unknown upload kind"))
	}
	h.markConfirmed(ctx, s, resultID)
	h.dropStaged(ctx, s)
	return c.JSON(http.StatusCreated, body)
}

// recordWritten finishes a confirm whose result row an earlier attempt wrote
// but did not record: it answers with the row and records it. done is false
// when there is no such row, and the caller goes on to write it.
func (h *UploadSessionHandler) recordWritten(c echo.Context, s *postgres.UploadSession) (done bool, err error) {
	if s.PendingResultID == nil {
		return false, nil
	}
	ctx := c.Request().Context()
	body, found, apiErr := h.resultBody(ctx, s, *s.PendingResultID)
	if apiErr != nil {
		return true, handleError(c, apiErr)
	}
	if !found {
		return false, nil
	}
	h.markConfirmed(ctx, s, *s.PendingResultID)
	h.dropStaged(ctx, s)
	return true, c.JSON(http.StatusOK, body)
}

// markConfirmed records the result on the session. Its failure does not undo
// the confirm: the row is written and pending_result_id, fixed when the lease
// was taken, already links the session to it, so every later confirm answers
// with that row (recordWritten) and tries the record again.
func (h *UploadSessionHandler) markConfirmed(ctx context.Context, s *postgres.UploadSession, resultID uuid.UUID) {
	cctx, cancel := detached(ctx)
	defer cancel()
	if err := h.sessions.MarkConfirmed(cctx, s.ID, resultID); err != nil {
		log.Printf("upload %s: record confirmed result %s: %v", s.ID, resultID, err)
	}
}

// release drops this confirm's lease unless the session was confirmed.
func (h *UploadSessionHandler) release(ctx context.Context, s *postgres.UploadSession) {
	cctx, cancel := detached(ctx)
	defer cancel()
	if err := h.sessions.Release(cctx, s.ID); err != nil {
		log.Printf("upload %s: release confirm lease: %v", s.ID, err)
	}
}

// detached is a context for bookkeeping after the request's own work: it
// outlives a client that hung up, and its clock starts when it is needed, not
// when the request began.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), uploadCleanupTimeout)
}

// authorizeConfirm re-checks, on every confirm and replay, that the declaring
// actor may still write to the upload's target: an agent key bound to its
// workspace or a user whose role there still grants uploads, and in both cases
// access to the target's project now, not when the upload was declared.
func (h *UploadSessionHandler) authorizeConfirm(c echo.Context, s *postgres.UploadSession) *apierror.Error {
	if mw.IsAgent(c) {
		ws, err := mw.GetAgentAuthWorkspaceID(c)
		if err != nil || ws != s.WorkspaceID {
			return uploadNotFound()
		}
	} else {
		userID, err := mw.GetUserID(c)
		if err != nil || h.members == nil {
			return uploadNotFound()
		}
		role, err := h.members.GetRole(c.Request().Context(), s.WorkspaceID, userID)
		if err != nil || role == "" {
			return uploadNotFound()
		}
		if !mw.RoleHasPermission(role, mw.PermUploadArtifact) {
			return apierror.Forbidden("insufficient permissions")
		}
		c.Set(mw.ContextKeyWorkspaceRole, role)
	}
	// The project rule reads the request's workspace; this route names none,
	// and the session's is the one just checked.
	c.Set(mw.ContextKeyWorkspaceID, s.WorkspaceID)
	switch s.Kind {
	case postgres.UploadKindArtifact:
		if !mw.MayAccessTask(c, s.TargetID) {
			return apierror.Forbidden("not a member of this task's project")
		}
	case postgres.UploadKindDocumentAttachment:
		doc, err := h.documents.GetByIDInWorkspace(c.Request().Context(), s.TargetID, s.WorkspaceID)
		if err != nil {
			return apierror.InternalError("could not load the document")
		}
		if doc == nil {
			return apierror.NotFound("Document")
		}
		if !mw.MayAccessProject(c, doc.ProjectID) {
			return apierror.Forbidden("not a member of this document's project")
		}
	default:
		return apierror.InternalError("unknown upload kind")
	}
	return nil
}

// verifyStaged checks the staged object against the declaration and returns
// the ETag both reads are pinned to. A refusal for bytes that cannot become
// the declared file removes the staged object; the session stays open until
// it expires, so the caller may PUT again and confirm again.
func (h *UploadSessionHandler) verifyStaged(ctx context.Context, s *postgres.UploadSession) (string, *apierror.Error) {
	info, err := h.store.StatObjectInfo(ctx, s.StagingKey)
	if errors.Is(err, storage.ErrNotFound) {
		return "", uploadRefusal(http.StatusConflict, "upload_not_received",
			"nothing was uploaded to upload_url yet; PUT the file there, then confirm", nil)
	}
	if err != nil {
		return "", apierror.ServiceUnavailable("object storage is unavailable")
	}
	if info.Size != s.SizeBytes {
		h.dropStaged(ctx, s)
		return "", uploadRefusal(http.StatusUnprocessableEntity, "size_mismatch",
			"the uploaded file is not the declared size",
			map[string]any{"declared": s.SizeBytes, "received": info.Size})
	}
	rc, err := h.store.DownloadIfMatch(ctx, s.StagingKey, info.ETag)
	if err != nil {
		return "", apierror.ServiceUnavailable("object storage is unavailable")
	}
	defer func() { _ = rc.Close() }()
	sum := sha256.New()
	head := &headCapture{limit: sniffBytes}
	n, err := io.Copy(io.MultiWriter(sum, head), io.LimitReader(rc, s.SizeBytes+1))
	if err != nil {
		// The object changed under the pinned ETag, or the read broke.
		return "", uploadRefusal(http.StatusConflict, apierror.CodeConflict,
			"the uploaded file changed while it was being checked; confirm again", nil)
	}
	if n != s.SizeBytes {
		h.dropStaged(ctx, s)
		return "", uploadRefusal(http.StatusUnprocessableEntity, "size_mismatch",
			"the uploaded file is not the declared size",
			map[string]any{"declared": s.SizeBytes, "received": n})
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != s.SHA256 {
		h.dropStaged(ctx, s)
		return "", uploadRefusal(http.StatusUnprocessableEntity, "checksum_mismatch",
			"the uploaded file does not match the declared sha256; PUT it again and confirm",
			map[string]any{"declared": s.SHA256, "received": got})
	}
	if declared := baseMediaType(s.MimeType); sniffedTypes[declared] {
		if detected := baseMediaType(http.DetectContentType(head.buf)); detected != declared {
			h.dropStaged(ctx, s)
			return "", uploadRefusal(http.StatusUnprocessableEntity, "mime_mismatch",
				"the uploaded file is not of the declared type",
				map[string]any{"declared": declared, "detected": detected})
		}
	}
	return info.ETag, nil
}

// dropStaged removes the staged object, on a context of its own.
func (h *UploadSessionHandler) dropStaged(ctx context.Context, s *postgres.UploadSession) {
	cctx, cancel := detached(ctx)
	defer cancel()
	if err := h.store.Delete(cctx, s.StagingKey); err != nil && !errors.Is(err, storage.ErrNotFound) {
		log.Printf("upload %s: delete staged object: %v", s.ID, err)
	}
}

func (h *UploadSessionHandler) expired(c echo.Context, s *postgres.UploadSession) error {
	h.dropStaged(c.Request().Context(), s)
	return handleError(c, uploadRefusal(http.StatusGone, "upload_expired",
		"the upload expired; declare it again", map[string]any{"expires_at": s.ExpiresAt}))
}

// replay answers a repeated confirm with the row the first one wrote.
func (h *UploadSessionHandler) replay(c echo.Context, s *postgres.UploadSession) error {
	body, found, apiErr := h.resultBody(c.Request().Context(), s, *s.ResultID)
	if apiErr != nil {
		return handleError(c, apiErr)
	}
	if !found {
		if s.Kind == postgres.UploadKindArtifact {
			return handleError(c, apierror.NotFound("Artifact"))
		}
		return handleError(c, apierror.NotFound("Attachment"))
	}
	return c.JSON(http.StatusOK, body)
}

// resultBody loads the row an upload became and renders the confirm answer;
// found is false when there is no live row with that id.
func (h *UploadSessionHandler) resultBody(ctx context.Context, s *postgres.UploadSession, resultID uuid.UUID) (body map[string]any, found bool, apiErr *apierror.Error) {
	switch s.Kind {
	case postgres.UploadKindDocumentAttachment:
		att, err := h.attachments.GetByIDInWorkspace(ctx, resultID, s.WorkspaceID)
		if err != nil {
			return nil, false, apierror.InternalError("could not load the attachment")
		}
		if att == nil || att.DeletedAt != nil {
			return nil, false, nil
		}
		return documentUploadResult(s, att), true, nil
	case postgres.UploadKindArtifact:
		art, err := h.artifactSvc.GetByIDInWorkspace(ctx, resultID, s.WorkspaceID)
		var ae *apierror.Error
		if errors.As(err, &ae) && ae.Code == http.StatusNotFound {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, apierror.InternalError("could not load the artifact")
		}
		return artifactUploadResult(s, art), true, nil
	}
	return nil, false, apierror.InternalError("unknown upload kind")
}

func documentUploadResult(s *postgres.UploadSession, att *domain.DocumentAttachment) map[string]any {
	path := "/api/v1/document-attachments/" + att.ID.String() + "/download?disposition=inline"
	return map[string]any{
		"kind":           postgres.UploadKindDocumentAttachment,
		"attachment_id":  att.ID,
		"document_id":    att.DocumentID,
		"name":           att.Name,
		"mime_type":      att.MimeType,
		"size_bytes":     att.SizeBytes,
		"sha256":         s.SHA256,
		"download_path":  path,
		"markdown":       uploadMarkdown(att.Name, att.MimeType, path),
		"renders_inline": service.IsInlineSafeMimeType(att.MimeType),
	}
}

func artifactUploadResult(s *postgres.UploadSession, art *domain.Artifact) map[string]any {
	path := "/api/v1/artifacts/" + art.ID.String() + "/download?disposition=inline"
	return map[string]any{
		"kind":           postgres.UploadKindArtifact,
		"artifact_id":    art.ID,
		"task_id":        art.TaskID,
		"name":           art.Name,
		"mime_type":      art.MimeType,
		"size_bytes":     art.SizeBytes,
		"sha256":         s.SHA256,
		"download_path":  path,
		"markdown":       uploadMarkdown(art.Name, art.MimeType, path),
		"renders_inline": service.IsInlineSafeMimeType(art.MimeType),
	}
}

var markdownLabelEscaper = strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`)

// uploadMarkdown is the snippet to paste into a document body: an image for
// the raster types a body renders, a link for everything else. It carries the
// stable API path, never a presigned URL, which expires.
func uploadMarkdown(name, mimeType, path string) string {
	label := markdownLabelEscaper.Replace(name)
	if markdownImageTypes[baseMediaType(mimeType)] {
		return "![" + label + "](" + path + ")"
	}
	return "[" + label + "](" + path + ")"
}

func baseMediaType(t string) string {
	if i := strings.Index(t, ";"); i >= 0 {
		t = t[:i]
	}
	return strings.ToLower(strings.TrimSpace(t))
}

// headCapture keeps the first limit bytes written to it.
type headCapture struct {
	buf   []byte
	limit int
}

func (w *headCapture) Write(p []byte) (int, error) {
	if room := w.limit - len(w.buf); room > 0 {
		w.buf = append(w.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// Sweeper settings for SweepUploads.
const (
	UploadSweepInterval = 5 * time.Minute
	// uploadSweepGrace keeps an expired session a little past expires_at, so a
	// PUT that started just before expiry is not cut off by the sweep.
	uploadSweepGrace = 5 * time.Minute
	// uploadConfirmedRetention keeps a confirmed session so a repeated confirm
	// answers with its result.
	uploadConfirmedRetention = 7 * 24 * time.Hour
	uploadSweepBatch         = 200
)

// uploadSweeper is what SweepUploads needs of the session store.
type uploadSweeper interface {
	Sweep(ctx context.Context, grace, retention time.Duration, limit int,
		deleteObject func(ctx context.Context, key string) error) (int, error)
}

// SweepUploads removes expired, unconfirmed upload sessions and their staged
// objects, and confirmed sessions past their retention. An object that is
// already gone counts as removed.
func SweepUploads(ctx context.Context, sessions uploadSweeper, store interface {
	Delete(ctx context.Context, key string) error
},
) (int, error) {
	return sessions.Sweep(ctx, uploadSweepGrace, uploadConfirmedRetention, uploadSweepBatch,
		func(ctx context.Context, key string) error {
			if err := store.Delete(ctx, key); err != nil && !errors.Is(err, storage.ErrNotFound) {
				return err
			}
			return nil
		})
}
