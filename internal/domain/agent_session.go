package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// AgentSessionStatus represents the lifecycle state of an agent session.
type AgentSessionStatus string

const (
	AgentSessionStatusActive AgentSessionStatus = "active"
	AgentSessionStatusIdle   AgentSessionStatus = "idle"
	AgentSessionStatusEnded  AgentSessionStatus = "ended"
)

// ComplianceDetail records which Agent Collaboration Protocol (ACP) steps the agent
// completed during a session. Used for compliance scoring and reporting.
type ComplianceDetail struct {
	// ReadTask indicates the agent called get_task before starting work.
	ReadTask bool `json:"read_task"`
	// PostedPlan indicates the agent posted a plan comment before coding.
	PostedPlan bool `json:"posted_plan"`
	// UsedBranch indicates the agent worked on a non-main branch.
	UsedBranch bool `json:"used_branch"`
	// RanTests indicates the agent ran tests before moving to review.
	RanTests bool `json:"ran_tests"`
	// MovedToReview indicates the agent moved the task to the review category when done.
	MovedToReview bool `json:"moved_to_review"`
	// ReassignedToCreator indicates the agent reassigned the task to the task creator.
	ReassignedToCreator bool `json:"reassigned_to_creator"`
	// AddedSummaryComment indicates the agent left a summary comment on the task.
	AddedSummaryComment bool `json:"added_summary_comment"`
}

// ComplianceScore computes a 0.0–1.0 compliance score based on completed steps.
// Each of the seven ACP steps contributes equally to the total score.
func (c ComplianceDetail) ComputeScore() float32 {
	total := 7
	passed := 0
	if c.ReadTask {
		passed++
	}
	if c.PostedPlan {
		passed++
	}
	if c.UsedBranch {
		passed++
	}
	if c.RanTests {
		passed++
	}
	if c.MovedToReview {
		passed++
	}
	if c.ReassignedToCreator {
		passed++
	}
	if c.AddedSummaryComment {
		passed++
	}
	return float32(passed) / float32(total)
}

// AgentSession tracks a single working session for an agent.
// Sessions accumulate metrics (tool calls, tokens, cost, compliance) for monitoring
// and are used to detect stale/abandoned sessions via EndStale.
type AgentSession struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	WorkspaceID uuid.UUID  `json:"workspace_id" db:"workspace_id"`
	AgentID     uuid.UUID  `json:"agent_id" db:"agent_id"`
	TaskID      *uuid.UUID `json:"task_id,omitempty" db:"task_id"`
	StartedAt   time.Time  `json:"started_at" db:"started_at"`
	EndedAt     *time.Time `json:"ended_at,omitempty" db:"ended_at"`
	// LastActivityAt is bumped on every tool call (IncrementToolBreakdown) and
	// every /agents/me/sessions/report call (ReportSession's Update). EndStale
	// closes a session on inactivity measured from THIS field, not StartedAt —
	// a long-lived fiddler session that keeps seeing tool calls must not be
	// force-ended purely for having been open a long time (see migration
	// 20260915001's comment for the incident this fixes).
	LastActivityAt   time.Time          `json:"last_activity_at" db:"last_activity_at"`
	Status           AgentSessionStatus `json:"status" db:"status"`
	ToolCalls        int                `json:"tool_calls" db:"tool_calls"`
	ToolBreakdown    json.RawMessage    `json:"tool_breakdown" db:"tool_breakdown"`
	TasksTouched     []uuid.UUID        `json:"tasks_touched" db:"tasks_touched"`
	EventsPublished  int                `json:"events_published" db:"events_published"`
	MemoriesCreated  int                `json:"memories_created" db:"memories_created"`
	ModelUsed        string             `json:"model_used" db:"model_used"`
	TokensIn         int64              `json:"tokens_in" db:"tokens_in"`
	TokensOut        int64              `json:"tokens_out" db:"tokens_out"`
	EstimatedCost    float64            `json:"estimated_cost" db:"estimated_cost"`
	ComplianceScore  float32            `json:"compliance_score" db:"compliance_score"`
	ComplianceDetail json.RawMessage    `json:"compliance_detail" db:"compliance_detail"`

	// Known/unknown facts per measure (migration 20261113300). *Known: some report gave a
	// figure. *Unknown: some report said the figure is unknown; never cleared by a later
	// report. A measure is incomplete when *Unknown or not *Known.
	TokensInKnown    bool `json:"tokens_in_known" db:"tokens_in_known"`
	TokensOutKnown   bool `json:"tokens_out_known" db:"tokens_out_known"`
	CostKnown        bool `json:"cost_known" db:"cost_known"`
	TokensInUnknown  bool `json:"tokens_in_unknown" db:"tokens_in_unknown"`
	TokensOutUnknown bool `json:"tokens_out_unknown" db:"tokens_out_unknown"`
	CostUnknown      bool `json:"cost_unknown" db:"cost_unknown"`
}

// SessionUsage is one usage report. A measure flagged unknown carries no figure (its number
// is ignored); a measure not flagged is a known figure, including a real 0.
type SessionUsage struct {
	TokensIn         int64
	TokensOut        int64
	Cost             float64
	TokensInUnknown  bool
	TokensOutUnknown bool
	CostUnknown      bool
}

// TaskCostSummary aggregates cost and quality metrics for a single task
// across all agent sessions that worked on it.
//
// TotalCost, TokensIn and TokensOut are the sum of the KNOWN figures: null when no session
// has a known figure for that measure (never 0), with the matching *Incomplete flag true
// whenever any session's figure is unknown or missing. A real zero stays 0 without the flag.
// A task with no sessions keeps 0 and no flags.
type TaskCostSummary struct {
	TotalCost           *float64 `json:"total_cost"`
	TokensIn            *int64   `json:"tokens_in"`
	TokensOut           *int64   `json:"tokens_out"`
	CostIncomplete      bool     `json:"cost_incomplete"`
	TokensInIncomplete  bool     `json:"tokens_in_incomplete"`
	TokensOutIncomplete bool     `json:"tokens_out_incomplete"`
	SessionCount        int      `json:"session_count"`
	ReworkCount         int      `json:"rework_count"`
	QualityFlag         string   `json:"quality_flag"` // "golden" | "rework" | "multi-turn" | "unknown"
}
