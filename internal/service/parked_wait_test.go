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

type parkedReleaseConnectionHeld struct{}

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

func (r *parkedServiceRepo) ReleaseParkedWaitVerified(ctx context.Context, id uuid.UUID, input domain.ReleaseParkedWait, verify func(context.Context, *domain.Task, domain.ParkedWaitPlan) (string, error)) (*domain.ParkedWaitResult, error) {
	status := ""
	if !r.registered.Result.Released {
		var err error
		status, err = verify(context.WithValue(ctx, parkedReleaseConnectionHeld{}, true), r.items[id], r.registered.Plan)
		if err != nil {
			return nil, err
		}
	}
	return r.ReleaseParkedWait(ctx, id, input, status)
}

type parkedPipelineChecker struct {
	fakeGitLabMRChecker
	status    string
	err       error
	calls     int
	project   string
	id        int
	jobsReady bool
	jobsErr   error
	jobsCalls int
	jobs      []string
}

func (c *parkedPipelineChecker) PipelineRequiredJobsSucceeded(_ context.Context, path string, id int, jobs []string) (bool, error) {
	c.jobsCalls++
	c.project, c.id, c.jobs = path, id, jobs
	return c.jobsReady, c.jobsErr
}

func TestParkedServiceRequiredJobsIgnoreAggregateAndReplay(t *testing.T) {
	for _, tc := range []struct {
		name, aggregate string
		ready           bool
		providerErr     error
		code            int
	}{
		{"manual with required success", "manual", true, nil, 0},
		{"failed unrelated with required success", "failed", true, nil, 0},
		{"aggregate success with required running", "success", false, nil, 409},
		{"jobs provider failure", "success", false, errors.New("incomplete pagination"), 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r, c, id, input := setupParkedService(t)
			r.registered.Plan.Condition.RequiredJobs = []string{"build:contenthub", "deploy:contenthub", "verify:contenthub"}
			c.status, c.jobsReady, c.jobsErr = tc.aggregate, tc.ready, tc.providerErr
			result, err := s.ReleaseParkedWait(context.Background(), id, input)
			if tc.code == 0 {
				require.NoError(t, err)
				require.True(t, result.Released)
				require.Equal(t, "success", r.verified)
				r.registered.Result = *result
				c.jobsErr = errors.New("offline after lost response")
				r.items[id].Version++
				result, err = s.ReleaseParkedWait(context.Background(), id, input)
				require.NoError(t, err)
				require.True(t, result.Replayed)
			} else {
				var e *apierror.Error
				require.ErrorAs(t, err, &e)
				require.Equal(t, tc.code, e.Code)
				require.Zero(t, r.releases)
			}
			require.Zero(t, c.calls)
			require.Equal(t, 1, c.jobsCalls)
			require.Equal(t, r.registered.Plan.Condition.RequiredJobs, c.jobs)
			require.Equal(t, 93, c.id)
		})
	}
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

func TestParkedServiceRequiredJobsNeedsCheckerAndPreservesHumanFence(t *testing.T) {
	s, r, c, id, input := setupParkedService(t)
	r.registered.Plan.Condition.RequiredJobs = []string{"build"}
	s.gitlabMRChecker = &fakeGitLabMRChecker{}
	_, err := s.ReleaseParkedWait(context.Background(), id, input)
	var e *apierror.Error
	require.ErrorAs(t, err, &e)
	require.Equal(t, 503, e.Code)
	require.Zero(t, r.releases)
	s.gitlabMRChecker = c
	r.items[id].Labels = []string{"needs:human"}
	_, err = s.ReleaseParkedWait(context.Background(), id, input)
	require.Error(t, err)
	require.Zero(t, c.jobsCalls)
	require.Zero(t, r.releases)
}

type parkedPoolProjectRepo struct {
	*MockProjectRepository
	readsWhileHeld int
	readHook       func()
	unavailable    bool
}

func (r *parkedPoolProjectRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Project, error) {
	if ctx.Value(parkedReleaseConnectionHeld{}) != nil {
		r.readsWhileHeld++
		return nil, errors.New("all pool connections held by release transactions")
	}
	if r.readHook != nil {
		r.readHook()
	}
	if r.unavailable {
		return nil, errors.New("workspace configuration unavailable")
	}
	return r.MockProjectRepository.GetByID(ctx, id)
}

func TestParkedServiceResolvesWorkspaceBeforeHoldingConnection(t *testing.T) {
	s, r, c, id, input := setupParkedService(t)
	r.registered.Plan.Condition.RequiredJobs = []string{"build"}
	c.jobsReady = true
	projects := &parkedPoolProjectRepo{MockProjectRepository: NewMockProjectRepository().WithDefaultWorkspace(testDefaultWorkspaceID)}
	s.projectRepo = projects
	result, err := s.ReleaseParkedWait(context.Background(), id, input)
	require.NoError(t, err)
	require.True(t, result.Released)
	require.Zero(t, projects.readsWhileHeld)
	require.Equal(t, 1, c.jobsCalls)

	// A committed receipt still replays after configuration disappears.
	r.registered.Result = *result
	s.projectRepo = nil
	s.gitlabMRChecker = nil
	result, err = s.ReleaseParkedWait(context.Background(), id, input)
	require.NoError(t, err)
	require.True(t, result.Replayed)
	require.Equal(t, 1, c.jobsCalls)
}

func TestParkedServiceConcurrentReceiptReplaysConfigurationError(t *testing.T) {
	s, r, c, id, input := setupParkedService(t)
	r.registered.Plan.Condition.RequiredJobs = []string{"build"}
	s.projectRepo = &parkedPoolProjectRepo{
		MockProjectRepository: NewMockProjectRepository(),
		unavailable:           true,
		readHook: func() {
			// The other request commits while this request resolves configuration.
			r.registered.Result.Released = true
		},
	}
	result, err := s.ReleaseParkedWait(context.Background(), id, input)
	require.NoError(t, err)
	require.True(t, result.Replayed)
	require.Zero(t, c.jobsCalls)
	require.Zero(t, c.calls)
}
