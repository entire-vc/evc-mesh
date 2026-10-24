package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// DefaultThemeID is the theme every instance falls back to. It is not an
// installed extension: the tokens and views of the app itself are the theme.
const DefaultThemeID = "mesh-default"

// Instance journal actions of the theme backend.
const (
	InstanceJournalThemeSelected = "theme_selected"
	InstanceJournalThemeDeleted  = "theme_deleted"
)

// ThemeSelection is a stored theme choice, at instance level (instance_theme)
// or workspace level (workspace_theme). ThemeID nil means "inherit".
type ThemeSelection struct {
	ThemeID   *string         `json:"theme_id" db:"theme_id"`
	Settings  json.RawMessage `json:"settings" db:"settings"`
	Version   int             `json:"version" db:"version"`
	UpdatedBy *uuid.UUID      `json:"updated_by,omitempty" db:"updated_by"`
	UpdatedAt time.Time       `json:"updated_at" db:"updated_at"`
}
