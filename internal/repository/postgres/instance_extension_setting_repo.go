package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions/secretstore"
	"github.com/entire-vc/evc-mesh/pkg/encryption"
)

// InstanceExtensionSettingRepo implements repository.InstanceExtensionSettingRepository.
type InstanceExtensionSettingRepo struct {
	db *sqlx.DB
}

// NewInstanceExtensionSettingRepo creates a new InstanceExtensionSettingRepo.
func NewInstanceExtensionSettingRepo(db *sqlx.DB) *InstanceExtensionSettingRepo {
	return &InstanceExtensionSettingRepo{db: db}
}

const instanceSettingCols = `extension_id, settings, enabled, source, updated_by, updated_at`

func (r *InstanceExtensionSettingRepo) Get(ctx context.Context, extensionID string) (*domain.InstanceExtensionSetting, error) {
	var s domain.InstanceExtensionSetting
	err := r.db.GetContext(ctx, &s, `SELECT `+instanceSettingCols+` FROM instance_extension_settings WHERE extension_id = $1`, extensionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	plain, err := decryptSettings(s.Settings)
	if err != nil {
		return nil, fmt.Errorf("instance setting %q: %w", extensionID, err)
	}
	s.Settings = plain
	return &s, nil
}

func (r *InstanceExtensionSettingRepo) InsertIfAbsent(ctx context.Context, s *domain.InstanceExtensionSetting, secretKeys []string) (bool, error) {
	blob, err := encryptSettings(ctx, s.ExtensionID, s.Settings, secretKeys)
	if err != nil {
		return false, err
	}
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO instance_extension_settings (extension_id, settings, enabled, source, updated_by)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (extension_id) DO NOTHING`,
		s.ExtensionID, blob, s.Enabled, string(s.Source), s.UpdatedBy)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (r *InstanceExtensionSettingRepo) Upsert(ctx context.Context, s *domain.InstanceExtensionSetting, secretKeys []string) error {
	blob, err := encryptSettings(ctx, s.ExtensionID, s.Settings, secretKeys)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO instance_extension_settings (extension_id, settings, enabled, source, updated_by)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (extension_id) DO UPDATE SET
			settings = EXCLUDED.settings, enabled = EXCLUDED.enabled,
			source = EXCLUDED.source, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		s.ExtensionID, blob, s.Enabled, string(s.Source), s.UpdatedBy)
	return err
}

// encryptSettings encrypts the string values of secretKeys. An empty value is
// left empty (nothing to protect).
func encryptSettings(ctx context.Context, extensionID string, raw json.RawMessage, secretKeys []string) ([]byte, error) {
	m := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("settings must be a JSON object: %w", err)
		}
	}
	for _, k := range secretKeys {
		v, ok := m[k].(string)
		if !ok || v == "" || encryption.IsEncrypted(v) || strings.HasPrefix(v, secretstore.KeepPrefix) {
			continue
		}
		enc, err := secretstore.Default().Put(ctx, secretstore.Ref{ExtensionID: extensionID, Field: k}, v)
		if err != nil {
			return nil, fmt.Errorf("seal %q: %w", k, err)
		}
		m[k] = enc
	}
	return json.Marshal(m)
}

// decryptSettings decrypts every encrypted-prefixed string value.
func decryptSettings(raw json.RawMessage) (json.RawMessage, error) {
	m := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
	}
	for k, v := range m {
		str, ok := v.(string)
		if !ok || (!encryption.IsEncrypted(str) && !strings.HasPrefix(str, secretstore.KeepPrefix)) {
			continue
		}
		plain, reason := secretstore.Open(context.Background(), str)
		if reason != "" {
			return nil, fmt.Errorf("open %q: secret store degraded: %s", k, reason)
		}
		m[k] = plain
	}
	return json.Marshal(m)
}

// IsInstanceAdmin reports whether userID is an owner/admin of the instance's
// owner workspace — the oldest non-deleted workspace (the one created first
// at install). The instance has no separate admin role today; this is the
// decision recorded on card an internal ticket.
func (r *InstanceExtensionSettingRepo) IsInstanceAdmin(ctx context.Context, userID uuid.UUID) (bool, error) {
	var ok bool
	err := r.db.GetContext(ctx, &ok, `
		SELECT EXISTS (
			SELECT 1 FROM workspaces w
			WHERE w.id = (SELECT id FROM workspaces WHERE deleted_at IS NULL ORDER BY created_at ASC, id ASC LIMIT 1)
			  AND (w.owner_id = $1 OR EXISTS (
				SELECT 1 FROM workspace_members m
				WHERE m.workspace_id = w.id AND m.user_id = $1 AND m.role IN ('owner', 'admin')))
		)`, userID)
	return ok, err
}

// InstanceAdminTargets returns the instance's owner workspace and the users who
// administer it (its owner plus members with the owner/admin role): the people
// an instance-level notice is addressed to. Empty when no workspace exists.
func (r *InstanceExtensionSettingRepo) InstanceAdminTargets(ctx context.Context) (workspaceID uuid.UUID, adminIDs []uuid.UUID, err error) {
	var ws struct {
		ID      uuid.UUID  `db:"id"`
		OwnerID *uuid.UUID `db:"owner_id"`
	}
	err = r.db.GetContext(ctx, &ws, `SELECT id, owner_id FROM workspaces WHERE deleted_at IS NULL ORDER BY created_at ASC, id ASC LIMIT 1`)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, nil, nil
	}
	if err != nil {
		return uuid.Nil, nil, err
	}
	var ids []uuid.UUID
	if serr := r.db.SelectContext(ctx, &ids, `
		SELECT user_id FROM workspace_members
		WHERE workspace_id = $1 AND role IN ('owner', 'admin') AND user_id IS NOT NULL`, ws.ID); serr != nil {
		return uuid.Nil, nil, serr
	}
	if ws.OwnerID != nil {
		found := false
		for _, id := range ids {
			if id == *ws.OwnerID {
				found = true
			}
		}
		if !found {
			ids = append(ids, *ws.OwnerID)
		}
	}
	return ws.ID, ids, nil
}
