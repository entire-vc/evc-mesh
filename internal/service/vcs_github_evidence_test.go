package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	githubapi "github.com/entire-vc/evc-mesh/internal/integration/github"
)

type fakeGitHubEvidence struct {
	head    githubapi.PullRequestHead
	sum     githubapi.ChecksSummary
	headErr error
	sumErr  error
	gotSHA  string
}

func (f *fakeGitHubEvidence) GetPullRequestHead(context.Context, string, string, int) (githubapi.PullRequestHead, error) {
	return f.head, f.headErr
}

func (f *fakeGitHubEvidence) GetCheckRunsSummary(_ context.Context, _, _, sha string) (githubapi.ChecksSummary, error) {
	f.gotSHA = sha
	return f.sum, f.sumErr
}

// mergedGitHubLinkWithShip is the PR81 shape: merged, SHIP stored, no head and no CI.
func mergedGitHubLinkWithShip(repo *fakeVCSLinkRepo, ship string) *domain.VCSLink {
	l := &domain.VCSLink{
		ID: uuid.New(), TaskID: uuid.New(), Provider: domain.VCSProviderGitHub, LinkType: domain.VCSLinkTypePR,
		ExternalID: "81", URL: "https://github.com/entire-vc/evc-mesh-mcp/pull/81", Status: domain.VCSLinkStatusMerged,
		Metadata: json.RawMessage(`{"ship_sha":"` + ship + `"}`),
	}
	repo.links[l.ID] = l
	return l
}

func TestSyncGitHubLinkEvidence_RestoresHeadAndCI(t *testing.T) {
	head := strings.Repeat("9", 40)
	repo := newFakeVCSLinkRepo()
	l := mergedGitHubLinkWithShip(repo, head)
	require.False(t, l.IsMergedShippedGreen(), "red first: the link has no head and no CI")
	require.Contains(t, l.ShipEvidenceGaps(), domain.ShipGapMissingHeadSHA)

	src := &fakeGitHubEvidence{head: githubapi.PullRequestHead{Merged: true, HeadSHA: head}, sum: githubapi.ChecksSummary{Total: 6, Status: "success", RunID: 77}}
	require.True(t, githubLinkNeedsEvidence(*l))
	changed, _ := syncGitHubLinks(context.Background(), repo, src, []domain.VCSLink{*l}, false)

	assert.True(t, changed)
	assert.Equal(t, head, src.gotSHA, "checks are read for the PR head, not another SHA")
	got := repo.links[l.ID]
	assert.Equal(t, head, got.HeadSHA())
	assert.Equal(t, head, got.PipelineSHA())
	require.NotNil(t, got.PipelineStatus)
	assert.Equal(t, "success", *got.PipelineStatus)
	assert.Equal(t, head, got.ShipSHA(), "ship_sha is never rewritten")
	assert.True(t, got.IsMergedShippedGreen())
}

func TestSyncGitHubLinkEvidence_DoesNotOpenGate(t *testing.T) {
	head := strings.Repeat("9", 40)
	cases := []struct {
		name string
		ship string
		head githubapi.PullRequestHead
		sum  githubapi.ChecksSummary
		gap  string
	}{
		{"failed checks", head, githubapi.PullRequestHead{Merged: true, HeadSHA: head}, githubapi.ChecksSummary{Total: 3, Status: "failed", RunID: 5}, domain.ShipGapPipelineNotGreen},
		{"running checks", head, githubapi.PullRequestHead{Merged: true, HeadSHA: head}, githubapi.ChecksSummary{Total: 3, Status: "running", RunID: 5}, domain.ShipGapPipelineNotGreen},
		{"no checks", head, githubapi.PullRequestHead{Merged: true, HeadSHA: head}, githubapi.ChecksSummary{}, domain.ShipGapPipelineNotGreen},
		{"ship for another sha", strings.Repeat("1", 40), githubapi.PullRequestHead{Merged: true, HeadSHA: head}, githubapi.ChecksSummary{Total: 6, Status: "success", RunID: 5}, domain.ShipGapStaleShipSHA},
		{"pr not merged", head, githubapi.PullRequestHead{Merged: false, HeadSHA: head}, githubapi.ChecksSummary{Total: 6, Status: "success", RunID: 5}, domain.ShipGapMissingHeadSHA},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeVCSLinkRepo()
			l := mergedGitHubLinkWithShip(repo, tc.ship)
			_, _ = syncGitHubLinks(context.Background(), repo, &fakeGitHubEvidence{head: tc.head, sum: tc.sum}, []domain.VCSLink{*l}, false)
			got := repo.links[l.ID]
			assert.False(t, got.IsMergedShippedGreen())
			assert.Contains(t, got.ShipEvidenceGaps(), tc.gap)
		})
	}
}

func TestSyncGitHubLinkEvidence_ProviderErrorLeavesLinkUntouched(t *testing.T) {
	repo := newFakeVCSLinkRepo()
	l := mergedGitHubLinkWithShip(repo, strings.Repeat("9", 40))
	changed, _ := syncGitHubLinks(context.Background(), repo, &fakeGitHubEvidence{headErr: errors.New("boom")}, []domain.VCSLink{*l}, false)
	assert.False(t, changed)
	assert.Empty(t, repo.links[l.ID].HeadSHA())
	assert.Nil(t, repo.links[l.ID].PipelineStatus)
}

func TestGitHubLinkNeedsEvidence_SkipsGitLabAndComplete(t *testing.T) {
	head := strings.Repeat("9", 40)
	green := "success"
	complete := domain.VCSLink{Provider: domain.VCSProviderGitHub, LinkType: domain.VCSLinkTypePR, PipelineStatus: &green,
		Metadata: json.RawMessage(`{"head_sha":"` + head + `","pipeline_sha":"` + head + `"}`)}
	assert.False(t, githubLinkNeedsEvidence(complete))
	gl := complete
	gl.Provider, gl.Metadata, gl.PipelineStatus = domain.VCSProviderGitLab, nil, nil
	assert.False(t, githubLinkNeedsEvidence(gl), "GitLab behaviour is unchanged")
}

func TestSyncGitHubLinkEvidence_ZeroChecksUnbindsCachedGreen(t *testing.T) {
	head := strings.Repeat("9", 40)
	repo := newFakeVCSLinkRepo()
	l := mergedGitHubLinkWithShip(repo, head)
	green, id := "success", int64(5)
	l.PipelineStatus, l.PipelineID = &green, &id
	l.Metadata = json.RawMessage(`{"ship_sha":"` + head + `","head_sha":"` + head + `","pipeline_sha":"` + head + `"}`)
	require.True(t, l.IsMergedShippedGreen(), "red first: stale cache proves delivery")

	_, _ = syncGitHubLinks(context.Background(), repo, &fakeGitHubEvidence{head: githubapi.PullRequestHead{Merged: true, HeadSHA: head}}, []domain.VCSLink{*l}, true)
	assert.False(t, repo.links[l.ID].IsMergedShippedGreen(), "no checks on GitHub must not leave cached green evidence")
}

func TestSyncGitHubLinkEvidence_UnmergedPRUnbindsCachedGreen(t *testing.T) {
	head := strings.Repeat("9", 40)
	repo := newFakeVCSLinkRepo()
	l := mergedGitHubLinkWithShip(repo, head)
	green, id := "success", int64(5)
	l.PipelineStatus, l.PipelineID = &green, &id
	l.Metadata = json.RawMessage(`{"ship_sha":"` + head + `","head_sha":"` + head + `","pipeline_sha":"` + head + `"}`)
	require.True(t, l.IsMergedShippedGreen())

	_, _ = syncGitHubLinks(context.Background(), repo, &fakeGitHubEvidence{head: githubapi.PullRequestHead{Merged: false, HeadSHA: head}}, []domain.VCSLink{*l}, true)
	assert.False(t, repo.links[l.ID].IsMergedShippedGreen())
}

func TestSyncGitHubLinks_ChecksErrorAfterNewHeadStillReportsChange(t *testing.T) {
	oldHead, newHead := strings.Repeat("8", 40), strings.Repeat("9", 40)
	repo := newFakeVCSLinkRepo()
	l := mergedGitHubLinkWithShip(repo, oldHead)
	green, id := "success", int64(5)
	l.PipelineStatus, l.PipelineID = &green, &id
	l.Metadata = json.RawMessage(`{"ship_sha":"` + oldHead + `","head_sha":"` + oldHead + `","pipeline_sha":"` + oldHead + `"}`)
	require.True(t, l.IsMergedShippedGreen())

	src := &fakeGitHubEvidence{head: githubapi.PullRequestHead{Merged: true, HeadSHA: newHead}, sumErr: errors.New("boom")}
	changed, _ := syncGitHubLinks(context.Background(), repo, src, []domain.VCSLink{*l}, true)
	assert.True(t, changed, "the caller must re-read a link whose head was rewritten")
	assert.False(t, repo.links[l.ID].IsMergedShippedGreen(), "old SHIP and old green must not vouch for the new head")
}

func TestSyncGitHubLinks_ChecksReadErrorUnbindsCachedGreen(t *testing.T) {
	head := strings.Repeat("9", 40)
	repo := newFakeVCSLinkRepo()
	l := mergedGitHubLinkWithShip(repo, head)
	green, id := "success", int64(5)
	l.PipelineStatus, l.PipelineID = &green, &id
	l.Metadata = json.RawMessage(`{"ship_sha":"` + head + `","head_sha":"` + head + `","pipeline_sha":"` + head + `"}`)
	require.True(t, l.IsMergedShippedGreen())

	src := &fakeGitHubEvidence{head: githubapi.PullRequestHead{Merged: true, HeadSHA: head}, sumErr: errors.New("boom")}
	chg, _ := syncGitHubLinks(context.Background(), repo, src, []domain.VCSLink{*l}, true)
	assert.True(t, chg)
	assert.False(t, repo.links[l.ID].IsMergedShippedGreen(), "unconfirmed green must not prove delivery")

	// Recovery: the next successful sync binds the result again.
	src.sumErr, src.sum = nil, githubapi.ChecksSummary{Total: 6, Status: "success", RunID: 9}
	_, _ = syncGitHubLinks(context.Background(), repo, src, []domain.VCSLink{*repo.links[l.ID]}, true)
	assert.True(t, repo.links[l.ID].IsMergedShippedGreen())
}

func TestSyncGitHubLinks_UnparseableURLIsUnconfirmedAndUnbound(t *testing.T) {
	head := strings.Repeat("8", 40)
	repo := newFakeVCSLinkRepo()
	l := mergedGitHubLinkWithShip(repo, head)
	green, id := "success", int64(5)
	l.PipelineStatus, l.PipelineID = &green, &id
	l.Metadata = json.RawMessage(`{"ship_sha":"` + head + `","head_sha":"` + head + `","pipeline_sha":"` + head + `"}`)
	l.URL = "https://github.com/entire-vc/not-a-pr-url"
	require.True(t, l.IsMergedShippedGreen(), "red first: cached facts alone prove delivery")

	src := &fakeGitHubEvidence{head: githubapi.PullRequestHead{Merged: true, HeadSHA: head}}
	chg, unconfirmed := syncGitHubLinks(context.Background(), repo, src, []domain.VCSLink{*l}, true)

	assert.True(t, chg)
	assert.True(t, unconfirmed[l.ID], "a link GitHub cannot be asked about is unconfirmed")
	assert.False(t, repo.links[l.ID].IsMergedShippedGreen(), "unconfirmed green must not prove delivery")
}

func TestSyncGitHubLinks_LogsURLQuoted(t *testing.T) {
	head := strings.Repeat("7", 40)
	repo := newFakeVCSLinkRepo()
	l := mergedGitHubLinkWithShip(repo, head)
	l.URL = "https://github.com/o/r/pull/x\n2026/10/31 00:00:00 [forged] entry"

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	syncGitHubLinks(context.Background(), repo, &fakeGitHubEvidence{}, []domain.VCSLink{*l}, true)

	assert.Equal(t, 1, strings.Count(buf.String(), "\n"), "a control character in the URL must not start a second log line")
}

func TestUnbindGitHubCachedGreen_NoCheckerFailsClosed(t *testing.T) {
	head := strings.Repeat("9", 40)
	repo := newFakeVCSLinkRepo()
	l := mergedGitHubLinkWithShip(repo, head)
	green, id := "success", int64(5)
	l.PipelineStatus, l.PipelineID = &green, &id
	l.Metadata = json.RawMessage(`{"ship_sha":"` + head + `","head_sha":"` + head + `","pipeline_sha":"` + head + `"}`)
	require.True(t, l.IsMergedShippedGreen())

	assert.True(t, unbindGitHubCachedGreen(context.Background(), repo, []domain.VCSLink{*l}))
	assert.False(t, repo.links[l.ID].IsMergedShippedGreen())
}

func TestHandlePR_MergedStoresGitHubHeadAndChecks(t *testing.T) {
	h := newHarness(t)
	task := inProgressTask(t, h)
	seedProjectRepos(h.repo, h.taskRepo, h.projectID, "https://github.com/entire-vc/evc-mesh-mcp/pull/1")
	head := strings.Repeat("9", 40)
	src := &fakeGitHubEvidence{head: githubapi.PullRequestHead{Merged: true, HeadSHA: head}, sum: githubapi.ChecksSummary{Total: 6, Status: "success", RunID: 77}}
	h.svc.(*vcsLinkService).githubEvidence = func(context.Context, uuid.UUID) (githubapi.PullRequestEvidenceSource, bool) {
		return src, true
	}
	ev := mergedClosedEvent(81, task.ID, "MESH-"+task.ID.String())
	ev.PRHTMLURL = "https://github.com/entire-vc/evc-mesh-mcp/pull/81"

	_, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.NoError(t, err)

	l := mergedLinkOf(t, h, task.ID)
	assert.Equal(t, head, l.HeadSHA())
	assert.Equal(t, head, l.PipelineSHA())
	require.NotNil(t, l.PipelineStatus)
	assert.Equal(t, "success", *l.PipelineStatus)
}

func TestHandlePR_MergedWithoutGitHubSourceStoresNoEvidence(t *testing.T) {
	h := newHarness(t)
	task := inProgressTask(t, h)
	ev := mergedClosedEvent(81, task.ID, "MESH-"+task.ID.String())
	ev.PRHTMLURL = "https://github.com/entire-vc/evc-mesh-mcp/pull/81"
	_, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.Empty(t, mergedLinkOf(t, h, task.ID).HeadSHA())

	// A source that cannot be resolved behaves the same.
	h.svc.(*vcsLinkService).githubEvidence = func(context.Context, uuid.UUID) (githubapi.PullRequestEvidenceSource, bool) { return nil, false }
	_, err = h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.Empty(t, mergedLinkOf(t, h, task.ID).HeadSHA())
}

func TestWithVCSGitHubEvidenceSource_Wires(t *testing.T) {
	src := &fakeGitHubEvidence{}
	svc := NewVCSLinkService(newFakeVCSLinkRepo(), WithVCSGitHubEvidenceSource(func(context.Context, uuid.UUID) (githubapi.PullRequestEvidenceSource, bool) {
		return src, true
	})).(*vcsLinkService)
	got, ok := svc.githubEvidence(context.Background(), uuid.New())
	assert.True(t, ok)
	assert.Same(t, src, got)
}

type unbindErrRepo struct{ *fakeVCSLinkRepo }

func (unbindErrRepo) MergeMetadata(context.Context, uuid.UUID, uuid.UUID, json.RawMessage) (*domain.VCSLink, error) {
	return nil, errors.New("db down")
}

func TestSyncGitHubEvidence_RepoErrorsAreSurvivable(t *testing.T) {
	head := strings.Repeat("9", 40)
	green, id := "success", int64(5)
	mk := func() (*fakeVCSLinkRepo, *domain.VCSLink) {
		repo := newFakeVCSLinkRepo()
		l := mergedGitHubLinkWithShip(repo, head)
		l.PipelineStatus, l.PipelineID = &green, &id
		l.Metadata = json.RawMessage(`{"ship_sha":"` + head + `","head_sha":"` + head + `","pipeline_sha":"` + head + `"}`)
		return repo, l
	}
	t.Run("unbind failure is reported, not swallowed by the writer", func(t *testing.T) {
		repo, l := mk()
		changed := unbindGitHubCachedGreen(context.Background(), unbindErrRepo{repo}, []domain.VCSLink{*l})
		assert.False(t, changed)
	})
	t.Run("head write failure leaves the link and reports nothing", func(t *testing.T) {
		repo, l := mk()
		newHead := strings.Repeat("8", 40)
		src := &fakeGitHubEvidence{head: githubapi.PullRequestHead{Merged: true, HeadSHA: newHead}}
		chg, unconfirmed := syncGitHubLinks(context.Background(), unbindErrRepo{repo}, src, []domain.VCSLink{*l}, true)
		assert.False(t, chg)
		assert.True(t, unconfirmed[l.ID], "a failed write must still mark the link unconfirmed")
	})
	t.Run("unparseable url is an error, not a silent success", func(t *testing.T) {
		repo, l := mk()
		l.URL = "https://example.com/not-a-pr"
		changed, err := syncGitHubLinkEvidence(context.Background(), repo, &fakeGitHubEvidence{}, *l)
		require.Error(t, err)
		assert.False(t, changed)
	})
}

func TestWithoutCachedGreen_ClearsOnlyUnconfirmedLinks(t *testing.T) {
	a := cachedGreenGitHubLink(uuid.New(), 81, strings.Repeat("a", 40))
	b := cachedGreenGitHubLink(a.TaskID, 82, strings.Repeat("b", 40))
	links := []domain.VCSLink{a, b}
	assert.Equal(t, links, withoutCachedGreen(links, nil))

	out := withoutCachedGreen(links, map[uuid.UUID]bool{a.ID: true})
	assert.Empty(t, out[0].PipelineSHA())
	assert.Equal(t, strings.Repeat("a", 40), out[0].HeadSHA(), "only the binding is dropped")
	assert.Equal(t, b.PipelineSHA(), out[1].PipelineSHA())
	assert.NotEmpty(t, links[0].PipelineSHA(), "input slice is not mutated")
}
