package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// ChatTurnAttachment is an attachment of the user message a held turn runs on:
// what a runner needs to ask object storage for a fresh download link.
type ChatTurnAttachment struct {
	ID          uuid.UUID
	WorkspaceID uuid.UUID
	FileName    string
	Mime        string
	Size        int64
}

// HeldTurnAttachment returns one attachment of the turn's user message, but only
// to the runner that holds the turn right now: the turn is in workspaceID, of one
// of agentIDs, held by runnerID and claimed or streaming (the predicate of
// ActiveTurns). Anything else about the turn is the same 404 chat_turn_not_found
// (no existence oracle); an attachment that is not an uploaded file of that
// message is attachment_not_found. No claim_token is asked for: the link is read
// access to a file the holder was already handed with the claim.
func (r *ChatRunnerRepo) HeldTurnAttachment(ctx context.Context, workspaceID uuid.UUID, agentIDs []uuid.UUID,
	runnerID, turnID, attachmentID uuid.UUID) (*ChatTurnAttachment, error) {
	// One statement: the LEFT JOIN keeps the turn row when the attachment does
	// not match, which is what tells the two not-found answers apart.
	var row struct {
		ID       uuid.NullUUID  `db:"id"`
		FileName sql.NullString `db:"file_name"`
		Mime     sql.NullString `db:"mime"`
		Size     sql.NullInt64  `db:"size"`
	}
	err := r.db.GetContext(ctx, &row, `
		SELECT a.id, a.file_name, a.mime, a.size
		  FROM ext_agent_chat_turns t
		  LEFT JOIN ext_agent_chat_attachments a
		    ON a.id = $5 AND a.workspace_id = t.workspace_id
		   AND a.message_id = t.user_message_id AND a.uploaded_at IS NOT NULL
		 WHERE t.id = $1 AND t.workspace_id = $2 AND t.agent_id = ANY($3) AND t.runner_id = $4
		   AND t.state IN ('claimed', 'streaming')`,
		turnID, workspaceID, pq.Array(agentIDs), runnerID, attachmentID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, errChatTurnNotFound()
	case err != nil:
		return nil, err
	case !row.ID.Valid:
		return nil, chatAttachmentNotFound()
	}
	return &ChatTurnAttachment{ID: row.ID.UUID, WorkspaceID: workspaceID, FileName: row.FileName.String,
		Mime: row.Mime.String, Size: row.Size.Int64}, nil
}
