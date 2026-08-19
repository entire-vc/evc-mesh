package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// documentCommentRow is the DB row representation.
//
// The five anchor columns are flat and nullable here and a *domain.DocumentAnchor
// in the domain type: the schema's job is to make "all five or none" checkable
// (ck_document_comments_anchor_complete), and the domain's job is to make a
// partial anchor unrepresentable. Both matter — four-of-five is not a degraded
// anchor, it is one whose missing context reads as "no context", which is exactly
// how a match gets accepted that should have been refused.
type documentCommentRow struct {
	ID         uuid.UUID  `db:"id"`
	DocumentID uuid.UUID  `db:"document_id"`
	ParentID   *uuid.UUID `db:"parent_comment_id"`
	Body       string     `db:"body"`

	AnchorStart  *int    `db:"anchor_start"`
	AnchorEnd    *int    `db:"anchor_end"`
	AnchorExact  *string `db:"anchor_exact"`
	AnchorPrefix *string `db:"anchor_prefix"`
	AnchorSuffix *string `db:"anchor_suffix"`

	ResolvedAt     *time.Time        `db:"resolved_at"`
	ResolvedBy     *uuid.UUID        `db:"resolved_by"`
	ResolvedByType *domain.ActorType `db:"resolved_by_type"`

	AuthorID   uuid.UUID        `db:"author_id"`
	AuthorType domain.ActorType `db:"author_type"`
	CreatedAt  time.Time        `db:"created_at"`
	UpdatedAt  time.Time        `db:"updated_at"`
	DeletedAt  *time.Time       `db:"deleted_at"`
}

// documentCommentSelectCols lists every column documentCommentRow scans — see
// artifactSelectCols for why no query here uses `SELECT *`.
const documentCommentSelectCols = `id, document_id, parent_comment_id, body,
	anchor_start, anchor_end, anchor_exact, anchor_prefix, anchor_suffix,
	resolved_at, resolved_by, resolved_by_type,
	author_id, author_type, created_at, updated_at, deleted_at`

// documentCommentSelectColsQualified is the same list prefixed `c.`, for the
// query that JOINs documents and projects (all three carry created_at/deleted_at).
const documentCommentSelectColsQualified = `c.id, c.document_id, c.parent_comment_id, c.body,
	c.anchor_start, c.anchor_end, c.anchor_exact, c.anchor_prefix, c.anchor_suffix,
	c.resolved_at, c.resolved_by, c.resolved_by_type,
	c.author_id, c.author_type, c.created_at, c.updated_at, c.deleted_at`

func (r *documentCommentRow) toDomain() domain.DocumentComment {
	c := domain.DocumentComment{
		ID:             r.ID,
		DocumentID:     r.DocumentID,
		ParentID:       r.ParentID,
		Body:           r.Body,
		ResolvedAt:     r.ResolvedAt,
		ResolvedBy:     r.ResolvedBy,
		ResolvedByType: r.ResolvedByType,
		AuthorID:       r.AuthorID,
		AuthorType:     r.AuthorType,
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
		DeletedAt:      r.DeletedAt,
	}
	// All five or none — the CHECK constraint guarantees it, and this reads all
	// five so a row that somehow broke that becomes an anchorless comment rather
	// than one with silently zeroed context.
	if r.AnchorStart != nil && r.AnchorEnd != nil &&
		r.AnchorExact != nil && r.AnchorPrefix != nil && r.AnchorSuffix != nil {
		c.Anchor = &domain.DocumentAnchor{
			Start:  *r.AnchorStart,
			End:    *r.AnchorEnd,
			Exact:  *r.AnchorExact,
			Prefix: *r.AnchorPrefix,
			Suffix: *r.AnchorSuffix,
		}
	}
	return c
}

// DocumentCommentRepo implements repository.DocumentCommentRepository with
// PostgreSQL.
type DocumentCommentRepo struct {
	db *sqlx.DB
}

// NewDocumentCommentRepo creates a new DocumentCommentRepo.
func NewDocumentCommentRepo(db *sqlx.DB) *DocumentCommentRepo {
	return &DocumentCommentRepo{db: db}
}

func (r *DocumentCommentRepo) Create(ctx context.Context, c *domain.DocumentComment) error {
	const q = `
		INSERT INTO document_comments (
			id, document_id, parent_comment_id, body,
			anchor_start, anchor_end, anchor_exact, anchor_prefix, anchor_suffix,
			author_id, author_type, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`
	var (
		start, end            *int
		exact, prefix, suffix *string
	)
	if c.Anchor != nil {
		start, end = &c.Anchor.Start, &c.Anchor.End
		exact, prefix, suffix = &c.Anchor.Exact, &c.Anchor.Prefix, &c.Anchor.Suffix
	}
	_, err := r.db.ExecContext(ctx, q,
		c.ID, c.DocumentID, c.ParentID, c.Body,
		start, end, exact, prefix, suffix,
		c.AuthorID, c.AuthorType, c.CreatedAt, c.UpdatedAt,
	)
	return err
}

// GetByIDInWorkspace returns the comment only when its document's project belongs
// to workspaceID. Returns nil (no error) otherwise, so callers answer 404 without
// telling the caller whether the id exists in some other tenant.
//
// A soft-deleted DOCUMENT hides its comments too, exactly as it hides its
// attachments: the page is gone as far as every read is concerned, and a comment
// that still resolved from a deleted page would be live content the tenant
// believes they removed.
func (r *DocumentCommentRepo) GetByIDInWorkspace(ctx context.Context, id, workspaceID uuid.UUID) (*domain.DocumentComment, error) {
	const q = `
		SELECT ` + documentCommentSelectColsQualified + `
		  FROM document_comments c
		  JOIN documents d ON c.document_id = d.id
		  JOIN projects p ON d.project_id = p.id
		 WHERE c.id = $1
		   AND c.deleted_at IS NULL
		   AND d.deleted_at IS NULL
		   AND p.workspace_id = $2`
	var row documentCommentRow
	if err := r.db.GetContext(ctx, &row, q, id, workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	c := row.toDomain()
	return &c, nil
}

// ListByDocument returns every live comment of the document, oldest first.
//
// Unpaginated on purpose: the caller needs the whole tree to render any of it,
// and a page boundary through a thread would hand back replies whose roots are on
// the next page. id is the tiebreak so the order is total even when two comments
// share a created_at.
func (r *DocumentCommentRepo) ListByDocument(ctx context.Context, documentID uuid.UUID) ([]domain.DocumentComment, error) {
	const q = `
		SELECT ` + documentCommentSelectCols + `
		  FROM document_comments
		 WHERE document_id = $1 AND deleted_at IS NULL
		 ORDER BY created_at ASC, id ASC`
	var rows []documentCommentRow
	if err := r.db.SelectContext(ctx, &rows, q, documentID); err != nil {
		return nil, err
	}
	items := make([]domain.DocumentComment, len(rows))
	for i := range rows {
		items[i] = rows[i].toDomain()
	}
	return items, nil
}

// UpdateBody rewrites one comment's text. The anchor is untouched — editing the
// words of a note does not move where it points.
func (r *DocumentCommentRepo) UpdateBody(ctx context.Context, id uuid.UUID, body string, at time.Time) error {
	const q = `UPDATE document_comments SET body = $2, updated_at = $3
	            WHERE id = $1 AND deleted_at IS NULL`
	res, err := r.db.ExecContext(ctx, q, id, body, at)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Comment")
	}
	return nil
}

// SetResolved marks a thread root resolved, or clears it when resolvedBy is nil.
//
// `parent_comment_id IS NULL` is in the WHERE rather than left to the CHECK
// constraint: the constraint would turn "resolve a reply" into a 500 from a
// violated check, and the honest answer is that there is no such thread — the
// same 404 an unknown id gets.
func (r *DocumentCommentRepo) SetResolved(ctx context.Context, id uuid.UUID, resolvedBy *uuid.UUID, byType *domain.ActorType, at time.Time) error {
	// Resolved and unresolved are one statement so the three columns cannot drift
	// apart: an unresolve that cleared only the timestamp would leave a stale
	// actor behind, and the next reader would attribute a resolution nobody made.
	const q = `
		UPDATE document_comments
		   SET resolved_at = $2, resolved_by = $3, resolved_by_type = $4, updated_at = $5
		 WHERE id = $1 AND deleted_at IS NULL AND parent_comment_id IS NULL`
	var resolvedAt *time.Time
	if resolvedBy != nil {
		resolvedAt = &at
	} else {
		byType = nil
	}
	res, err := r.db.ExecContext(ctx, q, id, resolvedAt, resolvedBy, byType, at)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Comment")
	}
	return nil
}

// SoftDelete stamps deleted_at on the comment and every descendant.
//
// The recursive CTE is the whole point: parent_comment_id is the only link, so
// deleting a root without its replies would leave them live, anchorless and
// displayable nowhere — a thread with its head cut off. ON DELETE CASCADE covers
// only the hard-delete path, which no endpoint takes.
func (r *DocumentCommentRepo) SoftDelete(ctx context.Context, id uuid.UUID, at time.Time) error {
	const q = `
		WITH RECURSIVE subtree AS (
			SELECT id FROM document_comments WHERE id = $1 AND deleted_at IS NULL
			UNION ALL
			SELECT c.id FROM document_comments c
			  JOIN subtree s ON c.parent_comment_id = s.id
			 WHERE c.deleted_at IS NULL
		)
		UPDATE document_comments
		   SET deleted_at = $2
		 WHERE id IN (SELECT id FROM subtree)`
	res, err := r.db.ExecContext(ctx, q, id, at)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Comment")
	}
	return nil
}
