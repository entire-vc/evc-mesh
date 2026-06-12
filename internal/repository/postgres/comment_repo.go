package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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
			(SELECT COALESCE(NULLIF(u.display_name, ''), SPLIT_PART(u.email, '@', 1)) FROM users u WHERE u.id = c.author_id)
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
	conditions := []string{"c.task_id = $1"}
	argIdx := 2

	if !filter.IncludeInternal {
		conditions = append(conditions, fmt.Sprintf("c.is_internal = $%d", argIdx))
		args = append(args, false)
	}

	where := "WHERE " + joinAnd(conditions)

	// Count
	countQ := fmt.Sprintf(`SELECT COUNT(*) FROM comments c %s`, where)
	var totalCount int
	if err := r.db.GetContext(ctx, &totalCount, countQ, args...); err != nil {
		return nil, err
	}

	// Data -- all comments (top-level + replies) ordered by creation time
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

// commentViewSelect is the base SELECT for enriched comment view queries (activity feed).
const commentViewSelect = `SELECT
	c.id         AS comment_id,
	c.task_id,
	t.title      AS task_title,
	t.project_id,
	p.name       AS project_name,
	c.body       AS comment_body,
	c.author_id,
	c.author_type AS author_kind,
	CASE
		WHEN c.author_type = 'agent' THEN
			(SELECT name FROM agents WHERE id = c.author_id AND deleted_at IS NULL)
		WHEN c.author_type = 'user' THEN
			(SELECT COALESCE(NULLIF(u.display_name, ''), SPLIT_PART(u.email, '@', 1)) FROM users u WHERE u.id = c.author_id)
		ELSE ''
	END AS author_name,
	c.created_at,
	c.updated_at
FROM comments c
JOIN tasks t ON t.id = c.task_id
JOIN projects p ON p.id = t.project_id`

// ListByAuthor returns the caller's own comments across workspaces, newest first.
func (r *CommentRepo) ListByAuthor(ctx context.Context, authorID uuid.UUID, filter repository.CommentViewFilter) ([]domain.CommentView, *time.Time, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	args := []any{authorID}
	where := `c.author_id = $1 AND c.is_internal = false`

	if filter.WorkspaceID != nil {
		args = append(args, *filter.WorkspaceID)
		where += fmt.Sprintf(` AND p.workspace_id = $%d`, len(args))
	}
	if filter.ProjectID != nil {
		args = append(args, *filter.ProjectID)
		where += fmt.Sprintf(` AND t.project_id = $%d`, len(args))
	}
	if filter.Before != nil {
		args = append(args, *filter.Before)
		where += fmt.Sprintf(` AND c.created_at < $%d`, len(args))
	}

	args = append(args, limit)
	q := commentViewSelect + ` WHERE ` + where + fmt.Sprintf(` ORDER BY c.created_at DESC LIMIT $%d`, len(args))

	var rows []domain.CommentView
	if err := r.db.SelectContext(ctx, &rows, q, args...); err != nil {
		return nil, nil, err
	}
	if rows == nil {
		rows = []domain.CommentView{}
	}

	var nextCursor *time.Time
	if len(rows) == limit {
		t := rows[len(rows)-1].CreatedAt
		nextCursor = &t
	}
	return rows, nextCursor, nil
}

// ListRecentByWorkspace returns workspace-wide recent comments, newest first.
func (r *CommentRepo) ListRecentByWorkspace(ctx context.Context, wsID uuid.UUID, filter repository.CommentViewFilter) ([]domain.CommentView, *time.Time, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	args := []any{wsID}
	where := `p.workspace_id = $1 AND c.is_internal = false`

	if filter.Before != nil {
		args = append(args, *filter.Before)
		where += fmt.Sprintf(` AND c.created_at < $%d`, len(args))
	}

	args = append(args, limit)
	q := commentViewSelect + ` WHERE ` + where + fmt.Sprintf(` ORDER BY c.created_at DESC LIMIT $%d`, len(args))

	var rows []domain.CommentView
	if err := r.db.SelectContext(ctx, &rows, q, args...); err != nil {
		return nil, nil, err
	}
	if rows == nil {
		rows = []domain.CommentView{}
	}

	var nextCursor *time.Time
	if len(rows) == limit {
		t := rows[len(rows)-1].CreatedAt
		nextCursor = &t
	}
	return rows, nextCursor, nil
}

// HasAnyComment returns true when the task has at least one comment.
func (r *CommentRepo) HasAnyComment(ctx context.Context, taskID uuid.UUID) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM comments WHERE task_id = $1)`, taskID).Scan(&exists)
	return exists, err
}
