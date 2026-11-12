package service

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	githubapi "github.com/entire-vc/evc-mesh/internal/integration/github"
	gitlabapi "github.com/entire-vc/evc-mesh/internal/integration/gitlab"
)

const (
	sha40 = "0123456789abcdef0123456789abcdef01234567"
	sha64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// The marker grammar's merge, registered and baseline forms recorded as typed
// waits: what is accepted is committed with its fields, what is refused never
// reaches the store.
func TestTurnCommandService_WaitForTypedKinds(t *testing.T) {
	merge := func(iid int, sha string) WaitForInput {
		return WaitForInput{On: "merge", ProjectPath: ptr("g/p"), MRIID: ptr(iid), AfterSHA: ptr(sha)}
	}
	reg := func(r, ref string) WaitForInput {
		in := WaitForInput{On: "registered", Registration: ptr(r)}
		if ref != "" {
			in.RegistrationRef = ptr(ref)
		}
		return in
	}
	ciBaseline := func(in WaitForInput, pipeline int64, status string) WaitForInput {
		in.AfterSHA, in.AfterPipelineID, in.AfterStatus = ptr(sha40), ptr(pipeline), ptr(status)
		return in
	}
	mr := func(until string) WaitForInput {
		return WaitForInput{On: "mr", Provider: ptr("gitlab"), ProjectPath: ptr("g/p"), MRIID: ptr(10), Until: ptr(until)}
	}
	pipe := func(until string) WaitForInput {
		return WaitForInput{On: "pipeline", ProjectPath: ptr("g/p"), PipelineID: ptr(int64(500)), Until: ptr(until)}
	}

	refused := []struct {
		name string
		in   func(task uuid.UUID) WaitForInput
		code string
		msg  string
	}{
		{"human through wait_for", func(uuid.UUID) WaitForInput { return WaitForInput{On: "human"} }, domain.CodeInvalidTarget, "ask_human"},
		{"unknown kind", func(uuid.UUID) WaitForInput { return WaitForInput{On: "gmail"} }, domain.CodeInvalidTarget, "registered or manual"},
		{"merge without sha", func(uuid.UUID) WaitForInput {
			return WaitForInput{On: "merge", ProjectPath: ptr("g/p"), MRIID: ptr(10)}
		}, domain.CodeInvalidTarget, "after_sha"},
		{"merge with short sha", func(uuid.UUID) WaitForInput { return merge(10, "abc123") }, domain.CodeInvalidTarget, "after_sha"},
		{"merge with uppercase sha", func(uuid.UUID) WaitForInput { return merge(10, strings.ToUpper(sha40)) }, domain.CodeInvalidTarget, "after_sha"},
		{"merge on GitHub", func(uuid.UUID) WaitForInput { in := merge(10, sha40); in.Provider = ptr("github"); return in }, domain.CodeInvalidTarget, "on=mr provider=github until=merged"},
		{"merge with until", func(uuid.UUID) WaitForInput { in := merge(10, sha40); in.Until = ptr("merged"); return in }, domain.CodeInvalidTarget, "only"},
		{"merge without group", func(uuid.UUID) WaitForInput {
			return WaitForInput{On: "merge", ProjectPath: ptr("p"), MRIID: ptr(10), AfterSHA: ptr(sha40)}
		}, domain.CodeInvalidTarget, "group/project"},
		{"merge of a merged MR", func(uuid.UUID) WaitForInput { return merge(11, sha40) }, domain.CodeAlreadyMet, ""},
		{"merge of an unknown MR", func(uuid.UUID) WaitForInput { return merge(99, sha40) }, domain.CodeInvalidTarget, "not found"},
		{"unknown registration", func(uuid.UUID) WaitForInput { return reg("poll", "") }, domain.CodeInvalidTarget, "registration must be one of"},
		{"no registration", func(uuid.UUID) WaitForInput { return WaitForInput{On: "registered"} }, domain.CodeInvalidTarget, "registration must be one of"},
		{"receipt of another card", func(uuid.UUID) WaitForInput { return reg("receipt", "deadbeef") }, domain.CodeInvalidTarget, "armed for this card"},
		{"gmail ref malformed", func(uuid.UUID) WaitForInput { return reg("gmail", "gmr_123") }, domain.CodeInvalidTarget, "gmr_"},
		{"ops-review ref malformed", func(uuid.UUID) WaitForInput { return reg("ops-review", sha40) }, domain.CodeInvalidTarget, "64 hex"},
		{"native without ref", func(uuid.UUID) WaitForInput { return reg("native", "") }, domain.CodeInvalidTarget, "registration_ref"},
		{"registered with until", func(uuid.UUID) WaitForInput { in := reg("native", sha64); in.Until = ptr("green"); return in }, domain.CodeInvalidTarget, "only"},
		{"merged baseline with a pipeline", func(uuid.UUID) WaitForInput { return ciBaseline(mr("merged"), 7, "opened") }, domain.CodeInvalidTarget, "opened"},
		{"merged baseline without status", func(uuid.UUID) WaitForInput { in := mr("merged"); in.AfterSHA = ptr(sha40); return in }, domain.CodeInvalidTarget, "opened"},
		{"CI baseline without pipeline", func(uuid.UUID) WaitForInput {
			in := mr("changed")
			in.AfterSHA, in.AfterStatus = ptr(sha40), ptr("running")
			return in
		}, domain.CodeInvalidTarget, "after_pipeline_id"},
		{"CI baseline with unknown status", func(uuid.UUID) WaitForInput { return ciBaseline(mr("green"), 7, "opened") }, domain.CodeInvalidTarget, "pipeline status"},
		{"CI baseline without sha", func(uuid.UUID) WaitForInput {
			in := mr("green")
			in.AfterPipelineID, in.AfterStatus = ptr(int64(7)), ptr("running")
			return in
		}, domain.CodeInvalidTarget, "after_sha"},
		{"pipeline baseline of another pipeline", func(uuid.UUID) WaitForInput { return ciBaseline(pipe("changed"), 499, "failed") }, domain.CodeInvalidTarget, "pipeline_id"},
		{"finished takes no baseline", func(uuid.UUID) WaitForInput { return ciBaseline(pipe("finished"), 500, "running") }, domain.CodeInvalidTarget, "no baseline"},
		{"pipeline provider unknown", func(uuid.UUID) WaitForInput { in := pipe("green"); in.Provider = ptr("bitbucket"); return in }, domain.CodeInvalidTarget, "gitlab or github"},
		{"GitHub run without owner/repo", func(uuid.UUID) WaitForInput {
			return WaitForInput{On: "pipeline", Provider: ptr("github"), ProjectPath: ptr("a/b/c"), PipelineID: ptr(int64(9)), Until: ptr("green")}
		}, domain.CodeInvalidTarget, "owner/repo"},
		// New fields are refused where the kind does not read them, never dropped.
		{"after_sha on a card wait", func(uuid.UUID) WaitForInput {
			other := uuid.New()
			return WaitForInput{On: "card", TargetTaskID: &other, AfterSHA: ptr(sha40)}
		}, domain.CodeInvalidTarget, "after_sha is not a field of an on=card wait"},
		{"registration on an mr wait", func(uuid.UUID) WaitForInput { in := mr("merged"); in.Registration = ptr("gmail"); return in }, domain.CodeInvalidTarget, "registration is not a field"},
		{"after_status on a time wait", func(uuid.UUID) WaitForInput { return WaitForInput{On: "time", AfterStatus: ptr("running")} }, domain.CodeInvalidTarget, "after_status"},
		{"after_pipeline_id on a registered wait", func(uuid.UUID) WaitForInput {
			in := reg("receipt", "")
			in.AfterPipelineID = ptr(int64(7))
			return in
		}, domain.CodeInvalidTarget, "after_pipeline_id is not a field of an on=registered wait"},
		{"registration_ref on a merge wait", func(uuid.UUID) WaitForInput { in := merge(10, sha40); in.RegistrationRef = ptr("x"); return in },
			domain.CodeInvalidTarget, "registration_ref is not a field of an on=merge wait"},
	}
	for _, tc := range refused {
		t.Run("refused/"+tc.name, func(t *testing.T) {
			e := newTurnEnv(liveGitLab())
			_, err := e.svc.WaitFor(e.ctx, e.task, tc.in(e.task))
			assert.Equal(t, tc.code, outcomeCode(t, err))
			assert.Contains(t, err.Error(), tc.msg)
			assert.Empty(t, e.store.committed, "a refused wait reached the store")
		})
	}

	accepted := []struct {
		name    string
		gl      GitLabCheckerSource
		in      func(task uuid.UUID) WaitForInput
		comment string
		check   func(t *testing.T, task uuid.UUID, w *domain.TaskWait)
	}{
		{"merge", liveGitLab(), func(uuid.UUID) WaitForInput { return merge(10, sha40) },
			"Waiting on the merge gate of MR g/p!10 at 0123456789ab.",
			func(t *testing.T, _ uuid.UUID, w *domain.TaskWait) {
				assert.Equal(t, domain.TaskWaitMerge, w.Kind)
				assert.Equal(t, "gitlab", *w.Provider)
				assert.Equal(t, sha40, *w.AfterSHA)
				assert.Nil(t, w.Until)
			}},
		{"merge with a 64-hex head", liveGitLab(), func(uuid.UUID) WaitForInput { return merge(10, sha64) }, "", nil},
		{"receipt of this card, ref filled in", nil, func(uuid.UUID) WaitForInput { return reg("receipt", "") }, "",
			func(t *testing.T, task uuid.UUID, w *domain.TaskWait) {
				assert.Equal(t, domain.TaskWaitRegistered, w.Kind)
				assert.Equal(t, "receipt", *w.Registration)
				assert.Equal(t, task.String()[:8], *w.RegistrationRef)
			}},
		{"queue of this card, ref given", nil, func(task uuid.UUID) WaitForInput { return reg("queue", task.String()[:8]) },
			"Waiting on registered queue ", nil},
		{"gmail", nil, func(uuid.UUID) WaitForInput { return reg("gmail", "gmr_0123456789ab") }, "Waiting on registered gmail gmr_0123456789ab.", nil},
		{"ops-review", nil, func(uuid.UUID) WaitForInput { return reg("ops-review", sha64) }, "", nil},
		{"native", nil, func(uuid.UUID) WaitForInput { return reg("native", sha64) }, "", nil},
		{"mr merged after an opened head", liveGitLab(), func(uuid.UUID) WaitForInput {
			in := mr("merged")
			in.AfterSHA, in.AfterStatus = ptr(sha40), ptr("opened")
			return in
		}, "Waiting on MR g/p!10 until merged after 0123456789ab (opened).",
			func(t *testing.T, _ uuid.UUID, w *domain.TaskWait) {
				assert.Equal(t, "opened", *w.AfterStatus)
				assert.Nil(t, w.AfterPipelineID)
			}},
		{"mr changed with a CI baseline and jobs", liveGitLab(), func(uuid.UUID) WaitForInput {
			in := ciBaseline(mr("changed"), 7001, "failed")
			in.RequiredJobs = []string{"test"}
			return in
		}, "Waiting on MR g/p!10 until changed (jobs: test) after 0123456789ab (pipeline 7001 failed).",
			func(t *testing.T, _ uuid.UUID, w *domain.TaskWait) {
				assert.Equal(t, int64(7001), *w.AfterPipelineID)
				assert.Equal(t, []string{"test"}, []string(w.RequiredJobs))
			}},
		{"mr merged keeps ignoring jobs", liveGitLab(), func(uuid.UUID) WaitForInput {
			in := mr("merged")
			in.RequiredJobs = []string{"test"}
			return in
		}, "Waiting on MR g/p!10 until merged.",
			func(t *testing.T, _ uuid.UUID, w *domain.TaskWait) { assert.Empty(t, w.RequiredJobs) }},
		{"pipeline changed with its own baseline", liveGitLab(), func(uuid.UUID) WaitForInput { return ciBaseline(pipe("changed"), 500, "running") },
			"Waiting on pipeline g/p#500 until changed after 0123456789ab (pipeline 500 running).", nil},
		{"GitHub Actions run, unconfirmed", func(context.Context, uuid.UUID) (gitlabapi.MergeRequestChecker, bool) {
			t.Error("a GitHub run was looked up on GitLab")
			return nil, false
		}, func(uuid.UUID) WaitForInput {
			in := WaitForInput{On: "pipeline", Provider: ptr("github"), ProjectPath: ptr("org/repo"), PipelineID: ptr(int64(42)), Until: ptr("green")}
			return ciBaseline(in, 42, "cancelled")
		}, "Waiting on GitHub Actions run org/repo#42 until green after 0123456789ab (pipeline 42 cancelled).",
			func(t *testing.T, _ uuid.UUID, w *domain.TaskWait) { assert.Equal(t, "github", *w.Provider) }},
	}
	for _, tc := range accepted {
		t.Run("accepted/"+tc.name, func(t *testing.T) {
			e := newTurnEnv(tc.gl)
			res, err := e.svc.WaitFor(e.ctx, e.task, tc.in(e.task))
			require.NoError(t, err)
			assert.True(t, res.Released)
			require.Len(t, e.store.committed, 1)
			cmd := e.store.committed[0]
			assert.Equal(t, domain.TurnOutcomeWait, cmd.Kind)
			if tc.comment != "" {
				assert.True(t, strings.HasPrefix(cmd.CommentBody, tc.comment), cmd.CommentBody)
			}
			assert.Equal(t, strings.TrimSuffix(strings.TrimPrefix(strings.SplitN(cmd.CommentBody, "\n", 2)[0], "Waiting on "), "."), cmd.RuntimeReason)
			if tc.check != nil {
				tc.check(t, e.task, cmd.Wait)
			}
		})
	}
}

// labelledTasks is the card reader of a manual wait: the card and its labels.
type labelledTasks map[uuid.UUID][]string

func (l labelledTasks) GetByID(_ context.Context, id uuid.UUID) (*domain.Task, error) {
	labels, ok := l[id]
	if !ok {
		return nil, nil
	}
	return &domain.Task{ID: id, Labels: labels}, nil
}

// A manual wait (`WAIT manual:#<8hex>`) is recorded only on the card itself
// and only while a lead's wake:manual label is on it.
func TestTurnCommandService_WaitForManual(t *testing.T) {
	why := "vendor answers by mail; the lead resumes the card"
	env := func(labels ...string) *turnEnv {
		e := newTurnEnv(nil)
		e.svc.WithTasks(labelledTasks{e.task: labels})
		return e
	}

	t.Run("accepted with the label", func(t *testing.T) {
		e := env("kind:ops", domain.LabelWakeManual)
		res, err := e.svc.WaitFor(e.ctx, e.task, WaitForInput{On: "manual", Note: why})
		require.NoError(t, err)
		assert.True(t, res.Released)
		require.Len(t, e.store.committed, 1)
		cmd := e.store.committed[0]
		assert.Equal(t, domain.TurnOutcomeWait, cmd.Kind)
		assert.Equal(t, domain.TaskWaitManual, cmd.Wait.Kind)
		assert.Nil(t, cmd.Wait.TargetTaskID)
		assert.Nil(t, cmd.Wait.UntilAt)
		assert.Equal(t, why, cmd.Wait.Note)
		assert.Equal(t, "Waiting on a comment or a lead's action (wake:manual).\n\n"+why, cmd.CommentBody)
		assert.Equal(t, "a comment or a lead's action (wake:manual)", cmd.RuntimeReason)
	})

	t.Run("accepted naming its own card", func(t *testing.T) {
		e := env(domain.LabelWakeManual)
		_, err := e.svc.WaitFor(e.ctx, e.task, WaitForInput{On: "manual", TargetTaskID: &e.task, Note: why})
		require.NoError(t, err)
		require.Len(t, e.store.committed, 1)
		assert.Nil(t, e.store.committed[0].Wait.TargetTaskID, "the card itself is not stored as a card target")
	})

	refused := []struct {
		name string
		e    func() *turnEnv
		in   func(e *turnEnv) WaitForInput
		msg  string
	}{
		{"without the label", func() *turnEnv { return env("kind:ops", "wake:date") },
			func(*turnEnv) WaitForInput { return WaitForInput{On: "manual", Note: why} }, "needs the label wake:manual"},
		{"on another card", func() *turnEnv { return env(domain.LabelWakeManual) },
			func(*turnEnv) WaitForInput {
				other := uuid.New()
				return WaitForInput{On: "manual", TargetTaskID: &other, Note: why}
			},
			"on the waiting card itself"},
		{"without why", func() *turnEnv { return env(domain.LabelWakeManual) },
			func(*turnEnv) WaitForInput { return WaitForInput{On: "manual"} }, "needs note"},
		{"with a timer", func() *turnEnv { return env(domain.LabelWakeManual) },
			func(*turnEnv) WaitForInput { return WaitForInput{On: "manual", Until: ptr("6h"), Note: why} }, "no observer and no timer"},
		{"with a baseline", func() *turnEnv { return env(domain.LabelWakeManual) },
			func(*turnEnv) WaitForInput { return WaitForInput{On: "manual", AfterSHA: ptr(sha40), Note: why} }, "after_sha is not a field of an on=manual wait"},
		{"labels unreadable", func() *turnEnv { return newTurnEnv(nil) },
			func(*turnEnv) WaitForInput { return WaitForInput{On: "manual", Note: why} }, "labels are unavailable"},
	}
	for _, tc := range refused {
		t.Run("refused/"+tc.name, func(t *testing.T) {
			e := tc.e()
			_, err := e.svc.WaitFor(e.ctx, e.task, tc.in(e))
			assert.Equal(t, domain.CodeInvalidTarget, outcomeCode(t, err))
			assert.Contains(t, err.Error(), tc.msg)
			assert.Empty(t, e.store.committed, "a refused wait reached the store")
		})
	}
}

// A GitHub PR merge is waited on as on=mr provider=github until=merged with
// the opened-head baseline; on=merge refuses GitHub and names that route.
func TestTurnCommandService_WaitForGitHubMerged(t *testing.T) {
	gh := func(context.Context, uuid.UUID) (githubapi.PullRequestChecker, bool) {
		return gitHubStub{"org/repo#7": {State: "open"}, "org/repo#8": {State: "closed", Merged: true}}, true
	}
	in := func(n int) WaitForInput {
		return WaitForInput{On: "mr", Provider: ptr("github"), ProjectPath: ptr("org/repo"), MRIID: ptr(n),
			Until: ptr("merged"), AfterSHA: ptr(sha40), AfterStatus: ptr("opened")}
	}

	t.Run("accepted", func(t *testing.T) {
		e := newTurnEnv(nil)
		e.svc.WithGitHub(gh)
		_, err := e.svc.WaitFor(e.ctx, e.task, in(7))
		require.NoError(t, err)
		require.Len(t, e.store.committed, 1)
		w := e.store.committed[0].Wait
		assert.Equal(t, domain.TaskWaitMR, w.Kind)
		assert.Equal(t, "github", *w.Provider)
		assert.Equal(t, "merged", *w.Until)
		assert.Equal(t, sha40, *w.AfterSHA)
		assert.Equal(t, "opened", *w.AfterStatus)
		assert.Equal(t, "Waiting on MR org/repo!7 until merged after 0123456789ab (opened).", e.store.committed[0].CommentBody)
	})

	t.Run("refused when already merged", func(t *testing.T) {
		e := newTurnEnv(nil)
		e.svc.WithGitHub(gh)
		_, err := e.svc.WaitFor(e.ctx, e.task, in(8))
		assert.Equal(t, domain.CodeAlreadyMet, outcomeCode(t, err))
		assert.Empty(t, e.store.committed)
	})

	t.Run("merge on GitHub names the mr route", func(t *testing.T) {
		e := newTurnEnv(nil)
		e.svc.WithGitHub(gh)
		_, err := e.svc.WaitFor(e.ctx, e.task, WaitForInput{On: "merge", Provider: ptr("github"), ProjectPath: ptr("org/repo"),
			MRIID: ptr(7), AfterSHA: ptr(sha40)})
		assert.Equal(t, domain.CodeInvalidTarget, outcomeCode(t, err))
		assert.Contains(t, err.Error(), "on=mr provider=github until=merged")
		assert.Empty(t, e.store.committed)
	})
}
