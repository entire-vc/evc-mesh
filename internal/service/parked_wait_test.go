package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type parkedServiceRepo struct {
	*MockTaskRepository
	registered domain.RegisteredParkedWait
	releases   int
	verified   string
}

func (r *parkedServiceRepo) RegisterParkedWait(context.Context, uuid.UUID, domain.ParkedWaitPlan) (*domain.ParkedWaitResult, error) {
	return &r.registered.Result, nil
}
func (r *parkedServiceRepo) GetParkedWait(context.Context, uuid.UUID, uuid.UUID) (*domain.RegisteredParkedWait, error) {
	return &r.registered, nil
}
func (r *parkedServiceRepo) ReleaseParkedWait(_ context.Context, _ uuid.UUID, _ domain.ReleaseParkedWait, status string) (*domain.ParkedWaitResult, error) {
	r.releases++
	r.verified = status
	result := r.registered.Result
	result.Replayed = result.Released
	result.Released = true
	return &result, nil
}

type parkedPipelineChecker struct {
	fakeGitLabMRChecker
	status  string
	err     error
	calls   int
	project string
	id      int
}

func (c *parkedPipelineChecker) GetPipelineStatus(_ context.Context, path string, id int) (string, error) {
	c.calls++
	c.project = path
	c.id = id
	return c.status, c.err
}

func setupParkedService(t *testing.T) (*taskService, *parkedServiceRepo, *parkedPipelineChecker, uuid.UUID, domain.ReleaseParkedWait) {
	t.Helper()
	id := uuid.New()
	r := &parkedServiceRepo{MockTaskRepository: NewMockTaskRepository()}
	task := &domain.Task{ID: id, Version: 7, ProjectID: uuid.New(), AssigneeType: domain.AssigneeTypeAgent}
	r.items[id] = task
	r.registered.Plan = domain.ParkedWaitPlan{ID: uuid.New(), ExpectedVersion: 7, ProjectID: task.ProjectID, Reason: "pipeline", Condition: domain.ParkedWaitCondition{ProjectPath: "entire-vc/evc-mesh", PipelineID: 93}}
	c := &parkedPipelineChecker{status: "success"}
	s := newTestTaskService(r, NewMockTaskStatusRepository(), NewMockTaskDependencyRepository(), NewMockActivityLogRepository(), WithGitLabMRChecker(c)).(*taskService)
	input := domain.ReleaseParkedWait{RegistrationID: r.registered.Plan.ID, ExpectedVersion: 7, ReleaseID: uuid.New(), Trigger: domain.ParkedWaitTrigger{Kind: "pipeline", EventID: "event-93"}}
	return s, r, c, id, input
}

func TestParkedServiceVerifiesPipelineAndFailsClosed(t *testing.T) {
	for _, status := range []string{"running", "pending", "manual", "scheduled", "success", "failed", "canceled", "skipped"} {
		t.Run(status, func(t *testing.T) {
			s, r, c, id, input := setupParkedService(t)
			c.status = status
			result, err := s.ReleaseParkedWait(context.Background(), id, input)
			require.Equal(t, "entire-vc/evc-mesh", c.project)
			require.Equal(t, 93, c.id)
			if status == "success" || status == "failed" || status == "canceled" || status == "skipped" {
				require.NoError(t, err)
				require.True(t, result.Released)
				require.Equal(t, status, r.verified)
				require.Equal(t, 1, r.releases)
			} else {
				var e *apierror.Error
				require.ErrorAs(t, err, &e)
				require.Equal(t, 409, e.Code)
				require.Zero(t, r.releases)
			}
		})
	}
	s, r, c, id, input := setupParkedService(t)
	c.err = errors.New("GitLab unavailable")
	_, err := s.ReleaseParkedWait(context.Background(), id, input)
	var e *apierror.Error
	require.ErrorAs(t, err, &e)
	require.Equal(t, 503, e.Code)
	require.Zero(t, r.releases)
	// A successful receipt is replayed without asking an unavailable provider.
	r.registered.Result.Released = true
	result, err := s.ReleaseParkedWait(context.Background(), id, input)
	require.NoError(t, err)
	require.True(t, result.Replayed)
	require.Equal(t, 1, c.calls)
}

func TestParkedServiceHumanPoliciesUseExactVersion(t *testing.T) {
	for _, label := range []string{"blocked-on-pavel", "needs:human", "decision", "kind:human-verify"} {
		t.Run(label, func(t *testing.T) {
			s, r, c, id, input := setupParkedService(t)
			r.items[id].Labels = []string{label}
			_, err := s.ReleaseParkedWait(context.Background(), id, input)
			require.Error(t, err)
			require.Zero(t, r.releases)
			require.Zero(t, c.calls)
		})
	}
	s, r, c, id, input := setupParkedService(t)
	r.items[id].Description = "❓ **Blocking @pavel**: нужен ответ"
	_, err := s.ReleaseParkedWait(context.Background(), id, input)
	require.Error(t, err)
	require.Zero(t, c.calls)
	r.items[id].Description = ""
	r.items[id].Version++
	_, err = s.ReleaseParkedWait(context.Background(), id, input)
	require.Error(t, err)
	require.Zero(t, r.releases)
}
