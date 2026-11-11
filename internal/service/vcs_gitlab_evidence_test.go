package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	gitlabapi "github.com/entire-vc/evc-mesh/internal/integration/gitlab"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// The guards that refuse a link before GitLab is asked; no store involved.
func TestSyncGitLabLinkEvidence_RefusesBeforeAskingGitLab(t *testing.T) {
	base := domain.VCSLink{ID: uuid.New(), TaskID: uuid.New(), Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR, ExternalID: "42", URL: syncMRURL, Metadata: []byte("{}")}
	cases := map[string]struct {
		mut    func(l *domain.VCSLink)
		reason string
	}{
		"github link":       {func(l *domain.VCSLink) { l.Provider = domain.VCSProviderGitHub }, GitLabSyncReasonNotGitLabMR},
		"commit link":       {func(l *domain.VCSLink) { l.LinkType = domain.VCSLinkTypeCommit }, GitLabSyncReasonNotGitLabMR},
		"review subject":    {func(l *domain.VCSLink) { l.Metadata = []byte(`{"role":"review"}`) }, GitLabSyncReasonNotGitLabMR},
		"not an MR url":     {func(l *domain.VCSLink) { l.URL = "https://git.example.org/example/product/-/commit/abc" }, GitLabSyncReasonUnparseableURL},
		"diffs suffix":      {func(l *domain.VCSLink) { l.URL = syncMRURL + "/diffs" }, GitLabSyncReasonUnparseableURL},
		"other iid in row":  {func(l *domain.VCSLink) { l.ExternalID = "17" }, GitLabSyncReasonExternalID},
		"bang prefixed iid": {func(l *domain.VCSLink) { l.ExternalID = "!42" }, GitLabSyncReasonExternalID},
		"padded iid":        {func(l *domain.VCSLink) { l.ExternalID = " 42 " }, GitLabSyncReasonExternalID},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			l := base
			tc.mut(&l)
			src := &fakeMREvidence{byRef: map[string]gitlabapi.MergeRequestEvidence{}}
			s := &vcsLinkService{repo: newFakeVCSLinkRepo()}
			written, reason, err := syncGitLabLinkEvidence(context.Background(), s, src, l)
			require.NoError(t, err)
			assert.False(t, written)
			assert.Equal(t, tc.reason, reason)
			assert.Empty(t, src.calls)
		})
	}
}

func TestSameMRWebURL(t *testing.T) {
	assert.True(t, sameMRWebURL(syncMRURL, "http://GIT.EXAMPLE.ORG/example/product/-/merge_requests/42/?x=1#n"))
	assert.False(t, sameMRWebURL(syncMRURL, "https://gitlab.example.com/example/product/-/merge_requests/42"))
	assert.False(t, sameMRWebURL(syncMRURL, "https://git.example.org/example/other-product/-/merge_requests/42"))
	assert.False(t, sameMRWebURL("", syncMRURL))
	assert.False(t, sameMRWebURL("::bad", syncMRURL))
}

// In-memory store: the pipeline outcomes the DB test does not reach.
func TestSyncGitLabLinkEvidence_PipelineOutcomes(t *testing.T) {
	cases := map[string]struct {
		hp      gitlabapi.HeadPipeline
		stored  *int64
		reason  string
		written bool
	}{
		"unknown status":         {hp: gitlabapi.HeadPipeline{ID: 5, Status: "weird", SHA: syncHeadSHA}, reason: GitLabSyncReasonUnknownStatus, written: true},
		"older than stored":      {hp: gitlabapi.HeadPipeline{ID: 5, Status: "success", SHA: syncHeadSHA}, stored: ptrInt64(9), reason: GitLabSyncReasonOlderPipeline, written: true},
		"fresh pipeline written": {hp: gitlabapi.HeadPipeline{ID: 5, Status: "success", SHA: syncHeadSHA}, written: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			repo := newFakeVCSLinkRepo()
			l := &domain.VCSLink{ID: uuid.New(), TaskID: uuid.New(), Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR, ExternalID: "42", URL: syncMRURL, Status: domain.VCSLinkStatusMerged, Metadata: []byte("{}"), CreatedAt: time.Now(), PipelineID: tc.stored}
			require.NoError(t, repo.Create(context.Background(), l))
			hp := tc.hp
			src := &fakeMREvidence{byRef: map[string]gitlabapi.MergeRequestEvidence{"example/product!42": mergedEvidence(syncMRURL, syncHeadSHA, &hp)}}
			s := &vcsLinkService{repo: repo}
			written, reason, err := syncGitLabLinkEvidence(context.Background(), s, src, *l)
			require.NoError(t, err)
			assert.Equal(t, tc.written, written)
			assert.Equal(t, tc.reason, reason)
		})
	}
}

func ptrInt64(v int64) *int64 { return &v }

func TestSyncLink_WithoutStoreDependencies(t *testing.T) {
	repo := newFakeVCSLinkRepo()
	gh := &domain.VCSLink{ID: uuid.New(), TaskID: uuid.New(), Provider: domain.VCSProviderGitHub, LinkType: domain.VCSLinkTypePR, ExternalID: "1", URL: "https://github.com/o/r/pull/1", Metadata: []byte("{}"), CreatedAt: time.Now()}
	gl := &domain.VCSLink{ID: uuid.New(), TaskID: uuid.New(), Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR, ExternalID: "42", URL: syncMRURL, Metadata: []byte("{}"), CreatedAt: time.Now()}
	require.NoError(t, repo.Create(context.Background(), gh))
	require.NoError(t, repo.Create(context.Background(), gl))
	svc := NewVCSLinkService(repo)

	res, err := svc.SyncLink(context.Background(), gh.TaskID, gh.ID)
	require.NoError(t, err)
	assert.False(t, res.Changed)
	assert.Equal(t, GitLabSyncReasonNotGitLabMR, res.Reason)

	_, err = svc.SyncLink(context.Background(), gl.TaskID, gl.ID)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 503, apiErr.StatusCode(), "no GitLab source wired")

	// Create with no source wired stores the merged link untouched.
	link, created, err := svc.Create(context.Background(), domain.CreateVCSLinkInput{TaskID: uuid.New(), Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR, ExternalID: "42", URL: syncMRURL, Status: domain.VCSLinkStatusMerged})
	require.NoError(t, err)
	assert.True(t, created)
	assert.Empty(t, link.HeadSHA())
}
