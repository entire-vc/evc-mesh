package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	headSHA40 = "0123456789abcdef0123456789abcdef01234567"
	headSHA64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// The merge, registered, baseline and GitHub-pipeline shapes of
// TaskWait.Validate: every accepted form and every refusal, by its message.
func TestTaskWaitValidate_TypedKinds(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	task := uuid.MustParse("0a1b2c3d-0000-4000-8000-000000000001")
	own := "0a1b2c3d"
	str := func(s string) *string { return &s }
	i64 := func(n int64) *int64 { return &n }
	iid, zero := 12, 0
	pipe := int64(7001)

	merge := func(edit func(w *TaskWait)) TaskWait {
		w := TaskWait{TaskID: task, Kind: TaskWaitMerge, ProjectPath: str("g/p"), MRIID: &iid, AfterSHA: str(headSHA40)}
		if edit != nil {
			edit(&w)
		}
		return w
	}
	reg := func(r string, ref *string, edit func(w *TaskWait)) TaskWait {
		w := TaskWait{TaskID: task, Kind: TaskWaitRegistered, Registration: str(r), RegistrationRef: ref}
		if edit != nil {
			edit(&w)
		}
		return w
	}
	mr := func(until string, edit func(w *TaskWait)) TaskWait {
		w := TaskWait{TaskID: task, Kind: TaskWaitMR, Provider: str("gitlab"), ProjectPath: str("g/p"), MRIID: &iid, Until: str(until)}
		if edit != nil {
			edit(&w)
		}
		return w
	}
	pipeline := func(until string, edit func(w *TaskWait)) TaskWait {
		w := TaskWait{TaskID: task, Kind: TaskWaitPipeline, ProjectPath: str("g/p"), PipelineID: &pipe, Until: str(until)}
		if edit != nil {
			edit(&w)
		}
		return w
	}
	ci := func(id int64, status string) func(w *TaskWait) {
		return func(w *TaskWait) { w.AfterSHA, w.AfterPipelineID, w.AfterStatus = str(headSHA40), i64(id), str(status) }
	}

	ok := map[string]TaskWait{
		"merge":                    merge(nil),
		"merge 64-hex head":        merge(func(w *TaskWait) { w.AfterSHA = str(headSHA64) }),
		"merge on gitlab":          merge(func(w *TaskWait) { w.Provider = str("gitlab") }),
		"ops-review":               reg(RegistrationOpsReview, str(headSHA64), nil),
		"native":                   reg(RegistrationNative, str(headSHA64), nil),
		"gmail":                    reg(RegistrationGmail, str("gmr_0123456789ab"), nil),
		"receipt with own id":      reg(RegistrationReceipt, str(own), nil),
		"lifecycle with own id":    reg(RegistrationLifecycle, str(own), nil),
		"stall, ref omitted":       reg(RegistrationStall, nil, nil),
		"recovery, ref empty":      reg(RegistrationRecovery, str(""), nil),
		"queue, ref omitted":       reg(RegistrationQueue, nil, nil),
		"mr merged after opened":   mr(WaitUntilMerged, func(w *TaskWait) { w.AfterSHA, w.AfterStatus = str(headSHA40), str(MRBaselineOpened) }),
		"mr green CI baseline":     mr(WaitUntilGreen, ci(42, "running")),
		"mr changed CI baseline":   mr(WaitUntilChanged, ci(42, "cancelled")),
		"mr without baseline":      mr(WaitUntilChanged, nil),
		"pipeline changed":         pipeline(WaitUntilChanged, nil),
		"pipeline own baseline":    pipeline(WaitUntilGreen, ci(pipe, "manual")),
		"pipeline on gitlab":       pipeline(WaitUntilFinished, func(w *TaskWait) { w.Provider = str("gitlab") }),
		"GitHub Actions run":       pipeline(WaitUntilGreen, func(w *TaskWait) { w.Provider, w.ProjectPath = str("github"), str("org/repo") }),
		"GitHub run with baseline": pipeline(WaitUntilChanged, func(w *TaskWait) { w.Provider, w.ProjectPath = str("github"), str("org/repo"); ci(pipe, "success")(w) }),
	}
	other := uuid.MustParse("0a1b2c3d-0000-4000-8000-000000000002")
	manual := func(edit func(w *TaskWait)) TaskWait {
		w := TaskWait{TaskID: task, Kind: TaskWaitManual, Note: "lead parks it until the vendor answers"}
		if edit != nil {
			edit(&w)
		}
		return w
	}
	ok["manual"] = manual(nil)
	ok["manual naming its own card"] = manual(func(w *TaskWait) { w.TargetTaskID = &task })
	ok["GitHub PR merged after opened"] = mr(WaitUntilMerged, func(w *TaskWait) {
		w.Provider, w.ProjectPath, w.AfterSHA, w.AfterStatus = str("github"), str("org/repo"), str(headSHA40), str(MRBaselineOpened)
	})
	for name, w := range ok {
		t.Run("ok/"+name, func(t *testing.T) {
			assert.NoError(t, w.Validate(now))
		})
	}

	bad := map[string]struct {
		w   TaskWait
		msg string
	}{
		"merge without project":     {merge(func(w *TaskWait) { w.ProjectPath = nil }), "needs project_path"},
		"merge without group":       {merge(func(w *TaskWait) { w.ProjectPath = str("p") }), "needs project_path"},
		"merge without iid":         {merge(func(w *TaskWait) { w.MRIID = nil }), "needs project_path"},
		"merge with iid 0":          {merge(func(w *TaskWait) { w.MRIID = &zero }), "needs project_path"},
		"merge on github":           {merge(func(w *TaskWait) { w.Provider = str("github") }), "on=mr provider=github until=merged"},
		"merge without sha":         {merge(func(w *TaskWait) { w.AfterSHA = nil }), "needs after_sha"},
		"merge with short sha":      {merge(func(w *TaskWait) { w.AfterSHA = str("abc") }), "needs after_sha"},
		"merge with uppercase sha":  {merge(func(w *TaskWait) { w.AfterSHA = str(strings.ToUpper(headSHA40)) }), "needs after_sha"},
		"merge with until":          {merge(func(w *TaskWait) { w.Until = str(WaitUntilMerged) }), "only"},
		"merge with pipeline":       {merge(func(w *TaskWait) { w.AfterPipelineID = i64(1) }), "only"},
		"merge with status":         {merge(func(w *TaskWait) { w.AfterStatus = str("opened") }), "only"},
		"merge with jobs":           {merge(func(w *TaskWait) { w.RequiredJobs = []string{"test"} }), "only"},
		"no registration":           {reg("", nil, func(w *TaskWait) { w.Registration = nil }), "registration must be one of"},
		"empty registration":        {reg("", nil, nil), "registration must be one of"},
		"unknown registration":      {reg("poll", str("x"), nil), "registration must be one of"},
		"receipt of another card":   {reg(RegistrationReceipt, str("deadbeef"), nil), "armed for this card"},
		"queue of another card":     {reg(RegistrationQueue, str(task.String()), nil), "armed for this card"},
		"ops-review without ref":    {reg(RegistrationOpsReview, nil, nil), "64 hex digits"},
		"ops-review with 40 hex":    {reg(RegistrationOpsReview, str(headSHA40), nil), "64 hex digits"},
		"native uppercase":          {reg(RegistrationNative, str(strings.ToUpper(headSHA64)), nil), "64 hex digits"},
		"gmail short":               {reg(RegistrationGmail, str("gmr_0123"), nil), "gmr_ and 12 hex digits"},
		"gmail without prefix":      {reg(RegistrationGmail, str("0123456789ab"), nil), "gmr_ and 12 hex digits"},
		"registered with until":     {reg(RegistrationGmail, str("gmr_0123456789ab"), func(w *TaskWait) { w.Until = str("green") }), "registration_ref only"},
		"registered with sha":       {reg(RegistrationStall, nil, func(w *TaskWait) { w.AfterSHA = str(headSHA40) }), "registration_ref only"},
		"registered with pipeline":  {reg(RegistrationNative, str(headSHA64), func(w *TaskWait) { w.AfterPipelineID = i64(1) }), "registration_ref only"},
		"registered with status":    {reg(RegistrationNative, str(headSHA64), func(w *TaskWait) { w.AfterStatus = str("running") }), "registration_ref only"},
		"registered with jobs":      {reg(RegistrationNative, str(headSHA64), func(w *TaskWait) { w.RequiredJobs = []string{"x"} }), "registration_ref only"},
		"baseline without sha":      {mr(WaitUntilGreen, func(w *TaskWait) { w.AfterPipelineID, w.AfterStatus = i64(1), str("running") }), "baseline needs after_sha"},
		"baseline short sha":        {mr(WaitUntilMerged, func(w *TaskWait) { w.AfterSHA, w.AfterStatus = str("abc"), str("opened") }), "baseline needs after_sha"},
		"only a baseline status":    {mr(WaitUntilMerged, func(w *TaskWait) { w.AfterStatus = str("opened") }), "baseline needs after_sha"},
		"merged without status":     {mr(WaitUntilMerged, func(w *TaskWait) { w.AfterSHA = str(headSHA40) }), "after_status opened"},
		"merged with CI status":     {mr(WaitUntilMerged, func(w *TaskWait) { w.AfterSHA, w.AfterStatus = str(headSHA40), str("running") }), "after_status opened"},
		"merged with a pipeline":    {mr(WaitUntilMerged, ci(42, "opened")), "after_status opened"},
		"CI without pipeline":       {mr(WaitUntilGreen, func(w *TaskWait) { w.AfterSHA, w.AfterStatus = str(headSHA40), str("running") }), "after_pipeline_id"},
		"CI with pipeline 0":        {mr(WaitUntilGreen, ci(0, "running")), "after_pipeline_id"},
		"CI without status":         {mr(WaitUntilChanged, func(w *TaskWait) { w.AfterSHA, w.AfterPipelineID = str(headSHA40), i64(42) }), "after_status"},
		"CI with unknown status":    {mr(WaitUntilChanged, ci(42, "opened")), "a pipeline status"},
		"pipeline other baseline":   {pipeline(WaitUntilChanged, ci(pipe+1, "failed")), "must be the awaited pipeline_id"},
		"finished with a baseline":  {pipeline(WaitUntilFinished, ci(pipe, "running")), "takes no baseline"},
		"pipeline merged":           {pipeline(WaitUntilMerged, nil), "finished, green or changed"},
		"pipeline unknown provider": {pipeline(WaitUntilGreen, func(w *TaskWait) { w.Provider = str("bitbucket") }), "gitlab or github"},
		"GitHub run, bare repo":     {pipeline(WaitUntilGreen, func(w *TaskWait) { w.Provider, w.ProjectPath = str("github"), str("repo") }), "owner/repo"},
		"GitHub run, nested path":   {pipeline(WaitUntilGreen, func(w *TaskWait) { w.Provider, w.ProjectPath = str("github"), str("a/b/c") }), "owner/repo"},
		"human question too long": {TaskWait{TaskID: task, Kind: TaskWaitHuman, HumanGateRef: str("comment:x"), Addressee: str("alex"),
			Question: str(strings.Repeat("я", MaxHumanQuestionLen+1))}, "longer than 2000"},
	}
	bad["manual on another card"] = struct {
		w   TaskWait
		msg string
	}{manual(func(w *TaskWait) { w.TargetTaskID = &other }), "on the waiting card itself"}
	bad["manual without why"] = struct {
		w   TaskWait
		msg string
	}{manual(func(w *TaskWait) { w.Note = "  " }), "needs note"}
	bad["manual with a timer"] = struct {
		w   TaskWait
		msg string
	}{manual(func(w *TaskWait) { at := now.Add(time.Hour); w.UntilAt = &at }), "no observer and no timer"}
	bad["manual with until"] = struct {
		w   TaskWait
		msg string
	}{manual(func(w *TaskWait) { w.Until = str(WaitUntilMerged) }), "note only"}
	bad["manual with an MR"] = struct {
		w   TaskWait
		msg string
	}{manual(func(w *TaskWait) { w.ProjectPath, w.MRIID = str("g/p"), &iid }), "note only"}
	for name, tc := range bad {
		t.Run("refused/"+name, func(t *testing.T) {
			err := tc.w.Validate(now)
			require.Error(t, err)
			assert.Equal(t, CodeInvalidTarget, outcomeCode(t, err))
			assert.Contains(t, err.Error(), tc.msg)
		})
	}

	t.Run("a card-scoped ref is filled with this card's id", func(t *testing.T) {
		for _, r := range []string{RegistrationReceipt, RegistrationLifecycle, RegistrationStall, RegistrationRecovery, RegistrationQueue} {
			w := reg(r, nil, nil)
			require.NoError(t, w.Validate(now))
			require.NotNil(t, w.RegistrationRef)
			assert.Equal(t, own, *w.RegistrationRef, r)
		}
	})

	t.Run("a human question of exactly 2000 runes is accepted", func(t *testing.T) {
		w := TaskWait{TaskID: task, Kind: TaskWaitHuman, HumanGateRef: str("comment:x"), Addressee: str("alex"),
			Question: str(strings.Repeat("я", MaxHumanQuestionLen))}
		assert.NoError(t, w.Validate(now))
	})
}

func TestRegistrations(t *testing.T) {
	all := Registrations()
	assert.Equal(t, []string{"ops-review", "gmail", "receipt", "lifecycle", "stall", "recovery", "queue", "native"}, all)
	scoped := map[string]bool{}
	for _, r := range all {
		scoped[r] = CardScopedRegistration(r)
	}
	assert.Equal(t, map[string]bool{"ops-review": false, "gmail": false, "receipt": true, "lifecycle": true,
		"stall": true, "recovery": true, "queue": true, "native": false}, scoped)
	assert.False(t, CardScopedRegistration("poll"))
	for _, r := range all {
		if !CardScopedRegistration(r) {
			assert.NotNil(t, registrationRefShape[r], "%s has no ref shape", r)
		}
	}
}

func TestWaitResolution_ResolvableByRoute(t *testing.T) {
	for r, want := range map[WaitResolution]bool{
		WaitResolutionMet: true, WaitResolutionCancelled: true, WaitResolutionExpired: true, WaitResolutionInvalid: true,
		WaitResolutionSuperseded: false, "": false, "done": false,
	} {
		assert.Equal(t, want, r.ResolvableByRoute(), "%q", r)
	}
}
