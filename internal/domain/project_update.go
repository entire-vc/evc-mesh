package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// UpdateStatus represents the health status of a project update.
type UpdateStatus string

const (
	UpdateStatusOnTrack   UpdateStatus = "on_track"
	UpdateStatusAtRisk    UpdateStatus = "at_risk"
	UpdateStatusOffTrack  UpdateStatus = "off_track"
	UpdateStatusCompleted UpdateStatus = "completed"
)

// TextItem is a single bullet item inside highlights, blockers, or next_steps.
type TextItem struct {
	Text string `json:"text"`
}

// ProjectUpdateMetrics holds auto-populated task count metrics for an update.
type ProjectUpdateMetrics struct {
	TasksCompleted int `json:"tasks_completed"`
	TasksTotal     int `json:"tasks_total"`
	TasksInProgress int `json:"tasks_in_progress"`
}

// ProjectUpdate is a structured status report for a project at a point in time.
type ProjectUpdate struct {
	ID         uuid.UUID       `json:"id" db:"id"`
	ProjectID  uuid.UUID       `json:"project_id" db:"project_id"`
	Title      string          `json:"title" db:"title"`
	Status     UpdateStatus    `json:"status" db:"status"`
	Summary    string          `json:"summary" db:"summary"`
	Highlights json.RawMessage `json:"highlights" db:"highlights"`
	Blockers   json.RawMessage `json:"blockers" db:"blockers"`
	NextSteps  json.RawMessage `json:"next_steps" db:"next_steps"`
	Metrics    json.RawMessage `json:"metrics" db:"metrics"`
	CreatedBy  uuid.UUID       `json:"created_by" db:"created_by"`
	CreatedAt  time.Time       `json:"created_at" db:"created_at"`
}
