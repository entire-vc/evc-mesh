package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

const typedSHA = "0123456789abcdef0123456789abcdef01234567"

// The merge, registered and baseline forms are stored with their fields and
// read back by the runner's GET /tasks/:id/wait exactly as written.
func TestTurnCommandRepo_TypedWaitKindsRoundTrip_DB(t *testing.T) {
	ctx := context.Background()
	str := func(s string) *string { return &s }
	i64 := func(n int64) *int64 { return &n }
	iid := 12

	cases := []struct {
		name  string
		wait  func(f *typedOutcomeFixture) *domain.TaskWait
		check func(t *testing.T, got *domain.TaskWait)
	}{
		{"merge", func(*typedOutcomeFixture) *domain.TaskWait {
			return &domain.TaskWait{Kind: domain.TaskWaitMerge, Provider: str("gitlab"), ProjectPath: str("g/p"), MRIID: &iid, AfterSHA: str(typedSHA)}
		}, func(t *testing.T, got *domain.TaskWait) {
			assert.Equal(t, domain.TaskWaitMerge, got.Kind)
			assert.Equal(t, "g/p", *got.ProjectPath)
			assert.Equal(t, 12, *got.MRIID)
			assert.Equal(t, typedSHA, *got.AfterSHA)
		}},
		{"registered", func(f *typedOutcomeFixture) *domain.TaskWait {
			return &domain.TaskWait{Kind: domain.TaskWaitRegistered, Registration: str("stall"), RegistrationRef: str(f.task.String()[:8])}
		}, func(t *testing.T, got *domain.TaskWait) {
			assert.Equal(t, domain.TaskWaitRegistered, got.Kind)
			assert.Equal(t, "stall", *got.Registration)
			assert.Len(t, *got.RegistrationRef, 8)
		}},
		{"pipeline changed with a CI baseline", func(*typedOutcomeFixture) *domain.TaskWait {
			return &domain.TaskWait{Kind: domain.TaskWaitPipeline, Provider: str("github"), ProjectPath: str("org/repo"),
				PipelineID: i64(42), Until: str("changed"), AfterSHA: str(typedSHA), AfterPipelineID: i64(42), AfterStatus: str("failed"),
				RequiredJobs: []string{"lint"}}
		}, func(t *testing.T, got *domain.TaskWait) {
			assert.Equal(t, "changed", *got.Until)
			assert.Equal(t, "github", *got.Provider)
			assert.Equal(t, int64(42), *got.AfterPipelineID)
			assert.Equal(t, "failed", *got.AfterStatus)
			assert.Equal(t, []string{"lint"}, []string(got.RequiredJobs))
		}},
		{"manual", func(*typedOutcomeFixture) *domain.TaskWait {
			return &domain.TaskWait{Kind: domain.TaskWaitManual, Note: "vendor answers by mail"}
		}, func(t *testing.T, got *domain.TaskWait) {
			assert.Equal(t, domain.TaskWaitManual, got.Kind)
			assert.Equal(t, "vendor answers by mail", got.Note)
			assert.Nil(t, got.TargetTaskID)
			assert.Nil(t, got.UntilAt)
		}},
		{"GitHub PR until merged after opened", func(*typedOutcomeFixture) *domain.TaskWait {
			return &domain.TaskWait{Kind: domain.TaskWaitMR, Provider: str("github"), ProjectPath: str("org/repo"),
				MRIID: &iid, Until: str("merged"), AfterSHA: str(typedSHA), AfterStatus: str("opened")}
		}, func(t *testing.T, got *domain.TaskWait) {
			assert.Equal(t, domain.TaskWaitMR, got.Kind)
			assert.Equal(t, "github", *got.Provider)
			assert.Equal(t, "merged", *got.Until)
			assert.Equal(t, "opened", *got.AfterStatus)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newTypedOutcomeFixture(t)
			f.holdCheckout(t, f.task, f.agent)
			res, err := NewTurnCommandRepo(f.db).CommitTurn(ctx, &domain.TurnCommand{TaskID: f.task, AgentID: f.agent,
				Kind: domain.TurnOutcomeWait, CommentBody: "Waiting.", RuntimeReason: "x", Wait: tc.wait(f)})
			require.NoError(t, err)
			assert.True(t, res.Released)
			require.NotNil(t, res.Outcome.WaitID)
			got, err := f.waits.GetActive(ctx, f.task, &f.agent)
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Equal(t, *res.Outcome.WaitID, got.ID)
			tc.check(t, got)
		})
	}
}

// The table refuses the new shapes when a writer bypasses the service.
func TestTaskWaits_TypedKindConstraints_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	insert := func(cols, vals string, args ...any) error {
		_, err := f.db.Exec(`INSERT INTO task_waits (task_id, agent_id, `+cols+`) VALUES ($1, $2, `+vals+`)`,
			append([]any{f.task, f.agent}, args...)...)
		if err == nil {
			_, _ = f.db.Exec(`DELETE FROM task_waits WHERE task_id = $1`, f.task)
		}
		return err
	}
	for name, err := range map[string]error{
		"merge without sha":         insert(`kind, project_path, mr_iid`, `'merge', 'g/p', 3`),
		"merge with a short sha":    insert(`kind, project_path, mr_iid, after_sha`, `'merge', 'g/p', 3, 'abc'`),
		"registered without ref":    insert(`kind, registration`, `'registered', 'gmail'`),
		"registered unknown":        insert(`kind, registration, registration_ref`, `'registered', 'poll', 'x'`),
		"registration on a card":    insert(`kind, registration, registration_ref`, `'time', 'gmail', 'x'`),
		"baseline without a sha":    insert(`kind, project_path, pipeline_id, until_state, after_pipeline_id`, `'pipeline', 'g/p', 1, 'green', 1`),
		"pipeline with a bad until": insert(`kind, project_path, pipeline_id, until_state`, `'pipeline', 'g/p', 1, 'merged'`),
		"unknown kind":              insert(`kind`, `'poll'`),
	} {
		assert.Error(t, err, name)
	}
	require.NoError(t, insert(`kind, project_path, mr_iid, after_sha`, `'merge', 'g/p', 3, $3`, typedSHA))
	require.NoError(t, insert(`kind, project_path, pipeline_id, until_state`, `'pipeline', 'g/p', 1, 'changed'`))
}

// ask_human's wait points at the question already posted through the comment
// service: the command adds no comment of its own and the row carries the
// person and the question.
func TestTurnCommandRepo_HumanWaitReusesQuestion_DB(t *testing.T) {
	ctx := context.Background()
	f := newTypedOutcomeFixture(t)
	repo := NewTurnCommandRepo(f.db)

	held, err := repo.HoldsCheckout(ctx, f.task, f.agent)
	require.NoError(t, err)
	assert.False(t, held)
	f.holdCheckout(t, f.task, f.agent)
	held, err = repo.HoldsCheckout(ctx, f.task, f.agent)
	require.NoError(t, err)
	assert.True(t, held)
	held, err = repo.HoldsCheckout(ctx, f.task, f.agent2)
	require.NoError(t, err)
	assert.False(t, held)

	question := uuid.New()
	_, err = f.db.Exec(`INSERT INTO comments (id, task_id, author_id, author_type, body, metadata, is_internal, created_at, updated_at)
		VALUES ($1, $2, $3, 'agent', '❓ Blocking @alex: ship?', '{}', false, now(), now())`, question, f.task, f.agent)
	require.NoError(t, err)
	ref, who, q := "comment:"+question.String(), "alex", "ship?"
	res, err := repo.CommitTurn(ctx, &domain.TurnCommand{TaskID: f.task, AgentID: f.agent, Kind: domain.TurnOutcomeWait,
		CommentID: &question, RuntimeReason: "@alex's answer",
		Wait: &domain.TaskWait{Kind: domain.TaskWaitHuman, HumanGateRef: &ref, Addressee: &who, Question: &q, Note: "asked @alex"}})
	require.NoError(t, err)
	assert.Equal(t, question, res.CommentID)
	var comments int
	require.NoError(t, f.db.Get(&comments, `SELECT count(*) FROM comments WHERE task_id = $1`, f.task))
	assert.Equal(t, 1, comments, "the command wrote a second comment")

	got, err := f.waits.GetActive(ctx, f.task, &f.agent)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, domain.TaskWaitHuman, got.Kind)
	assert.Equal(t, ref, *got.HumanGateRef)
	assert.Equal(t, "alex", *got.Addressee)
	assert.Equal(t, "ship?", *got.Question)
	var stateRef string
	require.NoError(t, f.db.Get(&stateRef, `SELECT runtime_state->>'ref' FROM tasks WHERE id = $1`, f.task))
	assert.Equal(t, "wait:"+got.ID.String(), stateRef)
}
