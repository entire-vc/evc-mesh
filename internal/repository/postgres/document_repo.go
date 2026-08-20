package postgres

import (
	"context"
	"database/sql"
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
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// documentRow is the DB row representation. domain.Document.Body has no column —
// it is filled from object storage by the service layer.
type documentRow struct {
	ID            uuid.UUID         `db:"id"`
	ProjectID     uuid.UUID         `db:"project_id"`
	ParentID      *uuid.UUID        `db:"parent_id"`
	Slug          string            `db:"slug"`
	Title         string            `db:"title"`
	StorageKey    string            `db:"storage_key"`
	Position      int               `db:"position"`
	Version       int               `db:"version"`
	CreatedBy     uuid.UUID         `db:"created_by"`
	CreatedByType domain.ActorType  `db:"created_by_type"`
	UpdatedBy     *uuid.UUID        `db:"updated_by"`
	UpdatedByType *domain.ActorType `db:"updated_by_type"`
	CreatedAt     time.Time         `db:"created_at"`
	UpdatedAt     time.Time         `db:"updated_at"`
	DeletedAt     *time.Time        `db:"deleted_at"`

	SourceKind     domain.DocumentSource `db:"source_kind"`
	SourceShare    *string               `db:"source_share"`
	SourcePath     *string               `db:"source_path"`
	SourceSHA256   *string               `db:"source_sha256"`
	SyncedAt       *time.Time            `db:"synced_at"`
	ExternalAuthor *string               `db:"external_author"`

	// Computed by documentEnrichedSelect, not stored — see actorNameExpr.
	CreatedByName *string `db:"created_by_name"`
	UpdatedByName *string `db:"updated_by_name"`
}

// documentSelectCols lists every stored column documentRow scans, qualified `d.`
// — every read here goes through the `documents d` alias so that one column list
// serves the plain reads and the ones that JOIN projects (both tables have
// created_at). See artifactSelectCols for why no query here uses `SELECT *`.
const documentSelectCols = `d.id, d.project_id, d.parent_id, d.slug, d.title, d.storage_key, d.position, d.version,
	d.created_by, d.created_by_type, d.updated_by, d.updated_by_type, d.created_at, d.updated_at, d.deleted_at,
	d.source_kind, d.source_share, d.source_path, d.source_sha256, d.synced_at, d.external_author`

// documentEnrichedSelect is documentSelectCols plus the two display names, each
// resolved through to the agent or user that authored the change rather than read
// from a denormalized copy. It is what every read uses: "created by X, last
// updated by Y" is the point of the metadata, and a caller handed bare uuids would
// have to fan out to resolve them itself.
var documentEnrichedSelect = `SELECT ` + documentSelectCols + `,
	` + actorNameExpr("d.created_by", "d.created_by_type", "created_by_name") + `,
	` + actorNameExpr("d.updated_by", "d.updated_by_type", "updated_by_name")

func (r *documentRow) toDomain() domain.Document {
	return domain.Document{
		ID:            r.ID,
		ProjectID:     r.ProjectID,
		ParentID:      r.ParentID,
		Slug:          r.Slug,
		Title:         r.Title,
		StorageKey:    r.StorageKey,
		Position:      r.Position,
		Version:       r.Version,
		CreatedBy:     r.CreatedBy,
		CreatedByType: r.CreatedByType,
		UpdatedBy:     r.UpdatedBy,
		UpdatedByType: r.UpdatedByType,
		CreatedAt:     r.CreatedAt,
		UpdatedAt:     r.UpdatedAt,
		DeletedAt:     r.DeletedAt,

		SourceKind:     r.SourceKind,
		SourceShare:    r.SourceShare,
		SourcePath:     r.SourcePath,
		SourceSHA256:   r.SourceSHA256,
		SyncedAt:       r.SyncedAt,
		ExternalAuthor: r.ExternalAuthor,

		CreatedByName: r.CreatedByName,
		UpdatedByName: r.UpdatedByName,
	}
}

// DocumentRepo implements repository.DocumentRepository with PostgreSQL.
type DocumentRepo struct {
	db *sqlx.DB
}

// NewDocumentRepo creates a new DocumentRepo.
func NewDocumentRepo(db *sqlx.DB) *DocumentRepo {
	return &DocumentRepo{db: db}
}

func (r *DocumentRepo) Create(ctx context.Context, doc *domain.Document) error {
	// version is written explicitly rather than left to the column default so the
	// row and the domain object the caller keeps agree on it without a re-read: a
	// document that came back saying version 0 would be refused by its own first
	// conditional write.
	const q = `
		INSERT INTO documents (
			id, project_id, parent_id, slug, title, storage_key,
			position, created_by, created_by_type, updated_by, updated_by_type,
			created_at, updated_at, version
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`
	_, err := r.db.ExecContext(ctx, q,
		doc.ID, doc.ProjectID, doc.ParentID, doc.Slug, doc.Title, doc.StorageKey,
		doc.Position, doc.CreatedBy, doc.CreatedByType, doc.UpdatedBy, doc.UpdatedByType,
		doc.CreatedAt, doc.UpdatedAt, doc.Version,
	)
	if isDocumentSlugConflict(err) {
		return apierror.Conflict("a document with this slug already exists in this location")
	}
	return err
}

func (r *DocumentRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Document, error) {
	q := documentEnrichedSelect + ` FROM documents d WHERE d.id = $1 AND d.deleted_at IS NULL`
	var row documentRow
	if err := r.db.GetContext(ctx, &row, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	d := row.toDomain()
	return &d, nil
}

// GetByIDInWorkspace returns the document only when its project belongs to
// workspaceID. Returns nil (no error) otherwise, so callers answer 404 without
// telling the caller whether the id exists in some other tenant.
func (r *DocumentRepo) GetByIDInWorkspace(ctx context.Context, id, workspaceID uuid.UUID) (*domain.Document, error) {
	q := documentEnrichedSelect + `
		  FROM documents d
		  JOIN projects p ON d.project_id = p.id
		 WHERE d.id = $1
		   AND d.deleted_at IS NULL
		   AND p.workspace_id = $2`
	var row documentRow
	if err := r.db.GetContext(ctx, &row, q, id, workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	d := row.toDomain()
	return &d, nil
}

// Update writes the mutable metadata and, when asked, bumps the version — see
// repository.DocumentRepository.Update for the contract.
//
// The version predicate, the version bump and the field writes are one statement
// because that is the only arrangement in which the check means anything. A
// SELECT that read the version followed by an UPDATE that trusted it would leave
// the two writers of the 2026-08-19 incident both passing the check and both
// writing — the window is small, which is precisely why it would be found in
// production and not in a test.
//
// RETURNING version is what makes the mismatch reportable: on a conflict the
// UPDATE matches nothing, so a second read gets the version the row is actually
// at, which is what the caller needs in order to re-read and retry.
func (r *DocumentRepo) Update(ctx context.Context, doc *domain.Document, expectedVersion *int, bumpVersion bool) (int, error) {
	versionExpr := "version"
	if bumpVersion {
		versionExpr = "version + 1"
	}

	q := `
		UPDATE documents
		   SET title = $2, parent_id = $3, position = $4, updated_at = $5,
		       updated_by = $6, updated_by_type = $7, version = ` + versionExpr + `
		 WHERE id = $1 AND deleted_at IS NULL
		   AND ($8::int IS NULL OR version = $8::int)
		RETURNING version`

	var newVersion int
	err := r.db.GetContext(ctx, &newVersion, q, doc.ID, doc.Title, doc.ParentID, doc.Position,
		doc.UpdatedAt, doc.UpdatedBy, doc.UpdatedByType, expectedVersion)
	if isDocumentSlugConflict(err) {
		return 0, apierror.Conflict("a document with this slug already exists in this location")
	}
	if err == nil {
		return newVersion, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	// Nothing matched. Either the row is gone (or soft-deleted), which is a 404,
	// or it is alive at a different version, which is the conflict. The second
	// query is what tells those apart, and it only runs on the failure path.
	if expectedVersion == nil {
		return 0, apierror.NotFound("Document")
	}
	var current int
	switch curErr := r.db.GetContext(ctx, &current,
		`SELECT version FROM documents WHERE id = $1 AND deleted_at IS NULL`, doc.ID); {
	case errors.Is(curErr, sql.ErrNoRows):
		return 0, apierror.NotFound("Document")
	case curErr != nil:
		return 0, curErr
	}
	return current, repository.ErrDocumentVersionMismatch
}

// SoftDelete stamps deleted_at. The children go with it: ON DELETE CASCADE only
// covers hard deletes, so the descendants are stamped in the same statement —
// leaving them behind would strand a subtree with a deleted parent, invisible to
// every listing but still holding its slug.
//
// The whole subtree also gets the updated_by pair, not just the document that was
// asked for. Deleting them is a change to every one of those rows, made by one
// actor, and a restored child claiming its last editor was whoever touched it
// weeks ago would be the same lie as back-filling the column would have been.
func (r *DocumentRepo) SoftDelete(ctx context.Context, id uuid.UUID, at time.Time, by uuid.UUID, byType domain.ActorType) error {
	const q = `
		WITH RECURSIVE subtree AS (
			SELECT id FROM documents WHERE id = $1 AND deleted_at IS NULL
			UNION ALL
			SELECT d.id FROM documents d JOIN subtree s ON d.parent_id = s.id WHERE d.deleted_at IS NULL
		)
		UPDATE documents SET deleted_at = $2, updated_at = $2, updated_by = $3, updated_by_type = $4
		 WHERE id IN (SELECT id FROM subtree)`
	res, err := r.db.ExecContext(ctx, q, id, at, by, byType)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Document")
	}
	return nil
}

func (r *DocumentRepo) ListByProject(ctx context.Context, projectID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Document], error) {
	pg.Normalize()

	const countQ = `SELECT COUNT(*) FROM documents WHERE project_id = $1 AND deleted_at IS NULL`
	var totalCount int
	if err := r.db.GetContext(ctx, &totalCount, countQ, projectID); err != nil {
		return nil, err
	}

	// Ordered by the sibling order the tree renders in, with created_at as the
	// tiebreak so a page boundary cannot repeat or skip a row when several
	// documents share a position.
	//
	// The listing carries the display names too: the document tree shows a byline
	// per row, and resolving them here is two correlated subqueries against a
	// page of at most 200 rows, against one extra HTTP round-trip per row if the
	// client had to resolve the uuids itself.
	dataQ := fmt.Sprintf(
		documentEnrichedSelect+`
		   FROM documents d
		  WHERE d.project_id = $1 AND d.deleted_at IS NULL
		  ORDER BY d.position ASC, d.created_at ASC, d.id ASC %s`,
		paginationClause(pg),
	)
	var rows []documentRow
	if err := r.db.SelectContext(ctx, &rows, dataQ, projectID); err != nil {
		return nil, err
	}

	items := make([]domain.Document, len(rows))
	for i := range rows {
		items[i] = rows[i].toDomain()
	}

	return pagination.NewPage(items, totalCount, pg), nil
}

// GetByPathInProject walks a slug path down from the project's top level.
//
// One recursive CTE rather than one round-trip per segment: the depth is small
// but the number of path lookups is not — an agent addresses a document by path
// on every single access, which is the whole reason paths exist here.
//
// It is unambiguous at every level because uq_documents_sibling_slug and
// uq_documents_root_slug make a slug unique among LIVE siblings, so each step of
// the walk matches at most one row. Deleted rows are excluded at every level, not
// just the last: a deleted document is not a step you can walk through, and its
// slug is already free for a sibling to take.
//
// The second return value is how far the walk got. A caller that only knows "not
// found" can say nothing useful; a caller that knows the path resolved three
// segments deep can name the segment that failed, which is the difference between
// a usable error and a shrug.
func (r *DocumentRepo) GetByPathInProject(ctx context.Context, projectID uuid.UUID, segments []string) (*domain.Document, int, error) {
	if len(segments) == 0 {
		return nil, 0, nil
	}

	// walk holds one row per segment successfully matched, so its deepest row is
	// both how far the path resolved and — when that depth is the whole path — the
	// document named.
	const walkQ = `
		WITH RECURSIVE seg(slug, depth) AS (
			SELECT s.slug, s.ord FROM unnest($2::text[]) WITH ORDINALITY AS s(slug, ord)
		), walk AS (
			SELECT d.id, 1 AS depth
			  FROM documents d
			  JOIN seg s ON s.depth = 1 AND d.slug = s.slug
			 WHERE d.project_id = $1 AND d.parent_id IS NULL AND d.deleted_at IS NULL
			UNION ALL
			SELECT d.id, w.depth + 1
			  FROM walk w
			  JOIN documents d ON d.parent_id = w.id
			  JOIN seg s ON s.depth = w.depth + 1 AND d.slug = s.slug
			 WHERE d.project_id = $1 AND d.deleted_at IS NULL
		)
		SELECT id, depth FROM walk ORDER BY depth DESC LIMIT 1`

	var found struct {
		ID    uuid.UUID `db:"id"`
		Depth int       `db:"depth"`
	}
	if err := r.db.GetContext(ctx, &found, walkQ, projectID, pq.Array(segments)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Not even the first segment matched.
			return nil, 0, nil
		}
		return nil, 0, err
	}
	if found.Depth < len(segments) {
		return nil, found.Depth, nil
	}

	// Resolved in full. Re-select the leaf through the enriched projection: the
	// walk carries ids only, and every read of a document is expected to carry its
	// bylines.
	doc, err := r.GetByID(ctx, found.ID)
	if err != nil {
		return nil, 0, err
	}
	if doc == nil {
		// Deleted between the two queries. A plain miss, at the last segment.
		return nil, len(segments) - 1, nil
	}
	return doc, found.Depth, nil
}

func (r *DocumentRepo) HasAncestor(ctx context.Context, docID, ancestorID uuid.UUID) (bool, error) {
	const q = `
		WITH RECURSIVE ancestors AS (
			SELECT parent_id FROM documents WHERE id = $1
			UNION ALL
			SELECT d.parent_id FROM documents d JOIN ancestors a ON d.id = a.parent_id
		)
		SELECT EXISTS (SELECT 1 FROM ancestors WHERE parent_id = $2)`
	var found bool
	if err := r.db.GetContext(ctx, &found, q, docID, ancestorID); err != nil {
		return false, err
	}
	return found, nil
}

// isDocumentSlugConflict reports whether err is the partial unique index on
// (project, parent, slug) refusing a duplicate among live siblings — a caller
// mistake worth a 409, not a 500. Constraint-named rather than code-only so an
// unrelated future unique index does not inherit the wording.
func isDocumentSlugConflict(err error) bool {
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) || pqErr.Code != "23505" {
		return false
	}
	return pqErr.Constraint == "uq_documents_sibling_slug" || pqErr.Constraint == "uq_documents_root_slug"
}

// ---------------------------------------------------------------------------
// Full-text search
// ---------------------------------------------------------------------------

// searchHeadlineWindow bounds the text ts_headline is asked to look at.
//
// ts_headline is linear in the text it is given and is run once per hit, so
// handing it a 5 MiB body times a page of results is seconds of CPU for a
// snippet. The window is the opening of the document; a match deeper than this
// still RANKS (the tsvector covers far more), it just cannot be quoted, and the
// hit says so via SnippetIsMatch rather than passing off the first sentence as
// the reason it matched.
const searchHeadlineWindow = 20000

// Markers chosen so nothing in a markdown body can be mistaken for them: a
// document may well contain <b> or **, and mistaking the author's own text for
// our highlight would mark the wrong words.
const (
	searchMarkStart = "\uE000"
	searchMarkEnd   = "\uE001"
)

// SetSearchText stores the copy of the body the index is built from. The trigger
// on documents recomputes search_vector from it.
func (r *DocumentRepo) SetSearchText(ctx context.Context, documentID uuid.UUID, text string) error {
	// updated_at is deliberately NOT touched: indexing is not an edit, and moving
	// the timestamp would reorder every list that sorts by it.
	const q = `UPDATE documents SET search_text = $2 WHERE id = $1 AND deleted_at IS NULL`
	_, err := r.db.ExecContext(ctx, q, documentID, text)
	return err
}

func (r *DocumentRepo) SearchInProject(
	ctx context.Context,
	projectID, workspaceID uuid.UUID,
	query string,
	limit int,
) ([]domain.DocumentSearchHit, error) {
	// The JOIN onto projects is the tenancy check and is not optional: a project
	// id is a caller-supplied value, and without this a caller who learned one
	// could read another tenant's documents through this endpoint.
	const q = `
		SELECT d.id, d.project_id, d.title, d.slug,
		       ts_headline('simple', left(coalesce(d.search_text, ''), $5), tsq,
		                   'StartSel=' || $6 || ',StopSel=' || $7 ||
		                   ',MaxWords=18,MinWords=6,ShortWord=2,MaxFragments=1') AS snippet,
		       ts_rank(d.search_vector, tsq) AS rank
		  FROM documents d
		  JOIN projects p ON d.project_id = p.id
		  CROSS JOIN LATERAL plainto_tsquery('simple', $3) AS tsq
		 WHERE d.project_id = $1
		   AND p.workspace_id = $2
		   AND d.deleted_at IS NULL
		   AND d.search_vector @@ tsq
		 ORDER BY rank DESC, d.updated_at DESC, d.id ASC
		 LIMIT $4`

	var rows []struct {
		domain.DocumentSearchHit
		Snippet string `db:"snippet"`
	}
	if err := r.db.SelectContext(ctx, &rows, q,
		projectID, workspaceID, query, limit,
		searchHeadlineWindow, searchMarkStart, searchMarkEnd,
	); err != nil {
		return nil, err
	}

	hits := make([]domain.DocumentSearchHit, len(rows))
	for i := range rows {
		hit := rows[i].DocumentSearchHit
		raw := rows[i].Snippet
		// A headline with no marker in it means the match is past the window, so
		// the text is the document's opening rather than the matched passage.
		hit.SnippetIsMatch = strings.Contains(raw, searchMarkStart)
		hit.Snippet = raw
		hits[i] = hit
	}
	return hits, nil
}
