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
	ID               uuid.UUID          `json:"id" db:"id"`
	WorkspaceID      uuid.UUID          `json:"workspace_id" db:"workspace_id"`
	AgentID          uuid.UUID          `json:"agent_id" db:"agent_id"`
	StartedAt        time.Time          `json:"started_at" db:"started_at"`
	EndedAt          *time.Time         `json:"ended_at,omitempty" db:"ended_at"`
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
}
