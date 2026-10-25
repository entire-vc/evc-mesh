package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	// StatusSyncedAt is nullable: rows that predate the column, and rows
	// written by Create (no status stated), read NULL = never confirmed.
	StatusSyncedAt *time.Time `db:"status_synced_at"`
	// Pipeline columns are nullable: NULL = no pipeline reported yet.
	PipelineID        *int64     `db:"pipeline_id"`
	PipelineStatus    *string    `db:"pipeline_status"`
	PipelineUpdatedAt *time.Time `db:"pipeline_updated_at"`
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

		StatusSyncedAt: r.StatusSyncedAt,

		PipelineID:        r.PipelineID,
		PipelineStatus:    r.PipelineStatus,
		PipelineUpdatedAt: r.PipelineUpdatedAt,
	}
}

// vcsLinkSelectCols is every column vcsLinkRow scans, listed explicitly —
// see agentSelectCols in agent_repo.go for why `SELECT *` is unsafe: sqlx
// refuses to scan a column with no matching struct field, so an additive
// migration to this table breaks every read until redeployed.
const vcsLinkSelectCols = `id, task_id, provider, link_type, external_id, url, title, status, metadata, created_at, status_synced_at, pipeline_id, pipeline_status, pipeline_updated_at`

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
// the caller (an internal ticket defect 1).
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
// accidental duplicate, never silently touch an existing row" —
// merging record state onto an existing row is Upsert's job, reserved for a
// caller that states a status and therefore means to correct the record.
//
// Before this method checked identity by url, a corrected provider (the
// exact effect of an internal ticket's own fix in vcs_url.go) slipped straight past
// the (task_id, provider, link_type, external_id) unique index — different
// provider, no conflict — and silently inserted a second row for the same
// PR/MR (an internal ticket defect 3). The url-identity check below closes that gap
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

	// Every link of the task is locked and compared by url: external_id is
	// only the number, unique per project, so it cannot decide identity (and
	// no longer narrows the lock — the unique index used to be the only thing
	// stopping a second row for the same url under another external_id). A
	// task carries a handful of links, so the scan is cheap.
	const lockQ = `
		SELECT id, created_at, url FROM vcs_links
		WHERE task_id = $1 AND link_type = $2
		ORDER BY created_at ASC
		FOR UPDATE
	`
	var candidates []vcsLinkURLCandidate
	if err := tx.SelectContext(ctx, &candidates, lockQ, link.TaskID, string(link.LinkType)); err != nil {
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
// status=merged) so correcting a wrongly-inferred provider (an internal ticket: a
// self-hosted GitLab URL that was first recorded as provider=github) fixes
// the existing row instead of inserting a second one the done-evidence gate
// never looks at.
//
// Matching by url, not by (task_id, link_type, external_id), is deliberate:
// external_id is not a stable identity across a provider correction (a
// GitHub a PR and a GitLab a review share the string "14" but are different
// objects — an internal ticket defect 2), while the url is. Every link of the
// task is locked and compared by url, with no external_id prefilter.
//
// The DB unique index is (task_id, provider, link_type, external_id, md5(url)),
// so a caller-visible identity collision can still occur (racing with a
// concurrent insert, or a genuinely different link already owning the
// target (provider, external_id) pair) — that surfaces as a clear
// apierror.Conflict, never a raw driver error (an internal ticket defect 1).
//
// When the lock finds MORE THAN ONE existing row naming the same url — the
// exact shape of an internal ticket defect 1's repro, left behind by the old
// external_id-only matching — this method heals it: the oldest row (by
// created_at) survives and is updated, every newer row sharing that same
// url is deleted. This is a safe merge, not data loss: every deleted row
// was proven, by url, to be the SAME object as the row it merges into. A row
// naming a genuinely different url is never touched.
//
// The UPDATE branch never fabricates id/created_at — the caller always ends
// up holding the row that is actually on disk, never the id/created_at it
// happened to generate before finding out the row already existed
// (an internal ticket: the caller-supplied values used to be echoed back verbatim,
// making an update look like a freshly created duplicate).
//
// Both branches stamp status_synced_at = now() (database clock, so every
// writer agrees on one time source) and reflect it back into
// link.StatusSyncedAt: every Upsert caller states the status it is writing
// (a webhook delivery, or a manual re-link with an explicit status), so the
// write IS a confirmation of that status at that moment.
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
		WHERE task_id = $1 AND link_type = $2
		ORDER BY created_at ASC
		FOR UPDATE
	`
	var candidates []vcsLinkURLCandidate
	if err := tx.SelectContext(ctx, &candidates, lockQ, link.TaskID, string(link.LinkType)); err != nil {
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
				url, title, status, metadata, created_at, status_synced_at
			) VALUES (
				$1, $2, $3, $4, $5,
				$6, $7, $8, $9, $10, now()
			)
			RETURNING status_synced_at
		`
		var syncedAt time.Time
		if execErr := tx.QueryRowxContext(ctx, insertQ,
			link.ID, link.TaskID, string(link.Provider), string(link.LinkType), link.ExternalID,
			link.URL, link.Title, string(link.Status), metadata, link.CreatedAt,
		).Scan(&syncedAt); execErr != nil {
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
		link.StatusSyncedAt = &syncedAt
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
		SET provider = $1, external_id = $2, status = $3, title = $4, metadata = metadata || $5::jsonb, url = $6,
		    status_synced_at = now()
		WHERE id = $7
		RETURNING status_synced_at
	`
	var syncedAt time.Time
	if execErr := tx.QueryRowxContext(ctx, updateQ,
		string(link.Provider), link.ExternalID, string(link.Status), link.Title, metadata, link.URL, primary.ID,
	).Scan(&syncedAt); execErr != nil {
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
	link.StatusSyncedAt = &syncedAt
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

// ListOpenTasksWithMergedPR: see repository.VCSLinkRepository. Review-subject
// links (metadata role=review) name somebody else's work and do not count. The
// activity_log NOT EXISTS makes the shadow journal one entry per card. The rules
// EXISTS is a superset prefilter (project or workspace rule row with the flag) so
// projects without it never occupy the page; the service still reads the
// effective rules before journaling.
func (r *VCSLinkRepo) ListOpenTasksWithMergedPR(ctx context.Context, limit, offset int) ([]repository.MergedPROpenTask, error) {
	const q = `
		SELECT t.id AS task_id, t.project_id, m.url AS link_url, m.status_synced_at AS merged_at
		FROM tasks t
		JOIN task_statuses ts ON ts.id = t.status_id
		JOIN LATERAL (
		    SELECT l.url, COALESCE(l.status_synced_at, l.created_at) AS status_synced_at
		    FROM vcs_links l
		    WHERE l.task_id = t.id AND l.link_type = 'pr' AND l.status = 'merged'
		      AND COALESCE(l.metadata->>'role', '') <> 'review'
		    ORDER BY COALESCE(l.status_synced_at, l.created_at) DESC
		    LIMIT 1
		) m ON true
		WHERE t.deleted_at IS NULL
		  AND ts.category NOT IN ('done', 'cancelled')
		  AND NOT EXISTS (
		      SELECT 1 FROM vcs_links o
		      WHERE o.task_id = t.id AND o.link_type = 'pr' AND o.status = 'open'
		        AND COALESCE(o.metadata->>'role', '') <> 'review')
		  AND (
		      EXISTS (SELECT 1 FROM project_rules pr
		              WHERE pr.project_id = t.project_id AND pr.rule_type = 'workflow'
		                AND pr.config->'mid_pipeline'->>'close_on_merge' = 'true')
		      OR EXISTS (SELECT 1 FROM projects pj
		                 JOIN workspace_rules wr ON wr.workspace_id = pj.workspace_id AND wr.rule_type = 'workflow'
		                 WHERE pj.id = t.project_id
		                   AND wr.config->'mid_pipeline'->>'close_on_merge' = 'true'))
		  AND NOT EXISTS (
		      SELECT 1 FROM activity_log a
		      WHERE a.entity_id = t.id AND a.action = 'workflow.shadow_close_on_merge')
		ORDER BY m.status_synced_at ASC, t.id ASC
		LIMIT $1 OFFSET $2`
	var rows []struct {
		TaskID    uuid.UUID `db:"task_id"`
		ProjectID uuid.UUID `db:"project_id"`
		LinkURL   string    `db:"link_url"`
		MergedAt  time.Time `db:"merged_at"`
	}
	if err := r.db.SelectContext(ctx, &rows, q, limit, offset); err != nil {
		return nil, err
	}
	out := make([]repository.MergedPROpenTask, len(rows))
	for i, x := range rows {
		out[i] = repository.MergedPROpenTask{TaskID: x.TaskID, ProjectID: x.ProjectID, LinkURL: x.LinkURL, MergedAt: x.MergedAt}
	}
	return out, nil
}

// SetPipelineStatus records the latest pipeline of the link. A delivery for a
// pipeline older than the stored one is ignored (webhooks arrive out of
// order; GitLab pipeline ids only grow), while a later status of the same
// pipeline overwrites. The commit sha is stored with the status in one UPDATE,
// so the two can never describe different pipelines. Returns true when the row
// was written.
func (r *VCSLinkRepo) SetPipelineStatus(ctx context.Context, id uuid.UUID, pipelineID int64, status, sha string) (bool, error) {
	const q = `
		UPDATE vcs_links
		SET pipeline_id = $2, pipeline_status = $3, pipeline_updated_at = now(),
		    metadata = COALESCE(metadata, '{}'::jsonb) || jsonb_build_object('pipeline_sha', $4::text)
		WHERE id = $1 AND (pipeline_id IS NULL OR pipeline_id <= $2)
	`
	res, err := r.db.ExecContext(ctx, q, id, pipelineID, status, strings.TrimSpace(sha))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// MergeMetadata merges patch into metadata with jsonb ||, so keys the patch
// does not name (head_sha, role, manual_status_declaration) survive. A deployed
// patch naming the sha and environment already stored keeps the stored
// deployed.at; the comparison runs inside the row-locked UPDATE, so two
// concurrent reports of one deploy cannot replace the first timestamp.
func (r *VCSLinkRepo) MergeMetadata(ctx context.Context, taskID, id uuid.UUID, patch json.RawMessage) (*domain.VCSLink, error) {
	const q = `
		UPDATE vcs_links
		SET metadata = CASE
		    WHEN metadata->'deployed' ? 'at'
		     AND metadata->'deployed'->>'sha' = $3::jsonb->'deployed'->>'sha'
		     AND metadata->'deployed'->>'environment' = $3::jsonb->'deployed'->>'environment'
		    THEN jsonb_set(COALESCE(metadata, '{}'::jsonb) || $3::jsonb, '{deployed,at}', metadata->'deployed'->'at')
		    ELSE COALESCE(metadata, '{}'::jsonb) || $3::jsonb
		  END
		WHERE id = $1 AND task_id = $2 AND link_type = 'pr'
		RETURNING ` + vcsLinkSelectCols
	var row vcsLinkRow
	if err := r.db.GetContext(ctx, &row, q, id, taskID, []byte(patch)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	link := row.toDomain()
	return &link, nil
}

// Ensure VCSLinkRepo satisfies the repository.VCSLinkRepository interface.
var _ repository.VCSLinkRepository = (*VCSLinkRepo)(nil)

// WorkspaceProviderActivity counts the vcs_links of one provider across the
// workspace's projects and returns the latest sign of life of any of them
// (created, status synced or pipeline updated); last is nil when there are none.
func (r *VCSLinkRepo) WorkspaceProviderActivity(ctx context.Context, workspaceID uuid.UUID, provider string) (count int, last *time.Time, err error) {
	var row struct {
		N    int        `db:"n"`
		Last *time.Time `db:"last"`
	}
	err = r.db.GetContext(ctx, &row, `
		SELECT COUNT(*) AS n,
		       MAX(GREATEST(v.created_at, COALESCE(v.status_synced_at, v.created_at), COALESCE(v.pipeline_updated_at, v.created_at))) AS last
		FROM vcs_links v
		JOIN tasks t ON t.id = v.task_id
		JOIN projects p ON p.id = t.project_id
		WHERE p.workspace_id = $1 AND v.provider = $2`, workspaceID, provider)
	if err != nil {
		return 0, nil, fmt.Errorf("vcs link activity: %w", err)
	}
	return row.N, row.Last, nil
}
