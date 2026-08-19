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
	CreatedBy     uuid.UUID         `db:"created_by"`
	CreatedByType domain.ActorType  `db:"created_by_type"`
	UpdatedBy     *uuid.UUID        `db:"updated_by"`
	UpdatedByType *domain.ActorType `db:"updated_by_type"`
	Version       int64             `db:"version"`
	CreatedAt     time.Time         `db:"created_at"`
	UpdatedAt     time.Time         `db:"updated_at"`
	DeletedAt     *time.Time        `db:"deleted_at"`

	// Computed by documentEnrichedSelect, not stored — see actorNameExpr.
	CreatedByName *string `db:"created_by_name"`
	UpdatedByName *string `db:"updated_by_name"`
}

// documentSelectCols lists every stored column documentRow scans, qualified `d.`
// — every read here goes through the `documents d` alias so that one column list
// serves the plain reads and the ones that JOIN projects (both tables have
// created_at). See artifactSelectCols for why no query here uses `SELECT *`.
const documentSelectCols = `d.id, d.project_id, d.parent_id, d.slug, d.title, d.storage_key, d.position,
	d.created_by, d.created_by_type, d.updated_by, d.updated_by_type, d.version,
	d.created_at, d.updated_at, d.deleted_at`

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
		CreatedBy:     r.CreatedBy,
		CreatedByType: r.CreatedByType,
		UpdatedBy:     r.UpdatedBy,
		UpdatedByType: r.UpdatedByType,
		Version:       r.Version,
		CreatedAt:     r.CreatedAt,
		UpdatedAt:     r.UpdatedAt,
		DeletedAt:     r.DeletedAt,
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
	const q = `
		INSERT INTO documents (
			id, project_id, parent_id, slug, title, storage_key,
			position, created_by, created_by_type, updated_by, updated_by_type,
			version, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`
	_, err := r.db.ExecContext(ctx, q,
		doc.ID, doc.ProjectID, doc.ParentID, doc.Slug, doc.Title, doc.StorageKey,
		doc.Position, doc.CreatedBy, doc.CreatedByType, doc.UpdatedBy, doc.UpdatedByType,
		doc.Version, doc.CreatedAt, doc.UpdatedAt,
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

// MutateLocked is the one write path for an existing document, and the only one:
// it holds a row-level lock on the document for the whole of mutate, then
// persists the mutable columns and bumps version by one.
//
// The lock is what makes a document body safe to change at all. The body is not
// a column — it is an object in S3 addressed by storage_key — so every body
// write is a read-modify-write across two stores that no single statement can
// make atomic. Two writers running that concurrently is exactly the incident
// this exists to prevent: both read the same body, both upload, and the second
// upload erases the first with nothing anywhere reporting a failure. Serializing
// on the row means the second writer either sees the first writer's committed
// version (and its conditional check refuses) or waits behind it.
//
// Ordering inside the transaction is: lock, mutate, UPDATE, commit. mutate runs
// while the lock is held, which is why the object-storage write belongs inside
// it rather than around the call: an upload done before the lock is taken, or
// after it is released, is unserialized again and the lock bought nothing.
//
// mutate returning an error rolls the transaction back and the error is returned
// as-is, so a caller can raise a typed refusal (a version conflict, a validation
// error) from inside and have it reach the client unwrapped.
//
// Residual failure worth naming: if mutate uploads a body and the UPDATE or the
// COMMIT then fails, the stored object is ahead of the row — new body, unchanged
// version. That is a failed request that nevertheless changed content, reported
// as an error rather than as success. It is not the lost-update this method
// exists to stop, because no other writer could have interleaved: the lock was
// held throughout, so the next writer reads a consistent pair.
//
// Returns (nil, nil) when there is no live document with this id in this
// workspace — the same "no error, no row" shape as GetByIDInWorkspace, so the
// caller answers 404 without revealing whether the id exists in another tenant.
// On success it returns the document re-read through documentEnrichedSelect, so
// the caller gets the bumped version and the resolved display names together.
func (r *DocumentRepo) MutateLocked(
	ctx context.Context,
	id, workspaceID uuid.UUID,
	mutate func(locked *domain.Document) error,
) (*domain.Document, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // best-effort rollback after commit or on error

	// FOR UPDATE OF d: the row being locked is the document, not the project the
	// join brings in for the tenant check. Without the OF clause Postgres locks a
	// row in projects too, and every document write in a project would serialize
	// against every other one instead of only against writes to the same page.
	//
	// Not SKIP LOCKED and not NOWAIT: a concurrent writer is expected here and the
	// correct behaviour is to wait for it, then see what it committed.
	lockQ := documentEnrichedSelect + `
		  FROM documents d
		  JOIN projects p ON d.project_id = p.id
		 WHERE d.id = $1
		   AND d.deleted_at IS NULL
		   AND p.workspace_id = $2
		   FOR UPDATE OF d`
	var row documentRow
	if err = tx.GetContext(ctx, &row, lockQ, id, workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	// The version as the locked read found it, kept aside before mutate can touch
	// the copy it is handed.
	observedVersion := row.Version

	locked := row.toDomain()
	if mErr := mutate(&locked); mErr != nil {
		return nil, mErr
	}

	// `AND version = $8` is redundant while the lock above holds — nothing can
	// have changed the row in between. It is here for the day it does not: a
	// weakened or accidentally dropped FOR UPDATE turns a lost update into
	// silent data loss, which is the failure this whole file exists to prevent
	// and the one nobody notices. With the predicate, the same regression makes
	// the write match zero rows and fail loudly instead. Measured: removing FOR
	// UPDATE with this predicate in place makes concurrent appends error;
	// without it, they silently overwrite each other.
	const updateQ = `
		UPDATE documents
		   SET title = $2, parent_id = $3, position = $4, updated_at = $5,
		       updated_by = $6, updated_by_type = $7, version = version + 1
		 WHERE id = $1 AND deleted_at IS NULL AND version = $8`
	res, err := tx.ExecContext(ctx, updateQ, locked.ID, locked.Title, locked.ParentID, locked.Position,
		locked.UpdatedAt, locked.UpdatedBy, locked.UpdatedByType, observedVersion)
	if isDocumentSlugConflict(err) {
		return nil, apierror.Conflict("a document with this slug already exists in this location")
	}
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// The row was located and locked a few statements ago, so this is not the
		// ordinary "no such document": it means the row was deleted or its version
		// moved while this transaction held the lock, which the lock makes
		// impossible. Answered as a refusal rather than an ignored zero — a write
		// that matched nothing must never report success.
		return nil, apierror.Conflict("the document changed while this write was in flight")
	}

	if cErr := tx.Commit(); cErr != nil {
		return nil, cErr
	}

	// Re-read outside the transaction for the resolved display names and the
	// committed version, the same enrich-after-write the callers of Update relied
	// on. Falling back to the locked copy with version stepped on by hand keeps a
	// successful write reported as one even if this read fails — answering an
	// error here would tell the caller their edit was lost when it was not.
	enriched, getErr := r.GetByIDInWorkspace(ctx, locked.ID, workspaceID)
	if getErr != nil || enriched == nil {
		locked.Version++
		return &locked, nil
	}
	return enriched, nil
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
