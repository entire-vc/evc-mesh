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

	SourceKind        domain.DocumentSource `db:"source_kind"`
	SourceShare       *string               `db:"source_share"`
	SourcePath        *string               `db:"source_path"`
	SourceSHA256      *string               `db:"source_sha256"`
	SyncedAt          *time.Time            `db:"synced_at"`
	ExternalAuthor    *string               `db:"external_author"`
	EmbedSyncedSHA256 *string               `db:"embed_synced_sha256"`

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
	d.source_kind, d.source_share, d.source_path, d.source_sha256, d.synced_at, d.external_author,
	d.embed_synced_sha256`

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

		SourceKind:        r.SourceKind,
		SourceShare:       r.SourceShare,
		SourcePath:        r.SourcePath,
		SourceSHA256:      r.SourceSHA256,
		SyncedAt:          r.SyncedAt,
		ExternalAuthor:    r.ExternalAuthor,
		EmbedSyncedSHA256: r.EmbedSyncedSHA256,

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
		return apierror.DocumentSlugTaken()
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

	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	// A move records where the subtree used to be before the UPDATE rewrites
	// it, so the recorded paths are the ones references in the wild still use.
	// The row lock keeps a concurrent move of the same document from slipping
	// between this read and the write below.
	if aerr := recordPathAliasesOnMove(ctx, tx, doc); aerr != nil {
		return 0, aerr
	}

	q := `
		UPDATE documents
		   SET title = $2, parent_id = $3, position = $4, updated_at = $5,
		       updated_by = $6, updated_by_type = $7, slug = $9, version = ` + versionExpr + `
		 WHERE id = $1 AND deleted_at IS NULL
		   AND ($8::int IS NULL OR version = $8::int)
		RETURNING version`

	var newVersion int
	err = tx.GetContext(ctx, &newVersion, q, doc.ID, doc.Title, doc.ParentID, doc.Position,
		doc.UpdatedAt, doc.UpdatedBy, doc.UpdatedByType, expectedVersion, doc.Slug)
	if isDocumentSlugConflict(err) {
		return 0, apierror.DocumentSlugTaken()
	}
	if err == nil {
		if cerr := tx.Commit(); cerr != nil {
			return 0, cerr
		}
		return newVersion, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	// Nothing was written; drop the aliases recorded for a move that did not
	// happen before telling the two failure cases apart.
	_ = tx.Rollback()

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

// recordPathAliasesOnMove writes the current full slug path of doc and of each
// of its live descendants into document_path_aliases when doc.ParentID or
// doc.Slug differs from the stored value. It must run inside the move's transaction and
// before the UPDATE: the paths it records are computed from the tree as it is
// now, which after the UPDATE would already be the new one.
//
// A path an earlier move left behind is re-pointed (ON CONFLICT DO UPDATE): the
// most recent document to vacate a path is the one a reference to it most
// plausibly means. A path moves with parent_id and with the document's own slug
// (the rename of a folder moves the whole subtree below it).
func recordPathAliasesOnMove(ctx context.Context, tx *sqlx.Tx, doc *domain.Document) error {
	var stored struct {
		ParentID *uuid.UUID `db:"parent_id"`
		Slug     string     `db:"slug"`
	}
	err := tx.GetContext(ctx, &stored,
		`SELECT parent_id, slug FROM documents WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, doc.ID)
	if errors.Is(err, sql.ErrNoRows) {
		// Gone or deleted: the UPDATE will match nothing and report it.
		return nil
	}
	if err != nil {
		return err
	}
	if sameParent(stored.ParentID, doc.ParentID) && stored.Slug == doc.Slug {
		return nil
	}

	// up walks from the document to its root, prefixing each ancestor's slug,
	// so the row whose parent_id IS NULL carries the document's full path; down
	// then extends that path over the live subtree. project_id is pinned on
	// every row of the subtree walk, same rule as SubtreeInProject.
	const q = `
		WITH RECURSIVE up AS (
			SELECT d.parent_id, d.slug::text AS path
			  FROM documents d WHERE d.id = $1
			UNION ALL
			SELECT p.parent_id, p.slug || '/' || up.path
			  FROM documents p JOIN up ON p.id = up.parent_id
		), down AS (
			SELECT d.id, (SELECT path FROM up WHERE parent_id IS NULL) AS path
			  FROM documents d WHERE d.id = $1
			UNION ALL
			SELECT c.id, down.path || '/' || c.slug
			  FROM documents c JOIN down ON c.parent_id = down.id
			 WHERE c.project_id = $2 AND c.deleted_at IS NULL
		)
		INSERT INTO document_path_aliases (project_id, path, document_id)
		SELECT $2, path, id FROM down WHERE path IS NOT NULL
		ON CONFLICT (project_id, path) DO UPDATE
		   SET document_id = EXCLUDED.document_id, created_at = now()`
	_, err = tx.ExecContext(ctx, q, doc.ID, doc.ProjectID)
	return err
}

func sameParent(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// SetEmbedSyncedSHA256 stamps the embed pass's dedup marker: the source hash of
// the body whose image embeds are now present as attachments. Guarded to copies
// for the same belt-and-suspenders reason RefreshSyncedCopy is — an own document
// has no embeds to import, and a marker on one would be a claim about nothing.
func (r *DocumentRepo) SetEmbedSyncedSHA256(ctx context.Context, id uuid.UUID, sha256 string) error {
	const q = `
		UPDATE documents
		   SET embed_synced_sha256 = $2
		 WHERE id = $1 AND deleted_at IS NULL AND source_kind <> 'own'`
	res, err := r.db.ExecContext(ctx, q, id, sha256)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Document")
	}
	return nil
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

// GetByPathAlias resolves a former path recorded by a move — see
// repository.DocumentRepository.GetByPathAlias. The join to a live document is
// what makes a deleted document's aliases dead: the alias row survives a soft
// delete, but it no longer leads anywhere.
func (r *DocumentRepo) GetByPathAlias(ctx context.Context, projectID uuid.UUID, path string) (*domain.Document, error) {
	q := documentEnrichedSelect + `
		  FROM document_path_aliases a
		  JOIN documents d ON d.id = a.document_id
		 WHERE a.project_id = $1 AND a.path = $2
		   AND d.project_id = a.project_id AND d.deleted_at IS NULL`
	var row documentRow
	if err := r.db.GetContext(ctx, &row, q, projectID, path); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	d := row.toDomain()
	return &d, nil
}

// documentPathUpSQL builds the full slug path of every document in the
// "start" CTE (one column, id) by walking each one up to its root. The final
// row per document is the one whose parent_id IS NULL.
const documentPathUpSQL = `
	up AS (
		SELECT s.id AS doc_id, d.parent_id, d.slug::text AS path
		  FROM start s JOIN documents d ON d.id = s.id
		UNION ALL
		SELECT up.doc_id, p.parent_id, p.slug || '/' || up.path
		  FROM up JOIN documents p ON p.id = up.parent_id
	)`

// FindLiveBySlug — see repository.DocumentRepository.FindLiveBySlug.
func (r *DocumentRepo) FindLiveBySlug(ctx context.Context, projectID uuid.UUID, slug string, limit int) ([]repository.DocumentPathHit, error) {
	q := `
		WITH RECURSIVE start AS (
			SELECT id FROM documents
			 WHERE project_id = $1 AND slug = $2 AND deleted_at IS NULL
		), ` + documentPathUpSQL + `
		SELECT doc_id AS id, path FROM up WHERE parent_id IS NULL
		 ORDER BY path LIMIT $3`
	var hits []repository.DocumentPathHit
	if err := r.db.SelectContext(ctx, &hits, q, projectID, slug, limit); err != nil {
		return nil, err
	}
	return hits, nil
}

// PathOf — see repository.DocumentRepository.PathOf.
func (r *DocumentRepo) PathOf(ctx context.Context, id uuid.UUID) (string, error) {
	q := `
		WITH RECURSIVE start AS (
			SELECT id FROM documents WHERE id = $1 AND deleted_at IS NULL
		), ` + documentPathUpSQL + `
		SELECT path FROM up WHERE parent_id IS NULL`
	var path string
	if err := r.db.GetContext(ctx, &path, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return path, nil
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

// documentSortKeySQL is one level's sortable key, built the same way at the base
// case and inside the recursion: position shifted into an unsigned range (so a
// negative position — the column has no CHECK against one — still sorts
// correctly as text) then zero-padded to 10 digits, followed by the created_at
// tiebreak and finally the id, matching ListByProject's
// "position ASC, created_at ASC, id ASC" exactly.
const documentSortKeySQL = `lpad((d.position::bigint + 2147483648)::text, 10, '0')
		|| ':' || to_char(d.created_at, 'YYYY-MM-DD"T"HH24:MI:SS.US') || ':' || d.id::text`

// SubtreeInProject returns rootID and its live descendants within projectID, in
// deterministic depth-first order.
//
// The recursion accumulates sort_path, one key per level (see
// documentSortKeySQL), and the final ORDER BY sort_path is what makes the
// result depth-first: Postgres compares arrays element by element and treats a
// shorter array that is a prefix of a longer one as the lesser value, so a
// parent's one-element-shorter path always sorts immediately before its
// children's, and siblings — which share every element up to their own last
// one — sort against each other by that last element, the same
// (position, created_at, id) tiebreak every other listing here uses.
//
// project_id is filtered in BOTH arms of the recursion (base case and the
// recursive step), not once at the top — see the interface doc for why: a row
// whose parent_id points inside this tree but whose own project_id does not
// match projectID must never be able to join by riding the pointer.
func (r *DocumentRepo) SubtreeInProject(ctx context.Context, rootID, projectID uuid.UUID) ([]domain.Document, error) {
	q := `
		WITH RECURSIVE subtree AS (
			SELECT d.id, d.parent_id, ARRAY[` + documentSortKeySQL + `] AS sort_path
			  FROM documents d
			 WHERE d.id = $1 AND d.project_id = $2 AND d.deleted_at IS NULL
			UNION ALL
			SELECT d.id, d.parent_id, s.sort_path || (` + documentSortKeySQL + `)
			  FROM documents d
			  JOIN subtree s ON d.parent_id = s.id
			 WHERE d.project_id = $2 AND d.deleted_at IS NULL
		)
		` + documentEnrichedSelect + `
		  FROM documents d
		  JOIN subtree s ON s.id = d.id
		 ORDER BY s.sort_path`

	var rows []documentRow
	if err := r.db.SelectContext(ctx, &rows, q, rootID, projectID); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	items := make([]domain.Document, len(rows))
	for i := range rows {
		items[i] = rows[i].toDomain()
	}
	return items, nil
}

// GetBySourceInProject looks up a copy by (project_id, source_share,
// source_path) — see uq_documents_source. Only ever matches a non-'own' row:
// an 'own' document's source_share is always NULL, and NULL never equals a
// supplied string, so this cannot accidentally return one of those.
func (r *DocumentRepo) GetBySourceInProject(ctx context.Context, projectID uuid.UUID, sourceShare, sourcePath string) (*domain.Document, error) {
	q := documentEnrichedSelect + `
		  FROM documents d
		 WHERE d.project_id = $1 AND d.source_share = $2 AND d.source_path = $3 AND d.deleted_at IS NULL`
	var row documentRow
	if err := r.db.GetContext(ctx, &row, q, projectID, sourceShare, sourcePath); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	d := row.toDomain()
	return &d, nil
}

// ListLiveCopiesByShare returns every live copy in projectID that came from
// sourceShare — the input side of SyncMount's archive pass. Plain enriched
// rows, no pagination: one project's copies from one share is bounded by the
// share's own file count, the same bound SyncMount's file loop already walks.
func (r *DocumentRepo) ListLiveCopiesByShare(ctx context.Context, projectID uuid.UUID, sourceShare string) ([]domain.Document, error) {
	q := documentEnrichedSelect + `
		  FROM documents d
		 WHERE d.project_id = $1 AND d.source_share = $2 AND d.deleted_at IS NULL
		 ORDER BY d.source_path`
	var rows []documentRow
	if err := r.db.SelectContext(ctx, &rows, q, projectID, sourceShare); err != nil {
		return nil, err
	}
	out := make([]domain.Document, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].toDomain())
	}
	return out, nil
}

// CreateExternalCopy inserts a copy document — the five source_* /
// synced_at columns are named explicitly, unlike Create's INSERT list, because
// a copy is required to populate all of them (chk_documents_source_shape) and
// none of them has a default suitable for a copy the way 'own' is for
// source_kind.
func (r *DocumentRepo) CreateExternalCopy(ctx context.Context, doc *domain.Document) error {
	const q = `
		INSERT INTO documents (
			id, project_id, parent_id, slug, title, storage_key,
			position, created_by, created_by_type, updated_by, updated_by_type,
			created_at, updated_at, version,
			source_kind, source_share, source_path, source_sha256, synced_at, external_author
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14,
			$15, $16, $17, $18, $19, $20)
	`
	_, err := r.db.ExecContext(ctx, q,
		doc.ID, doc.ProjectID, doc.ParentID, doc.Slug, doc.Title, doc.StorageKey,
		doc.Position, doc.CreatedBy, doc.CreatedByType, doc.UpdatedBy, doc.UpdatedByType,
		doc.CreatedAt, doc.UpdatedAt, doc.Version,
		doc.SourceKind, doc.SourceShare, doc.SourcePath, doc.SourceSHA256, doc.SyncedAt, doc.ExternalAuthor,
	)
	if isDocumentSlugConflict(err) {
		return apierror.DocumentSlugTaken()
	}
	return err
}

// RefreshSyncedCopy stamps synced_at/source_sha256 as of this check, bumping
// version only when bumpVersion says the body was rewritten alongside it — see
// the interface doc comment for why that decision is not re-derived here.
//
// source_kind <> 'own' in the WHERE clause is a belt-and-suspenders check: an
// 'own' document should never reach this call, but if one somehow did, this
// refuses to stamp sync metadata onto a row the CHECK constraint requires to
// have none.
func (r *DocumentRepo) RefreshSyncedCopy(ctx context.Context, id uuid.UUID, sourceSHA256 string, syncedAt time.Time, bumpVersion bool) (int, error) {
	versionExpr := "version"
	if bumpVersion {
		versionExpr = "version + 1"
	}
	q := `
		UPDATE documents
		   SET source_sha256 = $2, synced_at = $3, updated_at = $3, version = ` + versionExpr + `
		 WHERE id = $1 AND deleted_at IS NULL AND source_kind <> 'own'
		RETURNING version`
	var newVersion int
	err := r.db.GetContext(ctx, &newVersion, q, id, sourceSHA256, syncedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, apierror.NotFound("Document")
		}
		return 0, err
	}
	return newVersion, nil
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
