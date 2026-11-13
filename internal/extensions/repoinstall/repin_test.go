package repoinstall

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

var (
	shaA = strings.Repeat("a", 40)
	shaB = strings.Repeat("b", 40)
	shaC = strings.Repeat("c", 40)
)

// repinReq builds an apply request for a commit-SHA ref: the planned target
// commit is the ref itself.
func repinReq(row *domain.InstalledExtension, ref string) RepinRequest {
	return RepinRequest{Ref: ref, ExpectedVersion: row.Version, ExpectedCommitSHA: row.CommitSHA, ExpectedTargetCommitSHA: ref}
}

func installed(t *testing.T, svc *Service, id string) *domain.InstalledExtension {
	t.Helper()
	row, err := svc.Installed.Get(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, row)
	return row
}

func TestRepin_ShaToAnotherSha(t *testing.T) {
	svc, repo, gl, url := setup(t, "")
	gl.set(shaA, shaA, manifestJSON(t, withVersion("0.3.0")))
	gl.set(shaB, shaB, manifestJSON(t, withVersion("0.3.1")))
	ctx := context.Background()
	_, err := svc.Install(ctx, uuid.New(), url, shaA, "")
	require.NoError(t, err)
	row := installed(t, svc, "hello")

	plan, err := svc.PlanRepin(ctx, "hello", RepinRequest{Ref: shaB})
	require.NoError(t, err)
	assert.Equal(t, PinCommit, plan.Current.PinKind)
	assert.Equal(t, PinCommit, plan.Target.PinKind)
	assert.Equal(t, shaB, plan.Target.CommitSHA)
	assert.Equal(t, "0.3.1", plan.Target.Version)
	assert.True(t, plan.Changed)
	assert.Contains(t, plan.AutoUpdate, "never")
	assert.Equal(t, shaA, installed(t, svc, "hello").CommitSHA, "a plan writes nothing")

	res, err := svc.Repin(ctx, uuid.New(), "hello", repinReq(row, shaB))
	require.NoError(t, err)
	assert.Equal(t, "repinned", res.Outcome)
	after := installed(t, svc, "hello")
	assert.Equal(t, shaB, after.RepoRef)
	assert.Equal(t, shaB, after.CommitSHA)
	assert.Equal(t, "0.3.1", after.Version)
	assert.Equal(t, "0.3.0", after.PreviousVersion, "same URL: the old pin stays reachable by rollback")
	assert.Equal(t, []string{"repinned"}, repo.journal)

	// The same apply again: the expected state is gone, nothing is written.
	_, err = svc.Repin(ctx, uuid.New(), "hello", repinReq(row, shaB))
	assert.Equal(t, CodeStateChanged, errCode(err))
	assert.Len(t, repo.journal, 1)

	// Planned against the new state, the same pin is up to date and writes nothing.
	res, err = svc.Repin(ctx, uuid.New(), "hello", repinReq(after, shaB))
	require.NoError(t, err)
	assert.Equal(t, string(OutcomeUpToDate), res.Outcome)
	assert.Len(t, repo.journal, 1)
}

func TestRepin_TagToSha(t *testing.T) {
	svc, _, gl, url := setup(t, "")
	gl.tags = []string{"v0.3.0"}
	gl.set("v0.3.0", shaA, manifestJSON(t, withVersion("0.3.0")))
	gl.set(shaB, shaB, manifestJSON(t, withVersion("0.3.0")))
	ctx := context.Background()
	_, err := svc.Install(ctx, uuid.New(), url, "v0.3.0", "")
	require.NoError(t, err)
	row := installed(t, svc, "hello")

	plan, err := svc.PlanRepin(ctx, "hello", RepinRequest{Ref: "v0.3.0"})
	require.NoError(t, err)
	assert.Equal(t, PinTag, plan.Target.PinKind)
	assert.False(t, plan.Changed)

	_, err = svc.Repin(ctx, uuid.New(), "hello", repinReq(row, shaB))
	require.NoError(t, err)
	after := installed(t, svc, "hello")
	assert.Equal(t, shaB, after.RepoRef)
	assert.Equal(t, shaB, after.CommitSHA)
}

// A second host serving the same project; requests are counted.
func otherHost(t *testing.T, token string) (gl *fakeGitLab, url string, hits *int32) {
	t.Helper()
	gl = &fakeGitLab{token: token, refs: map[string]fakeRef{}}
	hits = new(int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		gl.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return gl, srv.URL + "/grp/hello", hits
}

func TestRepin_URLAndTokenChange(t *testing.T) {
	svc, repo, gl, url := setup(t, "old-token-value")
	gl.tags = []string{"v0.3.0"}
	gl.set("v0.3.0", shaA, manifestJSON(t, withVersion("0.3.0")))
	ctx := context.Background()
	_, err := svc.Install(ctx, uuid.New(), url, "v0.3.0", "old-token-value")
	require.NoError(t, err)
	row := installed(t, svc, "hello")

	gl2, url2, _ := otherHost(t, "new-token-value")
	gl2.set(shaC, shaC, manifestJSON(t, withVersion("0.3.0")))

	// The stored token is not sent to another repository without a choice.
	_, err = svc.PlanRepin(ctx, "hello", RepinRequest{RepoURL: url2, Ref: shaC})
	assert.Equal(t, CodeTokenChoice, errCode(err))

	req := RepinRequest{RepoURL: url2, Ref: shaC, AuthToken: "new-token-value"}
	plan, err := svc.PlanRepin(ctx, "hello", req)
	require.NoError(t, err)
	assert.Equal(t, "replace", plan.Token)
	assert.Equal(t, url2, plan.Target.RepoURL)

	req.ExpectedVersion, req.ExpectedCommitSHA, req.ExpectedTargetCommitSHA = row.Version, row.CommitSHA, plan.Target.CommitSHA
	res, err := svc.Repin(ctx, uuid.New(), "hello", req)
	require.NoError(t, err)
	after := installed(t, svc, "hello")
	assert.Equal(t, url2, after.RepoURL)
	assert.Equal(t, shaC, after.CommitSHA)
	assert.Empty(t, after.PreviousVersion, "another URL: the old ref means nothing there, no rollback to it")
	assert.Equal(t, "new-token-value", repo.tokens["hello"])

	// Secrets never appear in an answer or a journal entry.
	for _, v := range []any{plan, res, repo.details} {
		raw, _ := json.Marshal(v)
		assert.NotContains(t, string(raw), "old-token-value")
		assert.NotContains(t, string(raw), "new-token-value")
	}

	// Remove the token (the new host then refuses: a private repo needs one).
	after = installed(t, svc, "hello")
	gl2.token = ""
	_, err = svc.Repin(ctx, uuid.New(), "hello", RepinRequest{Ref: shaC, RemoveAuthToken: true, ExpectedVersion: after.Version, ExpectedCommitSHA: after.CommitSHA, ExpectedTargetCommitSHA: shaC})
	require.NoError(t, err)
	assert.NotContains(t, repo.tokens, "hello")
}

func TestRepin_Refusals(t *testing.T) {
	svc, repo, gl, url := setup(t, "")
	gl.set(shaA, shaA, manifestJSON(t, withVersion("0.3.0")))
	gl.set(shaB, shaB, manifestJSON(t, withVersion("0.4.0", "projects:write")))
	gl.set(shaC, shaC, manifestJSON(t, func(m map[string]any) { m["id"] = "someone-else" }))
	ctx := context.Background()
	_, err := svc.Install(ctx, uuid.New(), url, shaA, "")
	require.NoError(t, err)
	row := installed(t, svc, "hello")

	_, err = svc.Repin(ctx, uuid.New(), "hello", repinReq(row, shaC))
	assert.Equal(t, CodeIDConflict, errCode(err), "another id")

	stale := repinReq(row, shaB)
	stale.ExpectedCommitSHA = shaC
	_, err = svc.Repin(ctx, uuid.New(), "hello", stale)
	assert.Equal(t, CodeStateChanged, errCode(err), "stale expected state")

	moved := repinReq(row, shaB)
	moved.ExpectedTargetCommitSHA, moved.ConsentCommitSHA = shaC, shaB
	_, err = svc.Repin(ctx, uuid.New(), "hello", moved)
	assert.Equal(t, CodeStateChanged, errCode(err), "the target is not what the plan showed")

	_, err = svc.Repin(ctx, uuid.New(), "hello", repinReq(row, shaB))
	assert.Equal(t, CodeConsentRequired, errCode(err), "added permissions without consent")
	assert.Equal(t, shaA, installed(t, svc, "hello").CommitSHA, "every refusal leaves the row")
	assert.Empty(t, repo.journal, "no entry was written")

	ok := repinReq(row, shaB)
	ok.ConsentCommitSHA = shaB
	_, err = svc.Repin(ctx, uuid.New(), "hello", ok)
	require.NoError(t, err)
	assert.Equal(t, shaB, installed(t, svc, "hello").CommitSHA)

	// Only repository installs can be re-pinned.
	repo.rows["hello"].Source = domain.ExtensionSourceCatalog
	_, err = svc.PlanRepin(ctx, "hello", RepinRequest{Ref: shaA})
	assert.Equal(t, CodeNotRepoInstall, errCode(err))
}

// An explicit commit pin is never moved by the automatic pass and its
// repository is not even asked; a tag pin next to it does move (control).
func TestAutoUpdate_LeavesCommitPinAlone(t *testing.T) {
	svc, repo, gl, url := setup(t, "")
	ctx := context.Background()
	// Control: pinned to the old tag on the first host.
	gl.tags = []string{"v0.3.0", "v0.4.0"}
	gl.set("v0.3.0", shaA, manifestJSON(t, func(m map[string]any) { m["id"], m["version"] = "hello-tag", "0.3.0" }))
	gl.set("v0.4.0", shaB, manifestJSON(t, func(m map[string]any) { m["id"], m["version"] = "hello-tag", "0.4.0" }))
	_, err := svc.Install(ctx, uuid.New(), url, "v0.3.0", "")
	require.NoError(t, err)

	// The commit pin on a counted host that has newer tags and a moving branch.
	gl2, url2, hits := otherHost(t, "")
	gl2.tags, gl2.branches = []string{"v0.3.0", "v0.4.0"}, []string{"main"}
	gl2.set("v0.3.0", shaA, manifestJSON(t, withVersion("0.3.0")))
	gl2.set("v0.4.0", shaB, manifestJSON(t, withVersion("0.4.0")))
	gl2.set("main", shaC, manifestJSON(t, withVersion("0.5.0")))
	_, err = svc.Install(ctx, uuid.New(), url2, shaA, "")
	require.NoError(t, err)

	repo.auto = true
	atomic.StoreInt32(hits, 0)
	svc.AutoUpdateOnce(ctx)
	pinned := installed(t, svc, "hello")
	assert.Equal(t, shaA, pinned.CommitSHA, "the commit pin stays")
	assert.Equal(t, shaA, pinned.RepoRef)
	assert.Equal(t, "0.3.0", pinned.Version)
	assert.Zero(t, atomic.LoadInt32(hits), "the pinned repository was not asked")
	assert.Equal(t, "0.4.0", installed(t, svc, "hello-tag").Version, "control: the tag pin moved to the newer tag")

	// The manual update says up to date too, and the Updates tab offers nothing.
	res, err := svc.Update(ctx, nil, "hello", "")
	require.NoError(t, err)
	assert.Equal(t, OutcomeUpToDate, res.Outcome)
	list, err := svc.Updates(ctx)
	require.NoError(t, err)
	for _, u := range list {
		if u.ExtensionID == "hello" {
			assert.False(t, u.UpdateAvailable)
		}
	}
	assert.Zero(t, atomic.LoadInt32(hits))
}

// Input refusals happen before anything is fetched or written.
func TestRepin_InputRefusals(t *testing.T) {
	svc, repo, gl, url := setup(t, "")
	gl.set(shaA, shaA, manifestJSON(t, withVersion("0.3.0")))
	ctx := context.Background()
	_, err := svc.Install(ctx, uuid.New(), url, shaA, "")
	require.NoError(t, err)
	row := installed(t, svc, "hello")

	_, err = svc.PlanRepin(ctx, "hello", RepinRequest{Ref: "  "})
	assert.Equal(t, http.StatusBadRequest, errStatus(err), "ref is required")
	_, err = svc.PlanRepin(ctx, "hello", RepinRequest{Ref: shaA, AuthToken: "t", RemoveAuthToken: true})
	assert.Equal(t, http.StatusBadRequest, errStatus(err), "keep one token choice")
	_, err = svc.PlanRepin(ctx, "missing", RepinRequest{Ref: shaA})
	assert.Equal(t, http.StatusNotFound, errStatus(err))
	_, err = svc.Repin(ctx, uuid.New(), "hello", RepinRequest{Ref: shaA})
	assert.Equal(t, http.StatusBadRequest, errStatus(err), "the expected state is required")
	noTarget := repinReq(row, shaA)
	noTarget.ExpectedTargetCommitSHA = ""
	_, err = svc.Repin(ctx, uuid.New(), "hello", noTarget)
	assert.Equal(t, http.StatusBadRequest, errStatus(err), "the planned target commit is required")
	_, err = svc.PlanRepin(ctx, "hello", RepinRequest{Ref: shaA, RepoURL: "ftp://nowhere/x"})
	assert.Error(t, err, "an unusable repository URL")

	// A commit the host resolves to another commit is not that commit.
	gl.mu.Lock()
	gl.refs[shaC] = fakeRef{shaA, manifestJSON(t, withVersion("0.3.0"))}
	gl.mu.Unlock()
	_, err = svc.PlanRepin(ctx, "hello", RepinRequest{Ref: shaC})
	assert.Equal(t, CodeManifestInvalid, errCode(err))

	// Removing a token that is not stored is "none"; the same pin is up to date.
	plan, err := svc.PlanRepin(ctx, "hello", RepinRequest{Ref: shaA, RemoveAuthToken: true})
	require.NoError(t, err)
	assert.Equal(t, "none", plan.Token)
	assert.False(t, plan.Changed)
	assert.Equal(t, autoUpdateOf(PinCommit), plan.AutoUpdate)
	res, err := svc.Repin(ctx, uuid.New(), "hello", repinReq(row, shaA))
	require.NoError(t, err)
	assert.Equal(t, string(OutcomeUpToDate), res.Outcome)
	assert.Empty(t, repo.journal)
	assert.Equal(t, "follows the branch head when it adds no permissions", autoUpdateOf(PinBranch))
}
