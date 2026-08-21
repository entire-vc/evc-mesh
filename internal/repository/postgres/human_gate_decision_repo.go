package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// HumanGateDecisionRepo implements repository.HumanGateDecisionRepository.
type HumanGateDecisionRepo struct {
	db *sqlx.DB
}

// NewHumanGateDecisionRepo creates a new HumanGateDecisionRepo.
func NewHumanGateDecisionRepo(db *sqlx.DB) *HumanGateDecisionRepo {
	return &HumanGateDecisionRepo{db: db}
}

type humanGateDecisionRow struct {
	ID            uuid.UUID       `db:"id"`
	TaskID        uuid.UUID       `db:"task_id"`
	QuestionRef   *uuid.UUID      `db:"question_ref"`
	CanonicalKey  *string         `db:"canonical_key"`
	DecidedBy     uuid.UUID       `db:"decided_by"`
	Provenance    *string         `db:"provenance"`
	Channel       *string         `db:"channel"`
	Quote         *string         `db:"quote"`
	ChannelRef    json.RawMessage `db:"channel_ref"`
	RecordedBy    *uuid.UUID      `db:"recorded_by"`
	RevokesID     *uuid.UUID      `db:"revokes_id"`
	RevokedReason *string         `db:"revoked_reason"`
	CreatedAt     time.Time       `db:"created_at"`
	// RevocationCreatedAt/RevocationReason come from the LEFT JOIN against a
	// later row whose revokes_id = this row's id — nil unless this row is a
	// decision that has since been revoked.
	RevocationCreatedAt *time.Time `db:"revocation_created_at"`
	RevocationReason    *string    `db:"revocation_reason"`
}

func (r *humanGateDecisionRow) toDomain() domain.HumanGateDecision {
	d := domain.HumanGateDecision{
		ID:            r.ID,
		TaskID:        r.TaskID,
		QuestionRef:   r.QuestionRef,
		CanonicalKey:  r.CanonicalKey,
		DecidedBy:     r.DecidedBy,
		Quote:         r.Quote,
		ChannelRef:    r.ChannelRef,
		RecordedBy:    r.RecordedBy,
		RevokesID:     r.RevokesID,
		RevokedReason: r.RevokedReason,
		CreatedAt:     r.CreatedAt,
	}
	if r.Provenance != nil {
		p := domain.HumanGateProvenance(*r.Provenance)
		d.Provenance = &p
	}
	if r.Channel != nil {
		c := domain.HumanGateChannel(*r.Channel)
		d.Channel = &c
	}
	// A decision row's own revoked_reason column is always NULL by
	// construction (chk_hgd_revocation_has_reason only requires it on
	// revocation rows) — populate it, and RevokedAt, from the joined
	// revocation row when one exists.
	if r.RevocationCreatedAt != nil {
		d.RevokedAt = r.RevocationCreatedAt
		d.RevokedReason = r.RevocationReason
	}
	return d
}

// selectHumanGateDecisionQuery is shared by GetByID/FindLiveByRef/ListByTask:
// it LEFT JOINs the (at most one, by construction — nothing else may
// reference a given decision's id as its own revokes_id) revocation row, if
// any, so a decision's revoked state is read straight off this query instead
// of a second round trip.
const selectHumanGateDecisionQuery = `
	SELECT
		d.id, d.task_id, d.question_ref, d.canonical_key, d.decided_by,
		d.provenance, d.channel, d.quote, d.channel_ref, d.recorded_by,
		d.revokes_id, d.revoked_reason, d.created_at,
		r.created_at AS revocation_created_at, r.revoked_reason AS revocation_reason
	FROM human_gate_decisions d
	LEFT JOIN human_gate_decisions r ON r.revokes_id = d.id`

// Create inserts a new decision or revocation row. The append-only trigger
// rejects any UPDATE at the DB level, so this is the only write this
// repository ever performs against an existing row's identity.
func (r *HumanGateDecisionRepo) Create(ctx context.Context, d *domain.HumanGateDecision) error {
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now().UTC()
	}
	var provenance, channel *string
	if d.Provenance != nil {
		p := string(*d.Provenance)
		provenance = &p
	}
	if d.Channel != nil {
		c := string(*d.Channel)
		channel = &c
	}
	channelRef := d.ChannelRef
	if channelRef == nil {
		channelRef = json.RawMessage("null")
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO human_gate_decisions
			(id, task_id, question_ref, canonical_key, decided_by, provenance, channel,
			 quote, channel_ref, recorded_by, revokes_id, revoked_reason, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		d.ID, d.TaskID, d.QuestionRef, d.CanonicalKey, d.DecidedBy, provenance, channel,
		d.Quote, []byte(channelRef), d.RecordedBy, d.RevokesID, d.RevokedReason, d.CreatedAt,
	)
	return err
}

// GetByID returns one row, hydrated with revocation state if any.
func (r *HumanGateDecisionRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.HumanGateDecision, error) {
	var row humanGateDecisionRow
	err := r.db.GetContext(ctx, &row, selectHumanGateDecisionQuery+` WHERE d.id = $1`, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	d := row.toDomain()
	return &d, nil
}

// FindLiveByRef returns the most recent decision on taskID matching
// questionRef or canonicalKey that has NOT been revoked — the exact check
// enforceBlockingTriage's repeat-prevention (contract §6) needs before
// arming human_gate. At least one of questionRef/canonicalKey must be
// non-nil or this always returns nil (matching nothing is not "matching
// everything").
func (r *HumanGateDecisionRepo) FindLiveByRef(ctx context.Context, taskID uuid.UUID, questionRef *uuid.UUID, canonicalKey *string) (*domain.HumanGateDecision, error) {
	if questionRef == nil && canonicalKey == nil {
		return nil, nil
	}
	var row humanGateDecisionRow
	err := r.db.GetContext(ctx, &row, selectHumanGateDecisionQuery+`
		WHERE d.task_id = $1
		  AND d.revokes_id IS NULL
		  AND ((d.question_ref = $2 AND $2::uuid IS NOT NULL) OR (d.canonical_key = $3 AND $3::text IS NOT NULL))
		  AND r.id IS NULL
		ORDER BY d.created_at DESC
		LIMIT 1`,
		taskID, questionRef, canonicalKey,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	d := row.toDomain()
	return &d, nil
}

// ListByTask returns every row (decisions and revocations) for a task,
// newest first.
func (r *HumanGateDecisionRepo) ListByTask(ctx context.Context, taskID uuid.UUID) ([]domain.HumanGateDecision, error) {
	var rows []humanGateDecisionRow
	err := r.db.SelectContext(ctx, &rows, selectHumanGateDecisionQuery+`
		WHERE d.task_id = $1
		ORDER BY d.created_at DESC`, taskID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.HumanGateDecision, len(rows))
	for i, row := range rows {
		out[i] = row.toDomain()
	}
	return out, nil
}
