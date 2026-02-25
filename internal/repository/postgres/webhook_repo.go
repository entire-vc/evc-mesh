package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// webhookConfigRow is the DB row representation for webhook_configs.
type webhookConfigRow struct {
	ID            uuid.UUID      `db:"id"`
	WorkspaceID   uuid.UUID      `db:"workspace_id"`
	Name          string         `db:"name"`
	URL           string         `db:"url"`
	Secret        string         `db:"secret"`
	Events        pq.StringArray `db:"events"`
	IsActive      bool           `db:"is_active"`
	FailureCount  int            `db:"failure_count"`
	LastFailureAt *time.Time     `db:"last_failure_at"`
	LastSuccessAt *time.Time     `db:"last_success_at"`
	CreatedBy     uuid.UUID      `db:"created_by"`
	CreatedAt     time.Time      `db:"created_at"`
	UpdatedAt     time.Time      `db:"updated_at"`
}

func (r *webhookConfigRow) toDomain() domain.WebhookConfig {
	events := []string(r.Events)
	if events == nil {
		events = []string{}
	}
	return domain.WebhookConfig{
		ID:            r.ID,
		WorkspaceID:   r.WorkspaceID,
		Name:          r.Name,
		URL:           r.URL,
		Secret:        r.Secret,
		Events:        events,
		IsActive:      r.IsActive,
		FailureCount:  r.FailureCount,
		LastFailureAt: r.LastFailureAt,
		LastSuccessAt: r.LastSuccessAt,
		CreatedBy:     r.CreatedBy,
		CreatedAt:     r.CreatedAt,
		UpdatedAt:     r.UpdatedAt,
	}
}

// webhookDeliveryRow is the DB row representation for webhook_deliveries.
type webhookDeliveryRow struct {
	ID             uuid.UUID `db:"id"`
	WebhookID      uuid.UUID `db:"webhook_id"`
	EventType      string    `db:"event_type"`
	Payload        []byte    `db:"payload"`
	ResponseStatus *int      `db:"response_status"`
	ResponseBody   *string   `db:"response_body"`
	DurationMs     *int      `db:"duration_ms"`
	Success        bool      `db:"success"`
	Attempt        int       `db:"attempt"`
	CreatedAt      time.Time `db:"created_at"`
}

func (r *webhookDeliveryRow) toDomain() domain.WebhookDelivery {
	return domain.WebhookDelivery{
		ID:             r.ID,
		WebhookID:      r.WebhookID,
		EventType:      r.EventType,
		Payload:        r.Payload,
		ResponseStatus: r.ResponseStatus,
		ResponseBody:   r.ResponseBody,
		DurationMs:     r.DurationMs,
		Success:        r.Success,
		Attempt:        r.Attempt,
		CreatedAt:      r.CreatedAt,
	}
}

// WebhookRepo implements repository.WebhookRepository with PostgreSQL.
type WebhookRepo struct {
	db *sqlx.DB
}

// NewWebhookRepo creates a new WebhookRepo.
func NewWebhookRepo(db *sqlx.DB) *WebhookRepo {
	return &WebhookRepo{db: db}
}

// Create inserts a new webhook configuration.
func (r *WebhookRepo) Create(ctx context.Context, webhook *domain.WebhookConfig) error {
	const q = `
		INSERT INTO webhook_configs (
			id, workspace_id, name, url, secret, events,
			is_active, failure_count, last_failure_at, last_success_at,
			created_by, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10,
			$11, $12, $13
		)
	`
	_, err := r.db.ExecContext(ctx, q,
		webhook.ID, webhook.WorkspaceID, webhook.Name, webhook.URL, webhook.Secret,
		pq.Array(webhook.Events),
		webhook.IsActive, webhook.FailureCount, webhook.LastFailureAt, webhook.LastSuccessAt,
		webhook.CreatedBy, webhook.CreatedAt, webhook.UpdatedAt,
	)
	return err
}

// GetByID retrieves a webhook configuration by its ID.
func (r *WebhookRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.WebhookConfig, error) {
	const q = `SELECT * FROM webhook_configs WHERE id = $1`
	var row webhookConfigRow
	if err := r.db.GetContext(ctx, &row, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	wh := row.toDomain()
	return &wh, nil
}

// Update applies a partial update to a webhook configuration.
func (r *WebhookRepo) Update(ctx context.Context, id uuid.UUID, input domain.UpdateWebhookInput) (*domain.WebhookConfig, error) {
	existing, err := r.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, apierror.NotFound("Webhook")
	}

	if input.Name != nil {
		existing.Name = *input.Name
	}
	if input.URL != nil {
		existing.URL = *input.URL
	}
	if input.Events != nil {
		existing.Events = input.Events
	}
	if input.IsActive != nil {
		existing.IsActive = *input.IsActive
	}
	existing.UpdatedAt = time.Now()

	const q = `
		UPDATE webhook_configs
		SET name = $2, url = $3, events = $4, is_active = $5, updated_at = $6
		WHERE id = $1
	`
	_, err = r.db.ExecContext(ctx, q,
		id, existing.Name, existing.URL, pq.Array(existing.Events), existing.IsActive, existing.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return existing, nil
}

// Delete removes a webhook configuration by its ID.
func (r *WebhookRepo) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM webhook_configs WHERE id = $1`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Webhook")
	}
	return nil
}

// ListByWorkspace returns all webhook configurations for a workspace.
func (r *WebhookRepo) ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.WebhookConfig, error) {
	const q = `SELECT * FROM webhook_configs WHERE workspace_id = $1 ORDER BY created_at DESC`
	var rows []webhookConfigRow
	if err := r.db.SelectContext(ctx, &rows, q, workspaceID); err != nil {
		return nil, err
	}
	result := make([]domain.WebhookConfig, len(rows))
	for i := range rows {
		result[i] = rows[i].toDomain()
	}
	return result, nil
}

// ListActiveByEvent returns active webhook configurations that subscribe to the given event type.
func (r *WebhookRepo) ListActiveByEvent(ctx context.Context, workspaceID uuid.UUID, eventType string) ([]domain.WebhookConfig, error) {
	const q = `
		SELECT * FROM webhook_configs
		WHERE workspace_id = $1
		  AND is_active = true
		  AND $2 = ANY(events)
		ORDER BY created_at ASC
	`
	var rows []webhookConfigRow
	if err := r.db.SelectContext(ctx, &rows, q, workspaceID, eventType); err != nil {
		return nil, err
	}
	result := make([]domain.WebhookConfig, len(rows))
	for i := range rows {
		result[i] = rows[i].toDomain()
	}
	return result, nil
}

// IncrementFailure increments the failure counter and sets last_failure_at.
func (r *WebhookRepo) IncrementFailure(ctx context.Context, id uuid.UUID) error {
	const q = `
		UPDATE webhook_configs
		SET failure_count = failure_count + 1, last_failure_at = NOW(), updated_at = NOW()
		WHERE id = $1
	`
	_, err := r.db.ExecContext(ctx, q, id)
	return err
}

// ResetFailure resets the failure counter and sets last_success_at.
func (r *WebhookRepo) ResetFailure(ctx context.Context, id uuid.UUID) error {
	const q = `
		UPDATE webhook_configs
		SET failure_count = 0, last_success_at = NOW(), updated_at = NOW()
		WHERE id = $1
	`
	_, err := r.db.ExecContext(ctx, q, id)
	return err
}

// Deactivate marks a webhook as inactive (used after too many consecutive failures).
func (r *WebhookRepo) Deactivate(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE webhook_configs SET is_active = false, updated_at = NOW() WHERE id = $1`
	_, err := r.db.ExecContext(ctx, q, id)
	return err
}

// CreateDelivery records a webhook delivery attempt.
func (r *WebhookRepo) CreateDelivery(ctx context.Context, delivery *domain.WebhookDelivery) error {
	const q = `
		INSERT INTO webhook_deliveries (
			id, webhook_id, event_type, payload, response_status,
			response_body, duration_ms, success, attempt, created_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10
		)
	`
	_, err := r.db.ExecContext(ctx, q,
		delivery.ID, delivery.WebhookID, delivery.EventType, delivery.Payload, delivery.ResponseStatus,
		delivery.ResponseBody, delivery.DurationMs, delivery.Success, delivery.Attempt, delivery.CreatedAt,
	)
	return err
}

// ListDeliveries returns recent delivery records for a webhook, newest first.
func (r *WebhookRepo) ListDeliveries(ctx context.Context, webhookID uuid.UUID, limit int) ([]domain.WebhookDelivery, error) {
	const q = `
		SELECT * FROM webhook_deliveries
		WHERE webhook_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`
	var rows []webhookDeliveryRow
	if err := r.db.SelectContext(ctx, &rows, q, webhookID, limit); err != nil {
		return nil, err
	}
	result := make([]domain.WebhookDelivery, len(rows))
	for i := range rows {
		result[i] = rows[i].toDomain()
	}
	return result, nil
}

// Ensure WebhookRepo satisfies the repository.WebhookRepository interface.
var _ repository.WebhookRepository = (*WebhookRepo)(nil)
