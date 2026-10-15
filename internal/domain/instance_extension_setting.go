package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// InstanceSettingSource says who wrote an instance_extension_settings row.
type InstanceSettingSource string

const (
	// InstanceSettingSourceEnv: copied from MESH_* env by the startup migration.
	InstanceSettingSourceEnv InstanceSettingSource = "env"
	// InstanceSettingSourceAdmin: written through the instance-settings API.
	InstanceSettingSourceAdmin InstanceSettingSource = "admin"
)

// InstanceExtensionSetting is a row of instance_extension_settings. Secret
// fields inside Settings are stored encrypted (pkg/encryption) and returned
// decrypted by the repository.
type InstanceExtensionSetting struct {
	ExtensionID string                `db:"extension_id"`
	Settings    json.RawMessage       `db:"settings"`
	Enabled     bool                  `db:"enabled"`
	Source      InstanceSettingSource `db:"source"`
	UpdatedBy   *uuid.UUID            `db:"updated_by"`
	UpdatedAt   time.Time             `db:"updated_at"`
}
