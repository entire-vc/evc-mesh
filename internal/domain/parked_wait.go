package domain

import (
	"time"

	"github.com/google/uuid"
)

// ParkedWaitPlan is an explicit owner-authored wait, never inferred from edges.
// It contains no checkout token or other credential. ID is a public generation.
type ParkedWaitPlan struct {
	ID                 uuid.UUID           `json:"registration_id"`
	ProjectID          uuid.UUID           `json:"project_id"`
	OwnerID            uuid.UUID           `json:"owner_id"`
	OwnerType          AssigneeType        `json:"owner_type"`
	ExpectedVersion    int64               `json:"expected_version"`
	WaitCommentID      uuid.UUID           `json:"wait_comment_id"`
	FeedReceiptID      uuid.UUID           `json:"feed_receipt_id"`
	FeedSource         string              `json:"feed_source"`
	FeedReceivedAt     time.Time           `json:"feed_received_at"`
	FeedClosedAt       time.Time           `json:"feed_closed_at"`
	Reason             string              `json:"reason"`
	Condition          ParkedWaitCondition `json:"condition"`
	RemoveLabels       []string            `json:"remove_labels"`
	ExpectedStartAfter *time.Time          `json:"expected_start_after"`
	ClearStartAfter    bool                `json:"clear_start_after"`
	Lease              ParkedWaitLease     `json:"expected_lease"`
}

type ParkedWaitCondition struct {
	TaskID        *uuid.UUID `json:"task_id,omitempty"`
	ProjectPath   string     `json:"project_path,omitempty"`
	PipelineID    int        `json:"pipeline_id,omitempty"`
	RequiredJobs  []string   `json:"required_jobs,omitempty"`
	NotBefore     *time.Time `json:"not_before,omitempty"`
	TimeSemantics string     `json:"time_semantics,omitempty"`
}

type ParkedWaitLease struct {
	Mode       string     `json:"mode"` // absent, or owned (only the authenticated holder)
	Generation int64      `json:"generation"`
	Holder     *uuid.UUID `json:"holder,omitempty"`
	SessionID  *uuid.UUID `json:"session_id,omitempty"`
}

type ParkedWaitTrigger struct {
	EventID string `json:"event_id"`
	Kind    string `json:"kind"`
}

type ReleaseParkedWait struct {
	RegistrationID  uuid.UUID         `json:"registration_id"`
	ExpectedVersion int64             `json:"expected_version"`
	ReleaseID       uuid.UUID         `json:"release_id"`
	Trigger         ParkedWaitTrigger `json:"trigger"`
}

type ParkedWaitResult struct {
	RegistrationID uuid.UUID  `json:"registration_id"`
	TaskID         uuid.UUID  `json:"task_id"`
	ProjectID      uuid.UUID  `json:"project_id"`
	OwnerID        uuid.UUID  `json:"owner_id"`
	Version        int64      `json:"version"`
	ActivityID     *uuid.UUID `json:"activity_id,omitempty"`
	Released       bool       `json:"released"`
	Replayed       bool       `json:"replayed"`
}

type RegisteredParkedWait struct {
	Plan   ParkedWaitPlan
	Result ParkedWaitResult
}
