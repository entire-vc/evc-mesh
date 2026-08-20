package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// DocumentWatchRepo implements repository.DocumentWatchRepository against
// PostgreSQL.
type DocumentWatchRepo struct {
	db *sqlx.DB
}

// NewDocumentWatchRepo returns a new DocumentWatchRepo.
func NewDocumentWatchRepo(db *sqlx.DB) *DocumentWatchRepo {
	return &DocumentWatchRepo{db: db}
}

// Subscribe records a subscription.
//
// The whole design of this method is in the two ON CONFLICT branches. An
// explicit subscribe (force) un-mutes: pressing Watch after unwatching must
// work. An automatic subscribe — you created the page, you left a comment —
// deliberately does NOT touch `muted`, so a subscription somebody cancelled
// stays cancelled no matter how many comments they go on to write. Without that
// asymmetry the Watch toggle appears to do nothing for exactly the people the
// auto-subscription was meant to help.
func (r *DocumentWatchRepo) Subscribe(ctx context.Context, w domain.DocumentWatcher, force bool) error {
	const forced = `
		INSERT INTO document_watchers
			(document_id, watcher_id, watcher_kind, source, muted, created_at, updated_at)
		VALUES ($1, $2, $3, $4, FALSE, NOW(), NOW())
		ON CONFLICT (document_id, watcher_id) DO UPDATE
		   SET muted = FALSE,
		       source = EXCLUDED.source,
		       updated_at = NOW()
	`
	// The auto path keeps the earlier source too: somebody who created the page
	// and later commented on it is still, first and foremost, its author.
	const automatic = `
		INSERT INTO document_watchers
			(document_id, watcher_id, watcher_kind, source, muted, created_at, updated_at)
		VALUES ($1, $2, $3, $4, FALSE, NOW(), NOW())
		ON CONFLICT (document_id, watcher_id) DO NOTHING
	`

	q := automatic
	if force {
		q = forced
	}
	_, err := r.db.ExecContext(ctx, q, w.DocumentID, w.WatcherID, w.WatcherKind, w.Source)
	return err
}

// Unsubscribe mutes the principal's subscription on this document.
//
// It inserts a muted row when none exists rather than only updating: a watcher
// can be reached through a subscription they never explicitly created, and
// "unsubscribe from a row that is not there yet" has to be expressible or the
// button does nothing on the auto-subscribed case it exists for.
func (r *DocumentWatchRepo) Unsubscribe(ctx context.Context, documentID, watcherID uuid.UUID, watcherKind string) error {
	const q = `
		INSERT INTO document_watchers
			(document_id, watcher_id, watcher_kind, source, muted, created_at, updated_at)
		VALUES ($1, $2, $3, 'explicit', TRUE, NOW(), NOW())
		ON CONFLICT (document_id, watcher_id) DO UPDATE
		   SET muted = TRUE,
		       updated_at = NOW()
	`
	_, err := r.db.ExecContext(ctx, q, documentID, watcherID, watcherKind)
	return err
}

// GetState answers whether this principal is watching, and how many are.
func (r *DocumentWatchRepo) GetState(
	ctx context.Context,
	documentID, watcherID uuid.UUID,
	watcherKind string,
) (*domain.DocumentWatchState, error) {
	// Both halves in one round-trip. The count is over live rows only — a muted
	// row is a record of a refusal, not a subscriber.
	const q = `
		SELECT
			COALESCE((SELECT NOT muted FROM document_watchers
			           WHERE document_id = $1 AND watcher_id = $2 AND watcher_kind = $3), FALSE) AS watching,
			COALESCE((SELECT muted FROM document_watchers
			           WHERE document_id = $1 AND watcher_id = $2 AND watcher_kind = $3), FALSE) AS muted,
			COALESCE((SELECT source FROM document_watchers
			           WHERE document_id = $1 AND watcher_id = $2 AND watcher_kind = $3), '') AS source,
			(SELECT COUNT(*) FROM document_watchers
			  WHERE document_id = $1 AND muted = FALSE) AS watcher_count
	`
	var st domain.DocumentWatchState
	if err := r.db.QueryRowxContext(ctx, q, documentID, watcherID, watcherKind).
		Scan(&st.Watching, &st.Muted, &st.Source, &st.WatcherCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &domain.DocumentWatchState{}, nil
		}
		return nil, err
	}
	if !st.Watching {
		// Source describes a live subscription; reporting one for a muted or
		// absent row would have the UI explain a subscription that is not there.
		st.Source = ""
	}
	return &st, nil
}

// ListLiveWatchers returns the document's non-muted watchers.
func (r *DocumentWatchRepo) ListLiveWatchers(ctx context.Context, documentID uuid.UUID) ([]domain.DocumentWatcher, error) {
	const q = `
		SELECT document_id, watcher_id, watcher_kind, source, muted, created_at, updated_at
		  FROM document_watchers
		 WHERE document_id = $1 AND muted = FALSE
	`
	var out []domain.DocumentWatcher
	if err := r.db.SelectContext(ctx, &out, q, documentID); err != nil {
		return nil, err
	}
	return out, nil
}

// RecordChange folds one edit into the open notice for (document, actor).
//
// This single statement is the coalescing. The partial unique index
// uq_document_change_notices_open covers only rows with dispatched_at IS NULL,
// so the upsert targets the OPEN notice and a dispatched one never blocks the
// next: an edit arriving a second after a notice was sent opens a fresh notice
// rather than reviving a delivered one.
//
// GREATEST/LEAST on the version span rather than plain assignment: two
// concurrent writers can reach this out of order, and a span that narrowed
// because a slower request landed later would describe less than actually
// happened.
func (r *DocumentWatchRepo) RecordChange(ctx context.Context, n domain.DocumentChangeNotice) error {
	const q = `
		INSERT INTO document_change_notices
			(document_id, workspace_id, actor_id, actor_kind, actor_name,
			 edit_count, title_changed, body_changed, from_version, to_version,
			 first_edit_at, last_edit_at)
		VALUES ($1, $2, $3, $4, $5, 1, $6, $7, $8, $9, NOW(), NOW())
		ON CONFLICT (document_id, actor_id, actor_kind) WHERE dispatched_at IS NULL
		DO UPDATE SET
			edit_count    = document_change_notices.edit_count + 1,
			title_changed = document_change_notices.title_changed OR EXCLUDED.title_changed,
			body_changed  = document_change_notices.body_changed  OR EXCLUDED.body_changed,
			from_version  = LEAST(document_change_notices.from_version, EXCLUDED.from_version),
			to_version    = GREATEST(document_change_notices.to_version, EXCLUDED.to_version),
			actor_name    = CASE WHEN EXCLUDED.actor_name <> '' THEN EXCLUDED.actor_name
			                     ELSE document_change_notices.actor_name END,
			last_edit_at  = NOW()
	`
	_, err := r.db.ExecContext(ctx, q,
		n.DocumentID, n.WorkspaceID, n.ActorID, n.ActorKind, n.ActorName,
		n.TitleChanged, n.BodyChanged, n.FromVersion, n.ToVersion,
	)
	return err
}

// ClaimPendingNotices takes ownership of notices whose actor has gone quiet.
//
// The claim and the read are one statement on purpose. Two API replicas run
// this sweeper on the same table; a SELECT followed by an UPDATE would let both
// read the same pending notice and send the subscriber two copies of the news
// the notice exists to send once. FOR UPDATE SKIP LOCKED lets the second
// replica move on to other notices instead of blocking behind the first.
//
// Stamping dispatched_at at claim time — before delivery, not after — is
// deliberate. It means a sweeper that dies mid-dispatch drops that notice
// rather than replaying it on the next tick: for a notification, one lost is a
// smaller harm than an unbounded loop of duplicates, and the row it leaves
// behind (recipients = 0, dispatch_error unset) is the trace that says which
// happened. FinishNotice fills in the rest.
func (r *DocumentWatchRepo) ClaimPendingNotices(
	ctx context.Context,
	quietBefore time.Time,
	limit int,
) ([]domain.DocumentChangeNotice, error) {
	if limit <= 0 {
		limit = 100
	}
	const q = `
		WITH claimed AS (
			SELECT id FROM document_change_notices
			 WHERE dispatched_at IS NULL
			   AND last_edit_at < $1
			 ORDER BY last_edit_at
			 LIMIT $2
			 FOR UPDATE SKIP LOCKED
		)
		UPDATE document_change_notices n
		   SET dispatched_at = NOW()
		  FROM claimed
		 WHERE n.id = claimed.id
		RETURNING n.id, n.document_id, n.workspace_id, n.actor_id, n.actor_kind, n.actor_name,
		          n.edit_count, n.title_changed, n.body_changed, n.from_version, n.to_version,
		          n.first_edit_at, n.last_edit_at, n.dispatched_at, n.dispatch_error, n.recipients
	`
	var out []domain.DocumentChangeNotice
	if err := r.db.SelectContext(ctx, &out, q, quietBefore, limit); err != nil {
		return nil, err
	}
	return out, nil
}

// FinishNotice records what the dispatch actually achieved.
//
// recipients = 0 with no error is a real and ordinary answer: nobody was
// watching. It is stored as one so that "nobody was listening" stays
// distinguishable from "we could not find out who was", which is the whole
// reason dispatch_error is a column instead of a log line.
func (r *DocumentWatchRepo) FinishNotice(ctx context.Context, id uuid.UUID, recipients int, dispatchErr string) error {
	const q = `
		UPDATE document_change_notices
		   SET recipients = $2,
		       dispatch_error = NULLIF($3, '')
		 WHERE id = $1
	`
	_, err := r.db.ExecContext(ctx, q, id, recipients, dispatchErr)
	return err
}
