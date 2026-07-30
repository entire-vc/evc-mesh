package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// vcsLinkRow is the DB row representation for vcs_links.
type vcsLinkRow struct {
	ID         uuid.UUID `db:"id"`
	TaskID     uuid.UUID `db:"task_id"`
	Provider   string    `db:"provider"`
	LinkType   string    `db:"link_type"`
	ExternalID string    `db:"external_id"`
	URL        string    `db:"url"`
	Title      string    `db:"title"`
	Status     string    `db:"status"`
	Metadata   []byte    `db:"metadata"`
	CreatedAt  time.Time `db:"created_at"`
}

func (r *vcsLinkRow) toDomain() domain.VCSLink {
	metadata := r.Metadata
	if metadata == nil {
		metadata = []byte("{}")
	}
	return domain.VCSLink{
		ID:         r.ID,
		TaskID:     r.TaskID,
		Provider:   domain.VCSProvider(r.Provider),
		LinkType:   domain.VCSLinkType(r.LinkType),
		ExternalID: r.ExternalID,
		URL:        r.URL,
		Title:      r.Title,
		Status:     domain.VCSLinkStatus(r.Status),
		Metadata:   metadata,
		CreatedAt:  r.CreatedAt,
	}
}

// VCSLinkRepo implements repository.VCSLinkRepository with PostgreSQL.
type VCSLinkRepo struct {
	db *sqlx.DB
}

// NewVCSLinkRepo creates a new VCSLinkRepo.
func NewVCSLinkRepo(db *sqlx.DB) *VCSLinkRepo {
	return &VCSLinkRepo{db: db}
}

// Create inserts a new VCS link.
func (r *VCSLinkRepo) Create(ctx context.Context, link *domain.VCSLink) error {
	const q = `
		INSERT INTO vcs_links (
			id, task_id, provider, link_type, external_id,
			url, title, status, metadata, created_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10
		)
	`
	metadata := link.Metadata
	if metadata == nil {
		metadata = []byte("{}")
	}
	_, err := r.db.ExecContext(ctx, q,
		link.ID, link.TaskID, string(link.Provider), string(link.LinkType), link.ExternalID,
		link.URL, link.Title, string(link.Status), metadata, link.CreatedAt,
	)
	return err
}

// GetByID retrieves a VCS link by its ID.
func (r *VCSLinkRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.VCSLink, error) {
	const q = `SELECT * FROM vcs_links WHERE id = $1`
	var row vcsLinkRow
	if err := r.db.GetContext(ctx, &row, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	link := row.toDomain()
	return &link, nil
}

// Delete removes a VCS link by its ID.
func (r *VCSLinkRepo) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM vcs_links WHERE id = $1`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("VCSLink")
	}
	return nil
}

// ListByTask returns all VCS links for a given task.
func (r *VCSLinkRepo) ListByTask(ctx context.Context, taskID uuid.UUID) ([]domain.VCSLink, error) {
	const q = `SELECT * FROM vcs_links WHERE task_id = $1 ORDER BY created_at ASC`
	var rows []vcsLinkRow
	if err := r.db.SelectContext(ctx, &rows, q, taskID); err != nil {
		return nil, err
	}
	result := make([]domain.VCSLink, len(rows))
	for i := range rows {
		result[i] = rows[i].toDomain()
	}
	return result, nil
}

// Upsert inserts a new VCS link or updates status/title/metadata on conflict
// of (task_id, provider, link_type, external_id). Used by the GitHub webhook
// orchestrator so subsequent deliveries for the same PR (opened → synchronize
// → closed) update the same row rather than failing the unique index.
//
// The UPDATE branch never touches id/created_at — RETURNING (xmax = 0)
// reports whether THIS statement inserted the row (xmax is unset on a fresh
// insert) or updated an existing one, and the returned id/created_at are
// scanned back into *link so the caller always holds the row that is
// actually on disk, never the id/created_at it happened to generate before
// finding out the row already existed (#b73171fa: the caller-supplied values
// used to be echoed back verbatim, making an update look like a freshly
// created duplicate).
func (r *VCSLinkRepo) Upsert(ctx context.Context, link *domain.VCSLink) (bool, error) {
	const q = `
		INSERT INTO vcs_links (
			id, task_id, provider, link_type, external_id,
			url, title, status, metadata, created_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10
		)
		ON CONFLICT (task_id, provider, link_type, external_id) DO UPDATE
		SET status = EXCLUDED.status,
		    title = EXCLUDED.title,
		    metadata = EXCLUDED.metadata,
		    url = EXCLUDED.url
		RETURNING id, created_at, (xmax = 0) AS inserted
	`
	metadata := link.Metadata
	if metadata == nil {
		metadata = []byte("{}")
	}
	var result struct {
		ID        uuid.UUID `db:"id"`
		CreatedAt time.Time `db:"created_at"`
		Inserted  bool      `db:"inserted"`
	}
	if err := r.db.GetContext(ctx, &result, q,
		link.ID, link.TaskID, string(link.Provider), string(link.LinkType), link.ExternalID,
		link.URL, link.Title, string(link.Status), metadata, link.CreatedAt,
	); err != nil {
		return false, err
	}
	link.ID = result.ID
	link.CreatedAt = result.CreatedAt
	return result.Inserted, nil
}

// ListByExternalID returns links matching (provider, link_type, external_id),
// newest first by created_at. Returns an empty slice when no row matches.
func (r *VCSLinkRepo) ListByExternalID(ctx context.Context, provider domain.VCSProvider, linkType domain.VCSLinkType, externalID string) ([]domain.VCSLink, error) {
	const q = `
		SELECT * FROM vcs_links
		WHERE provider = $1 AND link_type = $2 AND external_id = $3
		ORDER BY created_at DESC
	`
	var rows []vcsLinkRow
	if err := r.db.SelectContext(ctx, &rows, q, string(provider), string(linkType), externalID); err != nil {
		return nil, err
	}
	result := make([]domain.VCSLink, len(rows))
	for i := range rows {
		result[i] = rows[i].toDomain()
	}
	return result, nil
}

// Ensure VCSLinkRepo satisfies the repository.VCSLinkRepository interface.
var _ repository.VCSLinkRepository = (*VCSLinkRepo)(nil)
