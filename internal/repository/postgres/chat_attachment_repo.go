package postgres

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ChatAttachmentHardLimit is the Docs upload ceiling (25 MiB). The workspace
// setting max_attachment_mb can only lower it.
const ChatAttachmentHardLimit int64 = 25 << 20

// ChatAttachment is one ext_agent_chat_attachments row.
type ChatAttachment struct {
	ID          uuid.UUID  `db:"id"`
	WorkspaceID uuid.UUID  `db:"workspace_id"`
	ThreadID    uuid.UUID  `db:"thread_id"`
	MessageID   *uuid.UUID `db:"message_id"`
	S3Key       string     `db:"s3_key"`
	FileName    string     `db:"file_name"`
	Mime        string     `db:"mime"`
	Size        int64      `db:"size"`
	Width       *int       `db:"width"`
	Height      *int       `db:"height"`
	UploadedAt  *time.Time `db:"uploaded_at"`
}

type ChatAttachmentRepo struct{ db *sqlx.DB }

func NewChatAttachmentRepo(db *sqlx.DB) *ChatAttachmentRepo { return &ChatAttachmentRepo{db: db} }

func chatThreadNotFound() error {
	return apierror.NotFoundWithDetails("thread", "thread_not_found")
}

func chatAttachmentNotFound() error {
	return apierror.NotFoundWithDetails("attachment", "attachment_not_found")
}

func chatTooLarge() error {
	return &apierror.Error{Code: http.StatusRequestEntityTooLarge, ErrorCode: apierror.CodeBadRequest,
		Message: "attachment is larger than the workspace limit", Details: "attachment_too_large"}
}

func chatUserOnly(actor UserRuntimeActor) error {
	if actor.Connector || actor.AgentID != uuid.Nil || actor.UserID == uuid.Nil {
		return apierror.ForbiddenWithDetails("chat is for signed-in users", "user_only")
	}
	return nil
}

// maxAttachmentBytes reads max_attachment_mb of the workspace's agent-chat
// integration_configs row. A missing row, an unreadable value or one outside
// 1..25 falls back to (or is clamped to) the 25 MiB default and ceiling.
func chatMaxAttachmentBytes(mb sql.NullInt64) int64 {
	if !mb.Valid || mb.Int64 < 1 || mb.Int64<<20 > ChatAttachmentHardLimit {
		return ChatAttachmentHardLimit
	}
	return mb.Int64 << 20
}

// Create records a pending attachment for one of the caller's threads in the
// project. The thread is resolved with user_id = caller in the query itself, so
// a stranger's, a deleted and a missing thread are all thread_not_found.
// size is checked against the workspace limit before the row exists.
func (r *ChatAttachmentRepo) Create(ctx context.Context, actor UserRuntimeActor, project, thread uuid.UUID,
	fileName, mime string, size int64, width, height *int, keyFor func(ws, id uuid.UUID) string) (ChatAttachment, error) {
	if err := chatUserOnly(actor); err != nil {
		return ChatAttachment{}, err
	}
	var row struct {
		WorkspaceID uuid.UUID     `db:"workspace_id"`
		MaxMB       sql.NullInt64 `db:"max_mb"`
	}
	err := r.db.GetContext(ctx, &row, `SELECT t.workspace_id,
			(SELECT NULLIF(c.config->>'max_attachment_mb','')::bigint FROM integration_configs c
				WHERE c.workspace_id = t.workspace_id AND c.provider = 'agent-chat'
				AND (c.config->>'max_attachment_mb') ~ '^[0-9]{1,9}$') AS max_mb
		FROM ext_agent_chat_threads t
		WHERE t.id = $1 AND t.project_id = $2 AND t.user_id = $3 AND t.deleted_at IS NULL`,
		thread, project, actor.UserID)
	if errors.Is(err, sql.ErrNoRows) {
		return ChatAttachment{}, chatThreadNotFound()
	}
	if err != nil {
		return ChatAttachment{}, err
	}
	if size > chatMaxAttachmentBytes(row.MaxMB) {
		return ChatAttachment{}, chatTooLarge()
	}
	att := ChatAttachment{ID: uuid.New(), WorkspaceID: row.WorkspaceID, ThreadID: thread,
		FileName: fileName, Mime: mime, Size: size, Width: width, Height: height}
	att.S3Key = keyFor(att.WorkspaceID, att.ID)
	_, err = r.db.ExecContext(ctx, `INSERT INTO ext_agent_chat_attachments
		(id, workspace_id, thread_id, s3_key, file_name, mime, size, width, height)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		att.ID, att.WorkspaceID, att.ThreadID, att.S3Key, att.FileName, att.Mime, att.Size, att.Width, att.Height)
	return att, err
}

// GetOwned returns an attachment of one of the caller's live threads in the
// project. Someone else's attachment is attachment_not_found, never 403.
func (r *ChatAttachmentRepo) GetOwned(ctx context.Context, actor UserRuntimeActor, project, id uuid.UUID) (ChatAttachment, error) {
	if err := chatUserOnly(actor); err != nil {
		return ChatAttachment{}, err
	}
	var att ChatAttachment
	err := r.db.GetContext(ctx, &att, `SELECT a.id, a.workspace_id, a.thread_id, a.message_id, a.s3_key,
			a.file_name, a.mime, a.size, a.width, a.height, a.uploaded_at
		FROM ext_agent_chat_attachments a
		JOIN ext_agent_chat_threads t ON t.id = a.thread_id AND t.workspace_id = a.workspace_id
		WHERE a.id = $1 AND t.project_id = $2 AND t.user_id = $3 AND t.deleted_at IS NULL`,
		id, project, actor.UserID)
	if errors.Is(err, sql.ErrNoRows) {
		return ChatAttachment{}, chatAttachmentNotFound()
	}
	return att, err
}

// PendingInThread returns the not-yet-confirmed, unbound attachments among ids
// that belong to the thread. Anything else is left for SendMessage to refuse.
func (r *ChatAttachmentRepo) PendingInThread(ctx context.Context, ws, thread uuid.UUID, ids []uuid.UUID) ([]ChatAttachment, error) {
	var out []ChatAttachment
	err := r.db.SelectContext(ctx, &out, `SELECT id, workspace_id, thread_id, message_id, s3_key,
			file_name, mime, size, width, height, uploaded_at
		FROM ext_agent_chat_attachments
		WHERE workspace_id = $1 AND thread_id = $2 AND id = ANY($3::uuid[])
			AND message_id IS NULL AND uploaded_at IS NULL`,
		ws, thread, pq.Array(uuidStrings(ids)))
	return out, err
}

// MarkUploaded stamps uploaded_at once the object is confirmed in S3.
func (r *ChatAttachmentRepo) MarkUploaded(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `UPDATE ext_agent_chat_attachments SET uploaded_at = now()
		WHERE id = $1 AND uploaded_at IS NULL`, id)
	return err
}
