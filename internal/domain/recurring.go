package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// RecurringFrequency defines the preset frequency of a recurring schedule.
type RecurringFrequency string

const (
	RecurringFrequencyDaily   RecurringFrequency = "daily"
	RecurringFrequencyWeekly  RecurringFrequency = "weekly"
	RecurringFrequencyMonthly RecurringFrequency = "monthly"
	RecurringFrequencyCustom  RecurringFrequency = "custom"
)

// RecurringSchedule defines a template and schedule for automatically creating task instances.
// Each instance is a full task with its own comments, artifacts, and activity log.
type RecurringSchedule struct {
	ID          uuid.UUID `json:"id" db:"id"`
	WorkspaceID uuid.UUID `json:"workspace_id" db:"workspace_id"`
	ProjectID   uuid.UUID `json:"project_id" db:"project_id"`

	TitleTemplate       string `json:"title_template" db:"title_template"`
	DescriptionTemplate string `json:"description_template" db:"description_template"`

	Frequency RecurringFrequency `json:"frequency" db:"frequency"`
	CronExpr  string             `json:"cron_expr" db:"cron_expr"`
	Timezone  string             `json:"timezone" db:"timezone"`

	AssigneeID   *uuid.UUID     `json:"assignee_id" db:"assignee_id"`
	AssigneeType AssigneeType   `json:"assignee_type" db:"assignee_type"`
	Priority     Priority       `json:"priority" db:"priority"`
	Labels       pq.StringArray `json:"labels" db:"labels"`
	StatusID     *uuid.UUID     `json:"status_id" db:"status_id"`

	IsActive     bool       `json:"is_active" db:"is_active"`
	StartsAt     time.Time  `json:"starts_at" db:"starts_at"`
	EndsAt       *time.Time `json:"ends_at" db:"ends_at"`
	MaxInstances *int       `json:"max_instances" db:"max_instances"`

	NextRunAt       *time.Time `json:"next_run_at" db:"next_run_at"`
	LastTriggeredAt *time.Time `json:"last_triggered_at" db:"last_triggered_at"`
	InstanceCount   int        `json:"instance_count" db:"instance_count"`

	CreatedBy     uuid.UUID  `json:"created_by" db:"created_by"`
	CreatedByType ActorType  `json:"created_by_type" db:"created_by_type"`
	CreatedAt     time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at" db:"updated_at"`
	DeletedAt     *time.Time `json:"deleted_at,omitempty" db:"deleted_at"`
}

// RecurringInstanceSummary is a lightweight view of one task instance in a series.
// Used in history responses and agent notification payloads.
type RecurringInstanceSummary struct {
	TaskID         uuid.UUID  `json:"task_id"`
	InstanceNumber int        `json:"instance_number"`
	Title          string     `json:"title"`
	StatusCategory string     `json:"status_category"`
	CompletedAt    *time.Time `json:"completed_at"`
	CreatedAt      time.Time  `json:"created_at"`
	// LastComment is the most recent comment body (truncated to 2000 chars), nil if no comments.
	LastComment *string `json:"last_comment,omitempty"`
	// ArtifactCount is the number of artifacts attached to this instance.
	ArtifactCount int `json:"artifact_count"`
}
