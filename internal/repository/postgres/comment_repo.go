package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// commentEnrichedSelect provides columns for comment queries including
// author_name resolved via correlated subquery (same pattern as task assignee_name).
const commentEnrichedSelect = `SELECT c.id, c.task_id, c.parent_comment_id, c.author_id, c.author_type,
	c.body, c.metadata, c.is_internal, c.created_at, c.updated_at,
	CASE
		WHEN c.author_type = 'agent' THEN
			(SELECT name FROM agents WHERE id = c.author_id AND deleted_at IS NULL)
		WHEN c.author_type = 'user' THEN
			(SELECT display_name FROM users WHERE id = c.author_id)
		ELSE NULL
	END AS author_name`

// CommentRepo implements repository.CommentRepository with PostgreSQL.
type CommentRepo struct {
	db *sqlx.DB
}

// NewCommentRepo creates a new CommentRepo.
func NewCommentRepo(db *sqlx.DB) *CommentRepo {
	return &CommentRepo{db: db}
}

func (r *CommentRepo) Create(ctx context.Context, comment *domain.Comment) error {
	const q = `
		INSERT INTO comments (id, task_id, parent_comment_id, author_id, author_type, body, metadata, is_internal, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	metadata := comment.Metadata
	if metadata == nil {
		metadata = json.RawMessage(`{}`)
	}
	_, err := r.db.ExecContext(ctx, q,
		comment.ID, comment.TaskID, comment.ParentCommentID,
		comment.AuthorID, comment.AuthorType, comment.Body,
		metadata, comment.IsInternal,
		comment.CreatedAt, comment.UpdatedAt,
	)
	return err
}

func (r *CommentRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Comment, error) {
	const q = commentEnrichedSelect + ` FROM comments c WHERE c.id = $1`
	var comment domain.Comment
	if err := r.db.GetContext(ctx, &comment, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &comment, nil
}

func (r *CommentRepo) Update(ctx context.Context, comment *domain.Comment) error {
	const q = `
		UPDATE comments
		SET body = $2, metadata = $3, is_internal = $4, updated_at = $5
		WHERE id = $1
	`
	metadata := comment.Metadata
	if metadata == nil {
		metadata = json.RawMessage(`{}`)
	}
	res, err := r.db.ExecContext(ctx, q,
		comment.ID, comment.Body, metadata, comment.IsInternal, comment.UpdatedAt,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Comment")
	}
	return nil
}

func (r *CommentRepo) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM comments WHERE id = $1`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Comment")
	}
	return nil
}

func (r *CommentRepo) ListByTask(ctx context.Context, taskID uuid.UUID, filter repository.CommentFilter, pg pagination.Params) (*pagination.Page[domain.Comment], error) {
	pg.Normalize()

	args := []interface{}{taskID} // $1
	conditions := []string{"c.task_id = $1", "c.parent_comment_id IS NULL"}
	argIdx := 2

	if !filter.IncludeInternal {
		conditions = append(conditions, fmt.Sprintf("c.is_internal = $%d", argIdx))
		args = append(args, false)
		argIdx++
	}

	where := "WHERE " + joinAnd(conditions)

	// Count
	countQ := fmt.Sprintf(`SELECT COUNT(*) FROM comments c %s`, where)
	var totalCount int
	if err := r.db.GetContext(ctx, &totalCount, countQ, args...); err != nil {
		return nil, err
	}

	// Data -- top-level comments ordered by creation time
	dataQ := fmt.Sprintf(commentEnrichedSelect+` FROM comments c %s ORDER BY c.created_at ASC %s`, where, paginationClause(pg))
	var comments []domain.Comment
	if err := r.db.SelectContext(ctx, &comments, dataQ, args...); err != nil {
		return nil, err
	}

	return pagination.NewPage(comments, totalCount, pg), nil
}

func (r *CommentRepo) ListReplies(ctx context.Context, parentCommentID uuid.UUID) ([]domain.Comment, error) {
	q := commentEnrichedSelect + ` FROM comments c WHERE c.parent_comment_id = $1 ORDER BY c.created_at ASC`
	var comments []domain.Comment
	if err := r.db.SelectContext(ctx, &comments, q, parentCommentID); err != nil {
		return nil, err
	}
	if comments == nil {
		comments = []domain.Comment{}
	}
	return comments, nil
}
