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
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// artifactRow is the DB row representation.
// The DB does NOT have a storage_url column; that field exists only in the domain model
// and is typically computed from storage_key at the service layer.
type artifactRow struct {
	ID             uuid.UUID           `db:"id"`
	TaskID         uuid.UUID           `db:"task_id"`
	Name           string              `db:"name"`
	ArtifactType   domain.ArtifactType `db:"artifact_type"`
	MimeType       string              `db:"mime_type"`
	StorageKey     string              `db:"storage_key"`
	SizeBytes      int64               `db:"size_bytes"`
	ChecksumSHA256 string              `db:"checksum_sha256"`
	Metadata       json.RawMessage     `db:"metadata"`
	UploadedBy     uuid.UUID           `db:"uploaded_by"`
	UploadedByType domain.UploaderType `db:"uploaded_by_type"`
	CreatedAt      time.Time           `db:"created_at"`
}

// artifactSelectCols is every column artifactRow scans, listed explicitly —
// see agentSelectCols in agent_repo.go for why `SELECT *` (bare or alias-
// qualified, e.g. `a.*`) is unsafe: sqlx refuses to scan a column with no
// matching struct field, so an additive migration to this table breaks every
// read until redeployed.
const artifactSelectCols = `id, task_id, name, artifact_type, mime_type, storage_key, size_bytes, checksum_sha256, metadata, uploaded_by, uploaded_by_type, created_at`

// artifactSelectColsQualified is artifactSelectCols with every column
// prefixed `a.`, for queries that JOIN artifacts against other tables whose
// column names could otherwise collide (e.g. both tasks and artifacts have
// created_at).
const artifactSelectColsQualified = `a.id, a.task_id, a.name, a.artifact_type, a.mime_type, a.storage_key, a.size_bytes, a.checksum_sha256, a.metadata, a.uploaded_by, a.uploaded_by_type, a.created_at`

func (r *artifactRow) toDomain() domain.Artifact {
	return domain.Artifact{
		ID:             r.ID,
		TaskID:         r.TaskID,
		Name:           r.Name,
		ArtifactType:   r.ArtifactType,
		MimeType:       r.MimeType,
		StorageKey:     r.StorageKey,
		StorageURL:     "", // Not stored in DB; computed by service layer.
		SizeBytes:      r.SizeBytes,
		ChecksumSHA256: r.ChecksumSHA256,
		Metadata:       r.Metadata,
		UploadedBy:     r.UploadedBy,
		UploadedByType: r.UploadedByType,
		CreatedAt:      r.CreatedAt,
	}
}

// ArtifactRepo implements repository.ArtifactRepository with PostgreSQL.
type ArtifactRepo struct {
	db *sqlx.DB
}

// NewArtifactRepo creates a new ArtifactRepo.
func NewArtifactRepo(db *sqlx.DB) *ArtifactRepo {
	return &ArtifactRepo{db: db}
}

func (r *ArtifactRepo) Create(ctx context.Context, artifact *domain.Artifact) error {
	const q = `
		INSERT INTO artifacts (
			id, task_id, name, artifact_type, mime_type, storage_key,
			size_bytes, checksum_sha256, metadata, uploaded_by, uploaded_by_type, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`
	metadata := artifact.Metadata
	if metadata == nil {
		metadata = json.RawMessage(`{}`)
	}
	_, err := r.db.ExecContext(ctx, q,
		artifact.ID, artifact.TaskID, artifact.Name, artifact.ArtifactType,
		artifact.MimeType, artifact.StorageKey, artifact.SizeBytes,
		artifact.ChecksumSHA256, metadata, artifact.UploadedBy,
		artifact.UploadedByType, artifact.CreatedAt,
	)
	return err
}

func (r *ArtifactRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Artifact, error) {
	const q = `SELECT ` + artifactSelectCols + ` FROM artifacts WHERE id = $1`
	var row artifactRow
	if err := r.db.GetContext(ctx, &row, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	a := row.toDomain()
	return &a, nil
}

// GetByIDInWorkspace returns the artifact only when it belongs to workspaceID.
// Returns nil (no error) when the artifact exists but is in a different workspace,
// so callers can treat it as a 404 without leaking cross-workspace IDs.
func (r *ArtifactRepo) GetByIDInWorkspace(ctx context.Context, id, workspaceID uuid.UUID) (*domain.Artifact, error) {
	const q = `
		SELECT ` + artifactSelectColsQualified + `
		  FROM artifacts a
		  JOIN tasks t ON a.task_id = t.id AND t.deleted_at IS NULL
		  JOIN projects p ON t.project_id = p.id
		 WHERE a.id = $1
		   AND p.workspace_id = $2`
	var row artifactRow
	if err := r.db.GetContext(ctx, &row, q, id, workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	a := row.toDomain()
	return &a, nil
}

// UpdateMetadata overwrites the metadata JSONB column for the given artifact.
func (r *ArtifactRepo) UpdateMetadata(ctx context.Context, id uuid.UUID, metadata json.RawMessage) error {
	if metadata == nil {
		metadata = json.RawMessage(`{}`)
	}
	const q = `UPDATE artifacts SET metadata = $2 WHERE id = $1`
	res, err := r.db.ExecContext(ctx, q, id, metadata)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Artifact")
	}
	return nil
}

func (r *ArtifactRepo) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM artifacts WHERE id = $1`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Artifact")
	}
	return nil
}

func (r *ArtifactRepo) ListByTask(ctx context.Context, taskID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Artifact], error) {
	pg.Normalize()

	// Count
	const countQ = `SELECT COUNT(*) FROM artifacts WHERE task_id = $1`
	var totalCount int
	if err := r.db.GetContext(ctx, &totalCount, countQ, taskID); err != nil {
		return nil, err
	}

	// Data
	dataQ := fmt.Sprintf(
		`SELECT `+artifactSelectCols+` FROM artifacts WHERE task_id = $1 ORDER BY created_at DESC %s`,
		paginationClause(pg),
	)
	var rows []artifactRow
	if err := r.db.SelectContext(ctx, &rows, dataQ, taskID); err != nil {
		return nil, err
	}

	items := make([]domain.Artifact, len(rows))
	for i := range rows {
		items[i] = rows[i].toDomain()
	}

	return pagination.NewPage(items, totalCount, pg), nil
}
