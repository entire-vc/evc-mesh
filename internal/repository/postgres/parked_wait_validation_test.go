package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

func validParkedPlan() domain.ParkedWaitPlan {
	now := time.Now().UTC()
	return domain.ParkedWaitPlan{
		ID: uuid.New(), ProjectID: uuid.New(), OwnerID: uuid.New(), OwnerType: domain.AssigneeTypeAgent,
		ExpectedVersion: 1, WaitCommentID: uuid.New(), FeedReceiptID: uuid.New(), FeedSource: "confirmed_feed",
		FeedReceivedAt: now.Add(-time.Minute), FeedClosedAt: now,
		Reason: "pipeline", Condition: domain.ParkedWaitCondition{ProjectPath: "group/project", PipelineID: 42},
		Lease: domain.ParkedWaitLease{Mode: "absent"},
	}
}

func TestValidateParkedPlanRejectsUnsafeOrIncompleteEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*domain.ParkedWaitPlan)
	}{
		{"legacy feed", func(p *domain.ParkedWaitPlan) { p.FeedSource = "legacy_discovery" }},
		{"too many labels", func(p *domain.ParkedWaitPlan) { p.RemoveLabels = make([]string, 9) }},
		{"negative lease generation", func(p *domain.ParkedWaitPlan) { p.Lease.Generation = -1 }},
		{"duplicate label removal", func(p *domain.ParkedWaitPlan) { p.RemoveLabels = []string{"park:date", "park:date"} }},
		{"holder on absent lease", func(p *domain.ParkedWaitPlan) { holder := uuid.New(); p.Lease.Holder = &holder }},
		{"missing registration identity", func(p *domain.ParkedWaitPlan) { p.ID = uuid.Nil }},
		{"unsupported owner type", func(p *domain.ParkedWaitPlan) { p.OwnerType = domain.AssigneeTypeUnassigned }},
		{"missing feed window", func(p *domain.ParkedWaitPlan) { p.FeedReceivedAt = time.Time{} }},
		{"closed before received", func(p *domain.ParkedWaitPlan) { p.FeedClosedAt = p.FeedReceivedAt.Add(-time.Second) }},
		{"feed closed in future", func(p *domain.ParkedWaitPlan) { p.FeedClosedAt = time.Now().Add(2 * time.Minute) }},
		{"clear start without captured timestamp", func(p *domain.ParkedWaitPlan) { p.ClearStartAfter = true }},
		{"dependency missing blocker", func(p *domain.ParkedWaitPlan) { p.Reason = "dependency" }},
		{"pipeline path injection", func(p *domain.ParkedWaitPlan) { p.Condition.ProjectPath = "group/project?ref=main" }},
		{"date without explicit semantics", func(p *domain.ParkedWaitPlan) {
			p.Reason = "date"
			p.Condition = domain.ParkedWaitCondition{NotBefore: ptrTime(time.Now())}
		}},
		{"unsupported reason", func(p *domain.ParkedWaitPlan) { p.Reason = "manual" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := validParkedPlan()
			tc.mutate(&plan)
			require.Error(t, validateParkedPlan(plan))
		})
	}
	require.NoError(t, validateParkedPlan(validParkedPlan()))
}

func TestParkedActorRequiresAuthenticatedTaskWriter(t *testing.T) {
	_, _, err := parkedActor(context.Background())
	require.Error(t, err)
	actor := uuid.New()
	got, kind, err := parkedActor(actorctx.WithActor(context.Background(), actor, domain.ActorTypeAgent))
	require.NoError(t, err)
	require.Equal(t, actor, got)
	require.Equal(t, domain.ActorTypeAgent, kind)
}

func TestParkedSnapshotRejectsChangedAndProtectedTaskState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*domain.Task, *domain.ParkedWaitPlan)
	}{
		{"version changed", func(task *domain.Task, _ *domain.ParkedWaitPlan) { task.Version++ }},
		{"start-after changed", func(task *domain.Task, p *domain.ParkedWaitPlan) {
			later := time.Now().Add(time.Hour)
			task.StartAfter = &later
			p.ExpectedStartAfter = nil
		}},
		{"future start-after not owned for clearing", func(task *domain.Task, p *domain.ParkedWaitPlan) {
			later := time.Now().Add(time.Hour)
			task.StartAfter = &later
			p.ExpectedStartAfter = &later
			p.ClearStartAfter = false
		}},
		{"lease generation changed", func(task *domain.Task, _ *domain.ParkedWaitPlan) { task.CheckoutGeneration++ }},
		{"unknown lease mode", func(_ *domain.Task, p *domain.ParkedWaitPlan) { p.Lease.Mode = "unknown" }},
		{"unsupported park reason", func(_ *domain.Task, p *domain.ParkedWaitPlan) { p.Reason = "manual" }},
		{"unapproved label removal", func(_ *domain.Task, p *domain.ParkedWaitPlan) { p.RemoveLabels = []string{"park:dependency"} }},
		{"label removal absent from snapshot", func(_ *domain.Task, p *domain.ParkedWaitPlan) { p.RemoveLabels = []string{"park:pipeline"} }},
		{"independent hold label", func(task *domain.Task, _ *domain.ParkedWaitPlan) { task.Labels = []string{"freeze"} }},
		{"malformed custom fields", func(task *domain.Task, _ *domain.ParkedWaitPlan) { task.CustomFields = json.RawMessage("{") }},
		{"conflicting custom park reason", func(task *domain.Task, _ *domain.ParkedWaitPlan) {
			task.CustomFields = json.RawMessage(`{"park_reason":"date"}`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner, project := uuid.New(), uuid.New()
			plan := validParkedPlan()
			plan.Reason = "pipeline"
			plan.ProjectID, plan.OwnerID, plan.OwnerType = project, owner, domain.AssigneeTypeAgent
			plan.RemoveLabels = []string{"park:date"}
			task := &domain.Task{Version: plan.ExpectedVersion, ProjectID: project, AssigneeID: &owner, AssigneeType: domain.AssigneeTypeAgent, Labels: []string{"park:date"}}
			tc.mutate(task, &plan)
			require.Error(t, parkedSnapshot(task, plan, owner, time.Now()))
		})
	}
}

func ptrTime(value time.Time) *time.Time { return &value }

func TestParkedRequiredJobsValidation(t *testing.T) {
	for _, jobs := range [][]string{{}, {""}, {" "}, {"build", "build"}, make([]string, 21)} {
		p := validParkedPlan()
		p.Condition.RequiredJobs = jobs
		require.Error(t, validateParkedPlan(p), "jobs=%v", jobs)
	}
	for _, reason := range []string{"dependency", "date"} {
		p := validParkedPlan()
		p.Reason = reason
		p.Condition = domain.ParkedWaitCondition{RequiredJobs: []string{"build"}}
		if reason == "dependency" {
			id := uuid.New()
			p.Condition.TaskID = &id
		} else {
			p.Condition.NotBefore = ptrTime(time.Now())
			p.Condition.TimeSemantics = "not_before"
		}
		require.Error(t, validateParkedPlan(p))
	}
	for _, jobs := range [][]string{nil, {"build:contenthub"}, {"a", "b"}} {
		p := validParkedPlan()
		p.Condition.RequiredJobs = jobs
		require.NoError(t, validateParkedPlan(p))
	}
}

func TestParkedSnapshotAllowsMatchingCustomParkReason(t *testing.T) {
	owner, project := uuid.New(), uuid.New()
	plan := validParkedPlan()
	plan.ProjectID, plan.OwnerID, plan.OwnerType = project, owner, domain.AssigneeTypeAgent
	task := &domain.Task{
		Version: plan.ExpectedVersion, ProjectID: project, AssigneeID: &owner,
		AssigneeType: domain.AssigneeTypeAgent, CustomFields: json.RawMessage(`{"park_reason":"pipeline"}`),
	}
	require.NoError(t, parkedSnapshot(task, plan, owner, time.Now()))
}
