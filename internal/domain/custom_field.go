package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// FieldType represents the data type of a custom field definition.
type FieldType string

const (
	FieldTypeText        FieldType = "text"
	FieldTypeNumber      FieldType = "number"
	FieldTypeDate        FieldType = "date"
	FieldTypeDatetime    FieldType = "datetime"
	FieldTypeSelect      FieldType = "select"
	FieldTypeMultiselect FieldType = "multiselect"
	FieldTypeURL         FieldType = "url"
	FieldTypeEmail       FieldType = "email"
	FieldTypeCheckbox    FieldType = "checkbox"
	FieldTypeUserRef     FieldType = "user_ref"
	FieldTypeAgentRef    FieldType = "agent_ref"
	FieldTypeJSON        FieldType = "json"
)

// CustomFieldDefinition defines a custom field within a project.
// Fields are rendered in task detail views and optionally exposed to agents via MCP.
type CustomFieldDefinition struct {
	ID                uuid.UUID       `json:"id" db:"id"`
	ProjectID         uuid.UUID       `json:"project_id" db:"project_id"`
	Name              string          `json:"name" db:"name"`
	Slug              string          `json:"slug" db:"slug"`
	FieldType         FieldType       `json:"field_type" db:"field_type"`
	Description       string          `json:"description" db:"description"`
	Options           json.RawMessage `json:"options" db:"options"`
	DefaultValue      json.RawMessage `json:"default_value" db:"default_value"`
	IsRequired        bool            `json:"is_required" db:"is_required"`
	IsVisibleToAgents bool            `json:"is_visible_to_agents" db:"is_visible_to_agents"`
	Position          int             `json:"position" db:"position"`
	CreatedAt         time.Time       `json:"created_at" db:"created_at"`
}
