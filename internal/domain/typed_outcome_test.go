package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
	iid := 42
	pipe := int64(7001)
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	dr := uuid.New()
	timed := func(d time.Duration, src string) TaskWait {
		return TaskWait{TaskID: task, Kind: TaskWaitTime, UntilAt: at(d), EventRef: str("nightly-export"),
			Source: str(src), Why: str("the export job publishes the file at this time"), UntilAtOffset: str("+03:00")}
	}
	noEvidence := func(edit func(w *TaskWait)) TaskWait {
		w := timed(time.Hour, "https://status.example.com/jobs")
		edit(&w)
		return w
	}

	ok := []TaskWait{
		{TaskID: task, Kind: TaskWaitCard, TargetTaskID: &other},
		{TaskID: task, Kind: TaskWaitCard, TargetTaskID: &other, TargetCondition: str("shipped")},
		{TaskID: task, Kind: TaskWaitMR, Provider: str("gitlab"), ProjectPath: str("entire-vc/evc-mesh"), MRIID: &iid, Until: str(WaitUntilMerged)},
		{TaskID: task, Kind: TaskWaitPipeline, ProjectPath: str("entire-vc/evc-mesh"), PipelineID: &pipe, Until: str(WaitUntilGreen)},
		timed(MinTimeWait, "https://status.example.com/jobs/42"),
		timed(MaxTimeWait, "comment:"+uuid.NewString()),
		timed(time.Hour, "artifact:"+uuid.NewString()),
		{TaskID: task, Kind: TaskWaitDecision, DecisionRequestID: &dr},
		{TaskID: task, Kind: TaskWaitHuman, HumanGateRef: str("gate-1"), Addressee: str("alex"), Question: str("ship it?")},
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
		{"time without evidence", TaskWait{TaskID: task, Kind: TaskWaitTime, UntilAt: at(time.Hour)}, CodeMissingEvidence},
		{"time no event_ref", noEvidence(func(w *TaskWait) { w.EventRef = nil }), CodeMissingEvidence},
		{"time no source", noEvidence(func(w *TaskWait) { w.Source = str("") }), CodeMissingEvidence},
		{"time no why", noEvidence(func(w *TaskWait) { w.Why = nil }), CodeMissingEvidence},
		{"time placeholder why", noEvidence(func(w *TaskWait) { w.Why = str("retry") }), CodeMissingEvidence},
		{"time no offset", noEvidence(func(w *TaskWait) { w.UntilAtOffset = nil }), CodeMissingEvidence},
		{"time http source", noEvidence(func(w *TaskWait) { w.Source = str("http://status.example.com") }), CodeMissingEvidence},
		{"time card ref as source", noEvidence(func(w *TaskWait) { w.Source = str("card:00000000") }), CodeMissingEvidence},
		{"time comment without uuid", noEvidence(func(w *TaskWait) { w.Source = str("comment:latest") }), CodeMissingEvidence},
		{"time relative until", noEvidence(func(w *TaskWait) { w.Until = str("6h") }), CodeNaiveTime},
		{"time relative until only", TaskWait{TaskID: task, Kind: TaskWaitTime, Until: str("6h")}, CodeNaiveTime},
		{"decision no request", TaskWait{TaskID: task, Kind: TaskWaitDecision}, CodeInvalidTarget},
		{"human no ref", TaskWait{TaskID: task, Kind: TaskWaitHuman, HumanGateRef: str("")}, CodeInvalidTarget},
		{"human no question", TaskWait{TaskID: task, Kind: TaskWaitHuman, HumanGateRef: str("comment:x"), Addressee: str("alex")}, CodeInvalidTarget},
		{"merge no sha", TaskWait{TaskID: task, Kind: TaskWaitMerge, ProjectPath: str("g/p"), MRIID: &iid}, CodeInvalidTarget},
		{"registered unknown", TaskWait{TaskID: task, Kind: TaskWaitRegistered, Registration: str("poll"), RegistrationRef: str("x")}, CodeInvalidTarget},
		{"unknown kind", TaskWait{TaskID: task, Kind: "someday"}, CodeInvalidTarget},
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

func TestClipRunes(t *testing.T) {
	assert.Equal(t, "", ClipRunes("abc", 0))
	assert.Equal(t, "abc", ClipRunes("abc", 3))
	assert.Equal(t, "ab", ClipRunes("abc", 2))
	assert.Equal(t, "яя", ClipRunes("яяя", 2))
	assert.Equal(t, "a😀", ClipRunes("a😀b", 2))
}

// A long note never refuses the wait: Validate keeps its first MaxWaitNoteLen
// characters, counted as the column's char_length counts them.
func TestTaskWaitValidateClipsLongNote(t *testing.T) {
	task, other := uuid.New(), uuid.New()
	for name, tc := range map[string]struct {
		note string
		keep int
	}{
		"2000 ascii":   {strings.Repeat("a", 2000), MaxWaitNoteLen},
		"2000 unicode": {strings.Repeat("я", 2000), MaxWaitNoteLen},
		"251 unicode":  {strings.Repeat("я", 251), 251},
		"500 ascii":    {strings.Repeat("a", 500), 500},
	} {
		t.Run(name, func(t *testing.T) {
			w := TaskWait{TaskID: task, Kind: TaskWaitCard, TargetTaskID: &other, Note: tc.note}
			require.NoError(t, w.Validate(time.Now()))
			assert.Equal(t, tc.keep, utf8.RuneCountInString(w.Note))
			assert.True(t, utf8.ValidString(w.Note))
			assert.True(t, strings.HasPrefix(tc.note, w.Note))
		})
	}
}

// The refusals the handler table cannot reach without a card target name
// their field too.
func TestTaskWaitValidateRefusalsNameTheField(t *testing.T) {
	task, other := uuid.New(), uuid.New()
	iid := 1
	sha := "0123456789abcdef0123456789abcdef01234567"
	for field, w := range map[string]TaskWait{
		"target_task_id":   {TaskID: task, Kind: TaskWaitCard, TargetTaskID: &task},
		"target_condition": {TaskID: task, Kind: TaskWaitCard, TargetTaskID: &other, TargetCondition: str("closed")},
		"provider":         {TaskID: task, Kind: TaskWaitMerge, Provider: str("github"), ProjectPath: str("o/r"), MRIID: &iid, AfterSHA: &sha},
	} {
		t.Run(field, func(t *testing.T) {
			err := w.Validate(time.Now())
			require.Error(t, err)
			assert.Contains(t, err.Error(), field)
		})
	}
	manual := TaskWait{TaskID: task, Kind: TaskWaitManual, TargetTaskID: &other, Note: "lead resumes"}
	assert.Contains(t, manual.Validate(time.Now()).Error(), "target_task_id")

	// A field the kind does not take is named, not just the fields it does take.
	for want, w := range map[string]TaskWait{
		"target_condition is not a field of a manual wait": {TaskID: task, Kind: TaskWaitManual, Note: "lead resumes", TargetCondition: str("shipped")},
		"until_at is not a field of a manual wait":         {TaskID: task, Kind: TaskWaitManual, Note: "lead resumes", UntilAt: &time.Time{}},
		"until is not a field of a registered wait":        {TaskID: task, Kind: TaskWaitRegistered, Registration: str(RegistrationReceipt), Until: str("merged")},
		"required_jobs is not a field of a merge wait": {TaskID: task, Kind: TaskWaitMerge, ProjectPath: str("g/p"), MRIID: &iid, AfterSHA: &sha,
			RequiredJobs: []string{"test"}},
	} {
		err := w.Validate(time.Now())
		require.Error(t, err, want)
		assert.Contains(t, err.Error(), want)
	}
}
