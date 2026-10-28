package domain

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func outcomeCode(t *testing.T, err error) string {
	t.Helper()
	var oe *OutcomeError
	require.True(t, errors.As(err, &oe), "want *OutcomeError, got %v", err)
	return oe.Code
}

func TestTaskWaitValidate(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	task := uuid.New()
	other := uuid.New()
	str := func(s string) *string { return &s }
	iid := 42
	pipe := int64(7001)
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	dr := uuid.New()

	ok := []TaskWait{
		{TaskID: task, Kind: TaskWaitCard, TargetTaskID: &other},
		{TaskID: task, Kind: TaskWaitCard, TargetTaskID: &other, TargetCondition: str("shipped")},
		{TaskID: task, Kind: TaskWaitMR, Provider: str("gitlab"), ProjectPath: str("entire-vc/evc-mesh"), MRIID: &iid, Until: str(WaitUntilMerged)},
		{TaskID: task, Kind: TaskWaitPipeline, ProjectPath: str("entire-vc/evc-mesh"), PipelineID: &pipe, Until: str(WaitUntilGreen)},
		{TaskID: task, Kind: TaskWaitTime, UntilAt: at(MinTimeWait)},
		{TaskID: task, Kind: TaskWaitTime, UntilAt: at(MaxTimeWait)},
		{TaskID: task, Kind: TaskWaitDecision, DecisionRequestID: &dr},
		{TaskID: task, Kind: TaskWaitHuman, HumanGateRef: str("gate-1")},
	}
	for _, w := range ok {
		assert.NoError(t, w.Validate(now), "kind %s", w.Kind)
	}

	bad := []struct {
		name string
		w    TaskWait
		code string
	}{
		{"self", TaskWait{TaskID: task, Kind: TaskWaitCard, TargetTaskID: &task}, CodeSelfWait},
		{"card no target", TaskWait{TaskID: task, Kind: TaskWaitCard}, CodeInvalidTarget},
		{"card bad condition", TaskWait{TaskID: task, Kind: TaskWaitCard, TargetTaskID: &other, TargetCondition: str("closed")}, CodeInvalidTarget},
		{"mr no iid", TaskWait{TaskID: task, Kind: TaskWaitMR, Provider: str("gitlab"), ProjectPath: str("p"), Until: str(WaitUntilMerged)}, CodeInvalidTarget},
		{"mr pipeline-only until", TaskWait{TaskID: task, Kind: TaskWaitMR, Provider: str("gitlab"), ProjectPath: str("p"), MRIID: &iid, Until: str(WaitUntilFinished)}, CodeInvalidTarget},
		{"pipeline mr-only until", TaskWait{TaskID: task, Kind: TaskWaitPipeline, ProjectPath: str("p"), PipelineID: &pipe, Until: str(WaitUntilMerged)}, CodeInvalidTarget},
		{"time too soon", TaskWait{TaskID: task, Kind: TaskWaitTime, UntilAt: at(MinTimeWait - time.Second)}, CodeTimeOutOfBounds},
		{"time too far", TaskWait{TaskID: task, Kind: TaskWaitTime, UntilAt: at(MaxTimeWait + time.Second)}, CodeTimeOutOfBounds},
		{"time in past", TaskWait{TaskID: task, Kind: TaskWaitTime, UntilAt: at(-time.Hour)}, CodeTimeOutOfBounds},
		{"decision no request", TaskWait{TaskID: task, Kind: TaskWaitDecision}, CodeInvalidTarget},
		{"human no ref", TaskWait{TaskID: task, Kind: TaskWaitHuman, HumanGateRef: str("")}, CodeInvalidTarget},
		{"unknown kind", TaskWait{TaskID: task, Kind: "someday"}, CodeInvalidTarget},
		{"long note", TaskWait{TaskID: task, Kind: TaskWaitCard, TargetTaskID: &other, Note: string(make([]byte, MaxWaitNoteLen+1))}, CodeInvalidTarget},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.code, outcomeCode(t, tc.w.Validate(now)))
		})
	}
}

func TestResultKindValid(t *testing.T) {
	for _, k := range []ResultKind{ResultKindChange, ResultKindDefect, ResultKindResearch, ResultKindContent, ResultKindOps} {
		assert.True(t, k.Valid(), k)
	}
	assert.False(t, ResultKind("").Valid())
	assert.False(t, ResultKind("monitor").Valid())
}

func TestCommentWakes(t *testing.T) {
	cases := map[string]bool{
		``:                       true,
		`null`:                   true,
		`{}`:                     true,
		`{"wakes":null}`:         true,
		`{"wakes":true}`:         true,
		`{"wakes":false}`:        false,
		`{"informational":true}`: true,
		`{"wakes":"false"}`:      true, // not a bool: the default holds
		`not json`:               true,
	}
	for meta, want := range cases {
		assert.Equal(t, want, CommentWakes(json.RawMessage(meta)), "metadata %q", meta)
	}
}
