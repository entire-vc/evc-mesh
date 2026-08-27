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

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// documentCommentRow is the DB row representation.
//
// The anchor is five columns here and one struct in the domain: the split is what
// lets Postgres enforce the states an anchor may be in (see the CHECK constraints
// in migrations/20260819100_create_document_comments.sql), which a single JSONB
// column could not.
type documentCommentRow struct {
	ID              uuid.UUID         `db:"id"`
	DocumentID      uuid.UUID         `db:"document_id"`
	ParentCommentID *uuid.UUID        `db:"parent_comment_id"`
	AuthorID        uuid.UUID         `db:"author_id"`
	AuthorType      domain.ActorType  `db:"author_type"`
	Body            string            `db:"body"`
	AnchorExact     *string           `db:"anchor_exact"`
	AnchorPrefix    *string           `db:"anchor_prefix"`
	AnchorSuffix    *string           `db:"anchor_suffix"`
	AnchorStart     *int              `db:"anchor_start"`
	AnchorEnd       *int              `db:"anchor_end"`
	ResolvedAt      *time.Time        `db:"resolved_at"`
	ResolvedBy      *uuid.UUID        `db:"resolved_by"`
	ResolvedByType  *domain.ActorType `db:"resolved_by_type"`
	CreatedAt       time.Time         `db:"created_at"`
	UpdatedAt       time.Time         `db:"updated_at"`
	DeletedAt       *time.Time        `db:"deleted_at"`

	// Computed, not stored — see actorNameExpr.
	AuthorName     *string `db:"author_name"`
	ResolvedByName *string `db:"resolved_by_name"`
}

// documentCommentSelectCols lists every stored column documentCommentRow scans,
// qualified `dc.` because every read joins at least documents. Nothing here uses
// `SELECT *` — see artifactSelectCols.
const documentCommentSelectCols = `dc.id, dc.document_id, dc.parent_comment_id, dc.author_id, dc.author_type, dc.body,
	dc.anchor_exact, dc.anchor_prefix, dc.anchor_suffix, dc.anchor_start, dc.anchor_end,
	dc.resolved_at, dc.resolved_by, dc.resolved_by_type,
	dc.created_at, dc.updated_at, dc.deleted_at`

// documentCommentEnrichedSelect adds the two display names, resolved through to
// the principal rather than copied at write time — the same read-time resolution
// commentEnrichedSelect uses, and for the same reason: a rename should show up
// everywhere that person ever wrote, not only where they write next.
var documentCommentEnrichedSelect = `SELECT ` + documentCommentSelectCols + `,
	` + actorNameExpr("dc.author_id", "dc.author_type", "author_name") + `,
	` + actorNameExpr("dc.resolved_by", "dc.resolved_by_type", "resolved_by_name")

func (r *documentCommentRow) toDomain() domain.DocumentComment {
	return domain.DocumentComment{
		ID:              r.ID,
		DocumentID:      r.DocumentID,
		ParentCommentID: r.ParentCommentID,
		AuthorID:        r.AuthorID,
		AuthorType:      r.AuthorType,
		Body:            r.Body,
		// derefString/nullIfEmpty (agent_repo.go) are the pair used on the way in
		// and out. Only the quote's presence distinguishes the anchor states, and
		// NewDocumentCommentAnchor reads that off the exact alone; prefix and suffix
		// are optional decoration, so NULL and '' are the same thing for them.
		Anchor: domain.NewDocumentCommentAnchor(
			derefString(r.AnchorExact),
			derefString(r.AnchorPrefix),
			derefString(r.AnchorSuffix),
			r.AnchorStart,
			r.AnchorEnd,
		),
		ResolvedAt:     r.ResolvedAt,
		ResolvedBy:     r.ResolvedBy,
		ResolvedByType: r.ResolvedByType,
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
		DeletedAt:      r.DeletedAt,
		AuthorName:     r.AuthorName,
		ResolvedByName: r.ResolvedByName,
	}
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

func (r *DocumentCommentRepo) Create(ctx context.Context, comment *domain.DocumentComment) error {
	const q = `
		INSERT INTO document_comments (
			id, document_id, parent_comment_id, author_id, author_type, body,
			anchor_exact, anchor_prefix, anchor_suffix, anchor_start, anchor_end,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`

	var exact, prefix, suffix *string
	var start, end *int
	if comment.Anchor != nil {
		// '' is stored as NULL so that "never anchored" is one value in the column
		// rather than two, and so a quoteless anchor satisfies
		// chk_document_comments_anchor_neighbourhood instead of tripping it.
		exact = nullIfEmpty(comment.Anchor.Exact)
		prefix = nullIfEmpty(comment.Anchor.Prefix)
		suffix = nullIfEmpty(comment.Anchor.Suffix)
		start, end = comment.Anchor.Start, comment.Anchor.End
	}

	// The resolution triple is deliberately absent from the column list: a comment
	// cannot be born resolved, and leaving the columns to their NULL default is
	// what makes that true of every row rather than of every caller who remembered.
	_, err := r.db.ExecContext(ctx, q,
		comment.ID, comment.DocumentID, comment.ParentCommentID,
		comment.AuthorID, comment.AuthorType, comment.Body,
		exact, prefix, suffix, start, end,
		comment.CreatedAt, comment.UpdatedAt,
	)
	return err
}

func (r *DocumentCommentRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.DocumentComment, error) {
	q := documentCommentEnrichedSelect + `
		  FROM document_comments dc
		 WHERE dc.id = $1 AND dc.deleted_at IS NULL`
	var row documentCommentRow
	if err := r.db.GetContext(ctx, &row, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	c := row.toDomain()
	return &c, nil
}

// GetByIDInWorkspace returns the comment only when its document's project belongs
// to workspaceID. Returns nil (no error) otherwise, so callers answer 404 without
// telling the caller whether the id exists in some other tenant.
//
// The document's own deleted_at is checked too: a comment on a deleted page is not
// reachable, or deleting a document would leave its discussion answering.
func (r *DocumentCommentRepo) GetByIDInWorkspace(ctx context.Context, id, workspaceID uuid.UUID) (*domain.DocumentComment, error) {
	q := documentCommentEnrichedSelect + `
		  FROM document_comments dc
		  JOIN documents d ON dc.document_id = d.id AND d.deleted_at IS NULL
		  JOIN projects p ON d.project_id = p.id
		 WHERE dc.id = $1
		   AND dc.deleted_at IS NULL
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

// Update writes the whole of what is mutable: the body and the resolution triple.
//
// The anchor is not in the SET list on purpose. It records what the comment was
// written about, and letting an edit move it would let a comment be relabelled
// onto text its author never read. Re-anchoring after the document changes is a
// different operation on different evidence, and when it arrives it gets its own
// method rather than riding in on a body edit.
func (r *DocumentCommentRepo) Update(ctx context.Context, comment *domain.DocumentComment) error {
	const q = `
		UPDATE document_comments
		   SET body = $2, resolved_at = $3, resolved_by = $4, resolved_by_type = $5, updated_at = $6
		 WHERE id = $1 AND deleted_at IS NULL`
	res, err := r.db.ExecContext(ctx, q,
		comment.ID, comment.Body,
		comment.ResolvedAt, comment.ResolvedBy, comment.ResolvedByType,
		comment.UpdatedAt,
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

// SoftDelete stamps deleted_at on the comment and on its replies in the same
// statement.
//
// The replies go with it for the same reason a document's children do: ON DELETE
// CASCADE covers only the hard-delete path, which no endpoint takes, and a reply
// left behind is an answer with no question — visible in the listing, attached to
// a parent id that resolves to nothing.
//
// Threads are one level deep by construction (the service refuses a reply to a
// reply), so one JOIN reaches every descendant there can be; a recursive CTE here
// would only describe a shape the writes cannot produce.
func (r *DocumentCommentRepo) SoftDelete(ctx context.Context, id uuid.UUID, at time.Time) error {
	const q = `
		UPDATE document_comments
		   SET deleted_at = $2, updated_at = $2
		 WHERE (id = $1 OR parent_comment_id = $1) AND deleted_at IS NULL`
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

// resolvedThreadIsHidden excludes a comment whose THREAD is resolved, not merely
// whose own row is.
//
// COALESCE(parent_comment_id, id) is the thread's root: itself for a top-level
// comment, its parent for a reply. Filtering on dc.resolved_at alone would leave
// the replies of a resolved thread on screen — answers to a question the reader
// can no longer see — which is the bug this predicate exists to not have.
const resolvedThreadIsHidden = `NOT EXISTS (
		SELECT 1 FROM document_comments root
		 WHERE root.id = COALESCE(dc.parent_comment_id, dc.id)
		   AND root.resolved_at IS NOT NULL
	)`

// ListByDocument returns a page of the document's live comments, oldest first.
//
// Flat and chronological, replies included, exactly like CommentRepo.ListByTask:
// the thread structure is one parent pointer and the client groups on it. Ordering
// the page by thread instead would need the root's timestamp on every row, and
// would make a page boundary able to split a thread in a way the client could not
// reassemble from one page.
func (r *DocumentCommentRepo) ListByDocument(
	ctx context.Context,
	documentID uuid.UUID,
	filter repository.DocumentCommentFilter,
	pg pagination.Params,
) (*pagination.Page[domain.DocumentComment], error) {
	pg.Normalize()

	where := `WHERE dc.document_id = $1 AND dc.deleted_at IS NULL`
	if !filter.IncludeResolved {
		where += ` AND ` + resolvedThreadIsHidden
	}

	countQ := fmt.Sprintf(`SELECT COUNT(*) FROM document_comments dc %s`, where)
	var totalCount int
	if err := r.db.GetContext(ctx, &totalCount, countQ, documentID); err != nil {
		return nil, err
	}

	// id is the tiebreak so a page boundary cannot repeat or skip a comment when
	// two share a created_at — two replies posted in the same millisecond is not
	// hypothetical when an agent writes them.
	dataQ := fmt.Sprintf(
		documentCommentEnrichedSelect+`
		  FROM document_comments dc
		  %s
		  ORDER BY dc.created_at ASC, dc.id ASC %s`,
		where, paginationClause(pg),
	)
	var rows []documentCommentRow
	if err := r.db.SelectContext(ctx, &rows, dataQ, documentID); err != nil {
		return nil, err
	}

	items := make([]domain.DocumentComment, len(rows))
	for i := range rows {
		items[i] = rows[i].toDomain()
	}

	return pagination.NewPage(items, totalCount, pg), nil
}

// ListAnchorsByDocument returns the anchor of every live comment on the document
// that has one.
//
// anchor_exact IS NOT NULL is the whole filter, and it is the definition of
// "anchored" the schema uses: a comment on the document as a whole, and a reply
// inheriting its parent's anchor, both have no quote and so have nothing to
// re-find. Resolved threads are included on purpose — resolving a conversation
// says nothing about whether its offsets still describe the text, and skipping
// them would leave exactly the rows nobody looks at pointing at the wrong words.
func (r *DocumentCommentRepo) ListAnchorsByDocument(ctx context.Context, documentID uuid.UUID) ([]repository.DocumentCommentAnchorRow, error) {
	const q = `
		SELECT dc.id, dc.anchor_exact, dc.anchor_prefix, dc.anchor_suffix, dc.anchor_start, dc.anchor_end
		  FROM document_comments dc
		 WHERE dc.document_id = $1
		   AND dc.deleted_at IS NULL
		   AND dc.anchor_exact IS NOT NULL
		 ORDER BY dc.created_at ASC, dc.id ASC`

	var rows []repository.DocumentCommentAnchorRow
	if err := r.db.SelectContext(ctx, &rows, q, documentID); err != nil {
		return nil, err
	}
	return rows, nil
}

// UpdateAnchorPositions writes the whole document's re-anchored offsets in one
// statement.
//
// UPDATE ... FROM (VALUES ...) rather than a loop of UPDATEs: the pass either
// describes the body that was just written or it does not, and a loop that dies
// half-way through leaves a document whose comments disagree about which
// revision they are anchored to — a state no reader could detect and no later
// pass would repair, because every row would look individually plausible.
//
// updated_at is deliberately NOT touched. Moving an offset is bookkeeping about
// where the text went, not an edit of the comment; bumping it would make every
// autosave look like somebody had just revised a hundred comments.
func (r *DocumentCommentRepo) UpdateAnchorPositions(ctx context.Context, positions []repository.DocumentCommentAnchorPosition) error {
	if len(positions) == 0 {
		return nil
	}

	values := make([]string, 0, len(positions))
	args := make([]any, 0, len(positions)*5)
	for i, p := range positions {
		base := i * 5
		values = append(values, fmt.Sprintf("($%d::uuid, $%d::text, $%d::text, $%d::int, $%d::int)",
			base+1, base+2, base+3, base+4, base+5))
		args = append(args, p.ID, nullIfEmpty(p.Prefix), nullIfEmpty(p.Suffix), p.Start, p.End)
	}

	q := // nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- strings.Join(values, ", ") below only concatenates literal "$N::type" placeholder tuples (built from the loop index above); every actual value travels as a bound arg via ExecContext, none are interpolated
		`
		UPDATE document_comments dc
		   SET anchor_prefix = v.prefix,
		       anchor_suffix = v.suffix,
		       anchor_start  = v.start,
		       anchor_end    = v.end
		  FROM (VALUES ` + strings.Join(values, ", ") + `) AS v(id, prefix, suffix, start, "end")
		 WHERE dc.id = v.id AND dc.deleted_at IS NULL`

	_, err := r.db.ExecContext(ctx, q, args...)
	return err
}
