package postgres

import (
	"context"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// DocumentCommentMentionRepo implements
// repository.DocumentCommentMentionRepository against PostgreSQL.
type DocumentCommentMentionRepo struct {
	db *sqlx.DB
}

// NewDocumentCommentMentionRepo returns a new DocumentCommentMentionRepo.
func NewDocumentCommentMentionRepo(db *sqlx.DB) *DocumentCommentMentionRepo {
	return &DocumentCommentMentionRepo{db: db}
}

// InsertBatch records the mentions extracted from one comment.
//
// ON CONFLICT DO NOTHING, keyed on the table's (comment_id, mentioned_id)
// primary key: re-running extraction over an edited body must not raise, and
// must not reset seen_at on a mention the recipient has already read.
func (r *DocumentCommentMentionRepo) InsertBatch(ctx context.Context, mentions []domain.DocumentCommentMention) error {
	if len(mentions) == 0 {
		return nil
	}
	const q = `
		INSERT INTO document_comment_mentions
			(comment_id, mentioned_id, mentioned_kind, mentioned_slug, extracted_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (comment_id, mentioned_id) DO NOTHING
	`
	for _, m := range mentions {
		if _, err := r.db.ExecContext(ctx, q,
			m.CommentID, m.MentionedID, m.MentionedKind, m.MentionedSlug, m.ExtractedAt,
		); err != nil {
			return err
		}
	}
	return nil
}

// List returns the recipient's document mentions, newest first.
//
// The joins are not decoration: dc.deleted_at IS NULL and d.deleted_at IS NULL
// are what keep a mention on a deleted comment, or on a deleted page, out of an
// inbox that would otherwise offer to open something that 404s. The same filter
// pair is applied in CountUnseen so the badge and the list agree.
func (r *DocumentCommentMentionRepo) List(
	ctx context.Context,
	mentionedID uuid.UUID,
	mentionedKind string,
	filter repository.MentionFilter,
) ([]domain.DocumentCommentMentionView, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}

	args := []any{mentionedID, mentionedKind}
	where := `dcm.mentioned_id = $1 AND dcm.mentioned_kind = $2
	          AND dc.deleted_at IS NULL AND d.deleted_at IS NULL`

	if filter.Seen != nil {
		if *filter.Seen {
			where += ` AND dcm.seen_at IS NOT NULL`
		} else {
			where += ` AND dcm.seen_at IS NULL`
		}
	}
	if filter.Since != nil {
		args = append(args, *filter.Since)
		where += ` AND dcm.extracted_at > $` + strconv.Itoa(len(args))
	}
	if filter.ProjectID != nil {
		args = append(args, *filter.ProjectID)
		where += ` AND d.project_id = $` + strconv.Itoa(len(args))
	}

	args = append(args, limit)
	q := `
		SELECT
			dcm.comment_id,
			dcm.mentioned_id,
			dcm.mentioned_kind,
			dcm.mentioned_slug,
			dcm.extracted_at,
			dcm.seen_at,
			d.id         AS document_id,
			d.title      AS document_title,
			d.slug       AS document_slug,
			d.project_id AS project_id,
			dc.body      AS comment_body,
			dc.author_id AS author_id,
			COALESCE(u.display_name, a.name, '') AS author_name
		FROM document_comment_mentions dcm
		JOIN document_comments dc ON dc.id = dcm.comment_id
		JOIN documents d          ON d.id = dc.document_id
		LEFT JOIN users  u ON u.id = dc.author_id AND dc.author_type = 'user'
		LEFT JOIN agents a ON a.id = dc.author_id AND dc.author_type = 'agent'
		WHERE ` + where + `
		ORDER BY dcm.extracted_at DESC
		LIMIT $` + strconv.Itoa(len(args))

	var rows []domain.DocumentCommentMentionView
	if err := r.db.SelectContext(ctx, &rows, q, args...); err != nil {
		return nil, err
	}
	return rows, nil
}

// MarkSeen stamps one mention as read by its recipient.
//
// Keyed on (comment_id, mentioned_id) with `seen_at IS NULL`, so a caller can
// only ever mark their own row and only the first mark wins — a replayed request
// does not move the timestamp.
func (r *DocumentCommentMentionRepo) MarkSeen(ctx context.Context, commentID, mentionedID uuid.UUID) error {
	const q = `
		UPDATE document_comment_mentions
		   SET seen_at = $1
		 WHERE comment_id = $2 AND mentioned_id = $3 AND seen_at IS NULL
	`
	_, err := r.db.ExecContext(ctx, q, time.Now().UTC(), commentID, mentionedID)
	return err
}

// CountUnseen is the badge number, over the same live-row filter List applies.
func (r *DocumentCommentMentionRepo) CountUnseen(
	ctx context.Context,
	mentionedID uuid.UUID,
	mentionedKind string,
) (int64, error) {
	const q = `
		SELECT COUNT(*)
		FROM document_comment_mentions dcm
		JOIN document_comments dc ON dc.id = dcm.comment_id
		JOIN documents d          ON d.id = dc.document_id
		WHERE dcm.mentioned_id = $1
		  AND dcm.mentioned_kind = $2
		  AND dcm.seen_at IS NULL
		  AND dc.deleted_at IS NULL
		  AND d.deleted_at IS NULL
	`
	var count int64
	if err := r.db.GetContext(ctx, &count, q, mentionedID, mentionedKind); err != nil {
		return 0, err
	}
	return count, nil
}
