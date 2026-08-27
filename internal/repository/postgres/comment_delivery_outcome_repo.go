package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// CommentDeliveryOutcomeRepo implements repository.CommentDeliveryOutcomeRepository
// against PostgreSQL.
type CommentDeliveryOutcomeRepo struct {
	db *sqlx.DB
}

// NewCommentDeliveryOutcomeRepo returns a new CommentDeliveryOutcomeRepo.
func NewCommentDeliveryOutcomeRepo(db *sqlx.DB) *CommentDeliveryOutcomeRepo {
	return &CommentDeliveryOutcomeRepo{db: db}
}

// InsertBatch records the verdict for each addressed handle on a comment.
//
// ON CONFLICT DO UPDATE rather than DO NOTHING: editing a comment re-runs the
// mention pass, and a handle whose situation changed between the two writes
// should show its current verdict, not the first one ever recorded.
//
// The conflict target is the full (comment_id, recipient_slug, recipient_kind)
// key, not just the first two columns. A slug can resolve to both an agent
// and a user (task f4f47938) — two genuinely different recipients sharing one
// handle, not two opinions about one recipient. Conflicting on the pair alone
// let the second write upsert over the first, silently erasing whichever side
// was recorded first; recipient_kind is part of the row's identity, not just
// a value on it (migration 20260827001).
func (r *CommentDeliveryOutcomeRepo) InsertBatch(ctx context.Context, rows []domain.CommentDeliveryOutcome) error {
	if len(rows) == 0 {
		return nil
	}
	const q = `
		INSERT INTO comment_delivery_outcomes
			(comment_id, recipient_slug, recipient_id, recipient_kind,
			 outcome, reason, channel, recipient_presence, decided_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (comment_id, recipient_slug, recipient_kind) DO UPDATE SET
			recipient_id       = EXCLUDED.recipient_id,
			outcome            = EXCLUDED.outcome,
			reason             = EXCLUDED.reason,
			channel            = EXCLUDED.channel,
			recipient_presence = EXCLUDED.recipient_presence,
			decided_at         = EXCLUDED.decided_at
	`
	for _, o := range rows {
		if _, err := r.db.ExecContext(ctx, q,
			o.CommentID, o.RecipientSlug, o.RecipientID, o.RecipientKind,
			o.Outcome, o.Reason, o.Channel, o.RecipientPresence, o.DecidedAt,
		); err != nil {
			return err
		}
	}
	return nil
}

// MarkFailed downgrades one already-recorded verdict to failed with a named
// reason. It is how an asynchronous write error becomes visible: the initial
// verdict is written synchronously with the comment, and the dispatch that
// actually persists the event runs afterwards in its own goroutine.
//
// kind addresses the row alongside comment_id and slug because slug alone no
// longer identifies one row — a colliding slug can carry both an agent row
// and a user row for the same comment, and this downgrade must land on the
// one whose async write actually failed, not on whichever row the two-column
// address happened to reach first.
//
// The `outcome <> 'failed'` guard makes it idempotent under retries without
// letting a later success overwrite an earlier failure — nothing on this path
// ever upgrades a row back to delivered, deliberately: a store that rejected
// the event is a fact about this comment that a subsequent retry does not undo.
func (r *CommentDeliveryOutcomeRepo) MarkFailed(ctx context.Context, commentID uuid.UUID, slug, kind, reason string) error {
	const q = `
		UPDATE comment_delivery_outcomes
		   SET outcome = 'failed', reason = $4, decided_at = NOW()
		 WHERE comment_id = $1 AND recipient_slug = $2 AND recipient_kind = $3 AND outcome <> 'failed'
	`
	_, err := r.db.ExecContext(ctx, q, commentID, slug, kind, reason)
	return err
}

// ListByCommentIDs returns the verdicts for a set of comments, so a comment
// list can be rendered with its delivery record in one extra query rather than
// one per comment.
func (r *CommentDeliveryOutcomeRepo) ListByCommentIDs(
	ctx context.Context, commentIDs []uuid.UUID,
) (map[uuid.UUID][]domain.CommentDeliveryOutcome, error) {
	out := make(map[uuid.UUID][]domain.CommentDeliveryOutcome)
	if len(commentIDs) == 0 {
		return out, nil
	}

	const q = `
		SELECT comment_id, recipient_slug, recipient_id, recipient_kind,
		       outcome, reason, channel, recipient_presence, decided_at
		  FROM comment_delivery_outcomes
		 WHERE comment_id = ANY($1)
		 ORDER BY recipient_slug
	`
	ids := make([]uuid.UUID, len(commentIDs))
	copy(ids, commentIDs)

	var rows []domain.CommentDeliveryOutcome
	if err := r.db.SelectContext(ctx, &rows, q, pq.Array(ids)); err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.CommentID] = append(out[row.CommentID], row)
	}
	return out, nil
}
