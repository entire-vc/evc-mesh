package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

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

// vcsLinkSelectCols is every column vcsLinkRow scans, listed explicitly —
// see agentSelectCols in agent_repo.go for why `SELECT *` is unsafe: sqlx
// refuses to scan a column with no matching struct field, so an additive
// migration to this table breaks every read until redeployed.
const vcsLinkSelectCols = `id, task_id, provider, link_type, external_id, url, title, status, metadata, created_at`

// VCSLinkRepo implements repository.VCSLinkRepository with PostgreSQL.
type VCSLinkRepo struct {
	db *sqlx.DB
}

// NewVCSLinkRepo creates a new VCSLinkRepo.
func NewVCSLinkRepo(db *sqlx.DB) *VCSLinkRepo {
	return &VCSLinkRepo{db: db}
}

// vcsLinkURLCandidate is the minimal projection used to test whether a
// locked row names the same real-world object as an incoming link — see
// domain.NormalizeVCSURL.
type vcsLinkURLCandidate struct {
	ID        uuid.UUID `db:"id"`
	CreatedAt time.Time `db:"created_at"`
	URL       string    `db:"url"`
}

// isVCSLinkUniqueViolation reports whether err is idx_vcs_links_unique
// (task_id, provider, link_type, external_id) refusing a write — a genuine
// identity collision worth a clear 409, not a raw driver error leaking to
// the caller (#0fbed572 defect 1).
func isVCSLinkUniqueViolation(err error) bool {
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) || pqErr.Code != "23505" {
		return false
	}
	return pqErr.Constraint == "idx_vcs_links_unique"
}

// Create inserts a new VCS link — but refuses (409, no row touched or
// inserted) when a row already exists on this task for the SAME real-world
// url, regardless of what provider that row currently carries.
//
// This is the path a caller that omits status takes (vcsLinkService.Create's
// explicitStatus==false branch): its contract is "fail loudly on an
// accidental duplicate, never silently touch an existing row" (#b73171fa) —
// merging record state onto an existing row is Upsert's job, reserved for a
// caller that states a status and therefore means to correct the record.
//
// Before this method checked identity by url, a corrected provider (the
// exact effect of #0fbed572's own fix in vcs_url.go) slipped straight past
// the (task_id, provider, link_type, external_id) unique index — different
// provider, no conflict — and silently inserted a second row for the same
// PR/MR (#0fbed572 defect 3). The url-identity check below closes that gap
// without changing what an EXACT repeat (same provider too) does: that case
// still also hits the unique index below as a backstop.
func (r *VCSLinkRepo) Create(ctx context.Context, link *domain.VCSLink) error {
	metadata := link.Metadata
	if metadata == nil {
		metadata = []byte("{}")
	}
	targetURL := domain.NormalizeVCSURL(link.URL)

	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // best-effort rollback on error or panic

	// Filtered on external_id as a cheap prefilter, not as the identity
	// itself — every known corruption case (#0fbed572) shares external_id
	// across the mis-provider'd rows, so this narrows the lock to the rows
	// that can possibly match, and the url comparison below is what actually
	// decides identity.
	const lockQ = `
		SELECT id, created_at, url FROM vcs_links
		WHERE task_id = $1 AND link_type = $2 AND external_id = $3
		ORDER BY created_at ASC
		FOR UPDATE
	`
	var candidates []vcsLinkURLCandidate
	if err := tx.SelectContext(ctx, &candidates, lockQ, link.TaskID, string(link.LinkType), link.ExternalID); err != nil {
		return err
	}
	for _, c := range candidates {
		if domain.NormalizeVCSURL(c.URL) == targetURL {
			return apierror.Conflict(fmt.Sprintf(
				"a %s link for this url already exists on this task (id=%s) — re-link with an explicit "+
					"status to correct it instead of adding a new one",
				link.LinkType, c.ID,
			))
		}
	}

	const insertQ = `
		INSERT INTO vcs_links (
			id, task_id, provider, link_type, external_id,
			url, title, status, metadata, created_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10
		)
	`
	if _, execErr := tx.ExecContext(ctx, insertQ,
		link.ID, link.TaskID, string(link.Provider), string(link.LinkType), link.ExternalID,
		link.URL, link.Title, string(link.Status), metadata, link.CreatedAt,
	); execErr != nil {
		if isVCSLinkUniqueViolation(execErr) {
			return apierror.Conflict(fmt.Sprintf(
				"a %s %s link with external_id %q already exists on this task",
				link.Provider, link.LinkType, link.ExternalID,
			))
		}
		return execErr
	}
	return tx.Commit()
}

// GetByID retrieves a VCS link by its ID.
func (r *VCSLinkRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.VCSLink, error) {
	const q = `SELECT ` + vcsLinkSelectCols + ` FROM vcs_links WHERE id = $1`
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
	const q = `SELECT ` + vcsLinkSelectCols + ` FROM vcs_links WHERE task_id = $1 ORDER BY created_at ASC`
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

// Upsert inserts a new VCS link, or updates an existing one in place when a
// row already exists whose URL names the SAME real-world object (see
// domain.NormalizeVCSURL) — REGARDLESS of provider or external_id. Used by
// the GitHub webhook orchestrator so subsequent deliveries for the same PR
// (opened → synchronize → closed) update the same row rather than failing
// the unique index, and by the manual re-link path (add_vcs_link ...
// status=merged) so correcting a wrongly-inferred provider (#0fbed572: a
// self-hosted GitLab URL that was first recorded as provider=github) fixes
// the existing row instead of inserting a second one the done-evidence gate
// never looks at.
//
// Matching by url, not by (task_id, link_type, external_id), is deliberate:
// external_id is not a stable identity across a provider correction (a
// GitHub PR #14 and a GitLab MR !14 share the string "14" but are different
// objects — #0fbed572 defect 2), while the url is. Locking is still
// prefiltered by external_id (see the lockQ comment below) for the same
// reason Create's is — it is cheap and, in every case that matters here,
// external_id is what the mis-provider'd duplicate rows already share.
//
// The DB unique index is still (task_id, provider, link_type, external_id),
// so a caller-visible identity collision can still occur (racing with a
// concurrent insert, or a genuinely different link already owning the
// target (provider, external_id) pair) — that surfaces as a clear
// apierror.Conflict, never a raw driver error (#0fbed572 defect 1).
//
// When the lock finds MORE THAN ONE existing row naming the same url — the
// exact shape of #0fbed572 defect 1's repro, left behind by the old
// external_id-only matching — this method heals it: the oldest row (by
// created_at) survives and is updated, every newer row sharing that same
// url is deleted. This is a safe merge, not data loss: every deleted row
// was proven, by url, to be the SAME object as the row it merges into. A row
// naming a genuinely different url is never touched.
//
// The UPDATE branch never fabricates id/created_at — the caller always ends
// up holding the row that is actually on disk, never the id/created_at it
// happened to generate before finding out the row already existed
// (#b73171fa: the caller-supplied values used to be echoed back verbatim,
// making an update look like a freshly created duplicate).
func (r *VCSLinkRepo) Upsert(ctx context.Context, link *domain.VCSLink) (bool, error) {
	metadata := link.Metadata
	if metadata == nil {
		metadata = []byte("{}")
	}
	targetURL := domain.NormalizeVCSURL(link.URL)

	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // best-effort rollback on error or panic

	const lockQ = `
		SELECT id, created_at, url FROM vcs_links
		WHERE task_id = $1 AND link_type = $2 AND external_id = $3
		ORDER BY created_at ASC
		FOR UPDATE
	`
	var candidates []vcsLinkURLCandidate
	if err := tx.SelectContext(ctx, &candidates, lockQ, link.TaskID, string(link.LinkType), link.ExternalID); err != nil {
		return false, err
	}

	var matches []vcsLinkURLCandidate
	for _, c := range candidates {
		if domain.NormalizeVCSURL(c.URL) == targetURL {
			matches = append(matches, c)
		}
	}

	if len(matches) == 0 {
		const insertQ = `
			INSERT INTO vcs_links (
				id, task_id, provider, link_type, external_id,
				url, title, status, metadata, created_at
			) VALUES (
				$1, $2, $3, $4, $5,
				$6, $7, $8, $9, $10
			)
		`
		if _, execErr := tx.ExecContext(ctx, insertQ,
			link.ID, link.TaskID, string(link.Provider), string(link.LinkType), link.ExternalID,
			link.URL, link.Title, string(link.Status), metadata, link.CreatedAt,
		); execErr != nil {
			if isVCSLinkUniqueViolation(execErr) {
				return false, apierror.Conflict(fmt.Sprintf(
					"a %s %s link with external_id %q already exists on this task under a different url — "+
						"delete it first if this url should replace it",
					link.Provider, link.LinkType, link.ExternalID,
				))
			}
			return false, execErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return false, commitErr
		}
		return true, nil
	}

	// matches[0] is the oldest (ORDER BY created_at ASC) — the row every
	// prior reader has been pointed at, so it is the one that survives.
	primary := matches[0]
	for _, dup := range matches[1:] {
		if _, execErr := tx.ExecContext(ctx, `DELETE FROM vcs_links WHERE id = $1`, dup.ID); execErr != nil {
			return false, execErr
		}
	}

	const updateQ = `
		UPDATE vcs_links
		SET provider = $1, external_id = $2, status = $3, title = $4, metadata = $5, url = $6
		WHERE id = $7
	`
	if _, execErr := tx.ExecContext(ctx, updateQ,
		string(link.Provider), link.ExternalID, string(link.Status), link.Title, metadata, link.URL, primary.ID,
	); execErr != nil {
		if isVCSLinkUniqueViolation(execErr) {
			return false, apierror.Conflict(fmt.Sprintf(
				"cannot update this link to provider=%s external_id=%q — a different link on this task "+
					"already claims that identity",
				link.Provider, link.ExternalID,
			))
		}
		return false, execErr
	}
	link.ID = primary.ID
	link.CreatedAt = primary.CreatedAt
	if commitErr := tx.Commit(); commitErr != nil {
		return false, commitErr
	}
	return false, nil
}

// ListByExternalID returns links matching (provider, link_type, external_id),
// newest first by created_at. Returns an empty slice when no row matches.
func (r *VCSLinkRepo) ListByExternalID(ctx context.Context, provider domain.VCSProvider, linkType domain.VCSLinkType, externalID string) ([]domain.VCSLink, error) {
	const q = `
		SELECT ` + vcsLinkSelectCols + ` FROM vcs_links
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
