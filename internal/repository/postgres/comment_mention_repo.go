package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// CommentMentionRepo implements repository.CommentMentionRepository against PostgreSQL.
type CommentMentionRepo struct {
	db *sqlx.DB
}

// NewCommentMentionRepo returns a new CommentMentionRepo.
func NewCommentMentionRepo(db *sqlx.DB) *CommentMentionRepo {
	return &CommentMentionRepo{db: db}
}

func (r *CommentMentionRepo) InsertBatch(ctx context.Context, mentions []domain.CommentMention) error {
	if len(mentions) == 0 {
		return nil
	}
	const q = `
		INSERT INTO comment_mentions (comment_id, mentioned_id, mentioned_kind, mentioned_slug, extracted_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (comment_id, mentioned_id) DO NOTHING
	`
	for _, m := range mentions {
		if _, err := r.db.ExecContext(ctx, q, m.CommentID, m.MentionedID, m.MentionedKind, m.MentionedSlug, m.ExtractedAt); err != nil {
			return err
		}
	}
	return nil
}

func (r *CommentMentionRepo) List(ctx context.Context, mentionedID uuid.UUID, mentionedKind string, filter repository.MentionFilter) ([]domain.CommentMentionView, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}

	args := []any{mentionedID, mentionedKind}
	where := `cm.mentioned_id = $1 AND cm.mentioned_kind = $2`

	if filter.Seen != nil {
		if *filter.Seen {
			where += ` AND cm.seen_at IS NOT NULL`
		} else {
			where += ` AND cm.seen_at IS NULL`
		}
	}
	if filter.Since != nil {
		args = append(args, *filter.Since)
		where += ` AND cm.extracted_at > $` + fmt.Sprintf("%d", len(args))
	}
	if filter.ProjectID != nil {
		args = append(args, *filter.ProjectID)
		where += ` AND t.project_id = $` + fmt.Sprintf("%d", len(args))
	}

	args = append(args, limit)
	q := `
		SELECT
			cm.comment_id,
			cm.mentioned_id,
			cm.mentioned_kind,
			cm.mentioned_slug,
			cm.extracted_at,
			cm.seen_at,
			t.id         AS task_id,
			t.title      AS task_title,
			t.project_id AS project_id,
			c.body       AS comment_body,
			c.author_id  AS author_id,
			COALESCE(u.display_name, a.name, '') AS author_name
		FROM comment_mentions cm
		JOIN comments c ON c.id = cm.comment_id
		JOIN tasks t    ON t.id = c.task_id
		LEFT JOIN users  u ON u.id = c.author_id AND c.author_type = 'user'
		LEFT JOIN agents a ON a.id = c.author_id AND c.author_type = 'agent'
		WHERE ` + where + `
		ORDER BY cm.extracted_at DESC
		LIMIT $` + fmt.Sprintf("%d", len(args))

	var rows []domain.CommentMentionView
	if err := r.db.SelectContext(ctx, &rows, q, args...); err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *CommentMentionRepo) MarkSeen(ctx context.Context, commentID, mentionedID uuid.UUID) error {
	const q = `UPDATE comment_mentions SET seen_at = $1 WHERE comment_id = $2 AND mentioned_id = $3 AND seen_at IS NULL`
	_, err := r.db.ExecContext(ctx, q, time.Now().UTC(), commentID, mentionedID)
	return err
}

func (r *CommentMentionRepo) CountUnseen(ctx context.Context, mentionedID uuid.UUID, mentionedKind string) (int64, error) {
	const q = `SELECT COUNT(*) FROM comment_mentions WHERE mentioned_id = $1 AND mentioned_kind = $2 AND seen_at IS NULL`
	var count int64
	if err := r.db.GetContext(ctx, &count, q, mentionedID, mentionedKind); err != nil {
		return 0, err
	}
	return count, nil
}
