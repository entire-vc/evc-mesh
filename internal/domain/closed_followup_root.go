package domain

import (
	"time"

	"github.com/google/uuid"
)

// ClosedFollowUpRoot is the persistent identity of a finding the closed-card
// follow-up mechanism routes (#5194afd4): the pair (source card, finding) maps
// to exactly one follow-up root for the pair's whole life, so a repeat after
// the root was closed reopens that root instead of opening a second card.
//
// ReopenCount counts reopens inside the current 24h window anchored at
// LastReopenedAt — not a lifetime total; see the migration's own header for
// why two columns are the whole storm-limit mechanism.
type ClosedFollowUpRoot struct {
	SourceTaskID   uuid.UUID  `db:"source_task_id"`
	FindingKey     string     `db:"finding_key"`
	RootTaskID     uuid.UUID  `db:"root_task_id"`
	CreatedAt      time.Time  `db:"created_at"`
	ReopenCount    int        `db:"reopen_count"`
	LastReopenedAt *time.Time `db:"last_reopened_at"`
}
