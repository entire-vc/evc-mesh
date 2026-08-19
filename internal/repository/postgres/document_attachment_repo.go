package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// documentAttachmentRow is the DB row representation. Every field of
// domain.DocumentAttachment has a column, so unlike documentRow there is nothing
// the service layer fills in afterwards.
type documentAttachmentRow struct {
	ID             uuid.UUID        `db:"id"`
	DocumentID     uuid.UUID        `db:"document_id"`
	Name           string           `db:"name"`
	MimeType       string           `db:"mime_type"`
	SizeBytes      int64            `db:"size_bytes"`
	StorageKey     string           `db:"storage_key"`
	UploadedBy     uuid.UUID        `db:"uploaded_by"`
	UploadedByType domain.ActorType `db:"uploaded_by_type"`
	CreatedAt      time.Time        `db:"created_at"`
	DeletedAt      *time.Time       `db:"deleted_at"`
}

// documentAttachmentSelectCols lists every column documentAttachmentRow scans —
// see artifactSelectCols for why no query here uses `SELECT *`.
const documentAttachmentSelectCols = `id, document_id, name, mime_type, size_bytes, storage_key, uploaded_by, uploaded_by_type, created_at, deleted_at`

// documentAttachmentSelectColsQualified is documentAttachmentSelectCols prefixed
// `a.`, for the query that JOINs documents and projects (all three tables have
// created_at and deleted_at).
const documentAttachmentSelectColsQualified = `a.id, a.document_id, a.name, a.mime_type, a.size_bytes, a.storage_key, a.uploaded_by, a.uploaded_by_type, a.created_at, a.deleted_at`

func (r *documentAttachmentRow) toDomain() domain.DocumentAttachment {
	return domain.DocumentAttachment{
		ID:             r.ID,
		DocumentID:     r.DocumentID,
		Name:           r.Name,
		MimeType:       r.MimeType,
		SizeBytes:      r.SizeBytes,
		StorageKey:     r.StorageKey,
		UploadedBy:     r.UploadedBy,
		UploadedByType: r.UploadedByType,
		CreatedAt:      r.CreatedAt,
		DeletedAt:      r.DeletedAt,
	}
}

// DocumentAttachmentRepo implements repository.DocumentAttachmentRepository with
// PostgreSQL.
type DocumentAttachmentRepo struct {
	db *sqlx.DB
}

// NewDocumentAttachmentRepo creates a new DocumentAttachmentRepo.
func NewDocumentAttachmentRepo(db *sqlx.DB) *DocumentAttachmentRepo {
	return &DocumentAttachmentRepo{db: db}
}

func (r *DocumentAttachmentRepo) Create(ctx context.Context, att *domain.DocumentAttachment) error {
	const q = `
		INSERT INTO document_attachments (
			id, document_id, name, mime_type, size_bytes, storage_key,
			uploaded_by, uploaded_by_type, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	_, err := r.db.ExecContext(ctx, q,
		att.ID, att.DocumentID, att.Name, att.MimeType, att.SizeBytes, att.StorageKey,
		att.UploadedBy, att.UploadedByType, att.CreatedAt,
	)
	return err
}

// GetByIDInWorkspace returns the attachment only when its document's project
// belongs to workspaceID. Returns nil (no error) otherwise, so callers answer 404
// without telling the caller whether the id exists in some other tenant.
//
// A soft-deleted DOCUMENT hides its attachments too: the document is gone as far
// as every read is concerned, and an image that still resolved from a deleted page
// would be a live link to content the tenant believes they removed.
func (r *DocumentAttachmentRepo) GetByIDInWorkspace(ctx context.Context, id, workspaceID uuid.UUID) (*domain.DocumentAttachment, error) {
	const q = `
		SELECT ` + documentAttachmentSelectColsQualified + `
		  FROM document_attachments a
		  JOIN documents d ON a.document_id = d.id
		  JOIN projects p ON d.project_id = p.id
		 WHERE a.id = $1
		   AND a.deleted_at IS NULL
		   AND d.deleted_at IS NULL
		   AND p.workspace_id = $2`
	var row documentAttachmentRow
	if err := r.db.GetContext(ctx, &row, q, id, workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	a := row.toDomain()
	return &a, nil
}

func (r *DocumentAttachmentRepo) ListByDocument(ctx context.Context, documentID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.DocumentAttachment], error) {
	pg.Normalize()

	const countQ = `SELECT COUNT(*) FROM document_attachments WHERE document_id = $1 AND deleted_at IS NULL`
	var totalCount int
	if err := r.db.GetContext(ctx, &totalCount, countQ, documentID); err != nil {
		return nil, err
	}

	// Oldest first, matching the order they were added to the page. id is the
	// tiebreak so a page boundary cannot repeat or skip a row when two uploads
	// share a created_at — the same reason ListByProject carries one.
	dataQ := fmt.Sprintf(
		`SELECT `+documentAttachmentSelectCols+`
		   FROM document_attachments
		  WHERE document_id = $1 AND deleted_at IS NULL
		  ORDER BY created_at ASC, id ASC %s`,
		paginationClause(pg),
	)
	var rows []documentAttachmentRow
	if err := r.db.SelectContext(ctx, &rows, dataQ, documentID); err != nil {
		return nil, err
	}

	items := make([]domain.DocumentAttachment, len(rows))
	for i := range rows {
		items[i] = rows[i].toDomain()
	}

	return pagination.NewPage(items, totalCount, pg), nil
}

// SoftDelete stamps deleted_at. Nothing cascades: an attachment has no children,
// and its document outlives it.
func (r *DocumentAttachmentRepo) SoftDelete(ctx context.Context, id uuid.UUID, at time.Time) error {
	const q = `UPDATE document_attachments SET deleted_at = $2 WHERE id = $1 AND deleted_at IS NULL`
	res, err := r.db.ExecContext(ctx, q, id, at)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Attachment")
	}
	return nil
}
