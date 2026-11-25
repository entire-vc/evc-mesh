package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	githubapi "github.com/entire-vc/evc-mesh/internal/integration/github"
	gitlabapi "github.com/entire-vc/evc-mesh/internal/integration/gitlab"
)

// The GitLab Merge Request Hook carries no merge time: on a merge the service
// reads GitLab's own merged_at for the link. A GitLab that reports
// no time, or cannot be read, leaves merged_at nil — never the delivery time.
func TestHandleGitLabMergeRequestEvent_StoresGitLabMergedAt(t *testing.T) {
	const mrURL = "https://gitlab.example.invalid/throwaway/repo/-/merge_requests/42"
	cases := []struct {
		name string
		ev   *gitlabapi.MergeRequestEvidence // nil: GitLab answers an error
		want *time.Time
	}{
		{"gitlab reports merged_at", &gitlabapi.MergeRequestEvidence{State: "merged", WebURL: mrURL,
			MergedAt: ptrTime(time.Date(2026, 11, 20, 18, 3, 4, 0, time.UTC))}, ptrTime(time.Date(2026, 11, 20, 18, 3, 4, 0, time.UTC))},
		{"gitlab reports no time", &gitlabapi.MergeRequestEvidence{State: "merged", WebURL: mrURL}, nil},
		{"gitlab unreachable", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newWorkspaceScopeHarness(t)
			src := &fakeMREvidence{byRef: map[string]gitlabapi.MergeRequestEvidence{}}
			if tc.ev != nil {
				src.byRef["throwaway/repo!42"] = *tc.ev
			}
			svc := NewVCSLinkService(h.repo,
				WithVCSTaskRepo(h.taskRepo), WithVCSStatusRepo(h.statusRepo), WithVCSTaskService(h.taskSvc),
				WithVCSCommentService(h.commentSvc), WithVCSProjectRepo(h.projectRepo),
				WithVCSMREvidenceSource(func(_ context.Context, ws uuid.UUID) (gitlabapi.MergeRequestEvidenceSource, bool) {
					return src, ws == h.wsA
				}))
			before := time.Now()
			_, err := svc.HandleGitLabMergeRequestEvent(context.Background(),
				h.mergedMREvent(h.wsA, "fix: gate (#"+h.taskA.ID.String()[:8]+")"))
			require.NoError(t, err)
			links, err := h.repo.ListByTask(context.Background(), h.taskA.ID)
			require.NoError(t, err)
			require.Len(t, links, 1)
			got := links[0].MergedAt
			if tc.want == nil {
				assert.Nil(t, got, "unknown merge time stays nil")
				return
			}
			require.NotNil(t, got)
			assert.True(t, got.Equal(*tc.want), "merged_at %v, want GitLab's %v", got, tc.want)
			assert.True(t, got.Before(before), "not the delivery time")
			assert.Contains(t, src.calls, "throwaway/repo!42")
		})
	}
}

type scriptedBackfiller struct {
	afters []uuid.UUID
	pages  []MergedAtBackfillPage
	errs   []error
}

func (b *scriptedBackfiller) BackfillMergedAt(_ context.Context, afterID uuid.UUID, _ int) (MergedAtBackfillPage, error) {
	i := len(b.afters)
	b.afters = append(b.afters, afterID)
	return b.pages[i], b.errs[i]
}

// A page cut short by the tick deadline still advances the cursor past the
// links it tried, so a slow provider at the head cannot stall the backfill;
// an error never starts the rest, and a full pass does.
func TestMergedAtSweeper_AdvancesPastTimedOutPage(t *testing.T) {
	tried, last := uuid.New(), uuid.New()
	b := &scriptedBackfiller{
		pages: []MergedAtBackfillPage{{Next: tried, Seen: 50}, {Next: last, Seen: 50}, {Done: true, Seen: 3}, {}},
		errs:  []error{context.DeadlineExceeded, nil, nil, nil},
	}
	now := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	w := NewMergedAtSweeper(b, 50, time.Hour)
	w.now = func() time.Time { return now }

	_, err := w.Tick(context.Background())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	_, err = w.Tick(context.Background())
	require.NoError(t, err)
	_, err = w.Tick(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{uuid.Nil, tried, last}, b.afters, "each tick resumes after the last link tried")

	page, err := w.Tick(context.Background())
	require.NoError(t, err)
	assert.Zero(t, page.Seen, "rests after a full pass")
	assert.Len(t, b.afters, 3)
	now = now.Add(time.Hour + time.Second)
	_, err = w.Tick(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uuid.Nil, b.afters[3], "next pass starts over")
}

// cancellingMRSource answers like GitLab and cancels the run on call n, the
// way the sweeper's tick deadline cuts a slow provider read short.
type cancellingMRSource struct {
	n, calls int
	cancel   context.CancelFunc
}

func (s *cancellingMRSource) GetMergeRequestEvidence(ctx context.Context, projectPath string, iid int) (gitlabapi.MergeRequestEvidence, error) {
	s.calls++
	if s.calls == s.n {
		s.cancel()
		return gitlabapi.MergeRequestEvidence{}, ctx.Err()
	}
	at := time.Date(2026, 11, 20, 18, 0, iid, 0, time.UTC)
	return gitlabapi.MergeRequestEvidence{State: "merged", MergedAt: &at,
		WebURL: fmt.Sprintf("https://gitlab.example.invalid/%s/-/merge_requests/%d", projectPath, iid)}, nil
}

// A final, partial page cut short — mid-way or on its very last link — is not
// a finished pass: it reports Done=false with the cursor at the last link that
// got an answer, so the next page asks the interrupted link again first.
func TestBackfillMergedAt_InterruptedFinalPageResumes(t *testing.T) {
	for _, cutAt := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("cut on read %d of 3", cutAt), func(t *testing.T) {
			h := newWorkspaceScopeHarness(t)
			for k := range h.repo.links {
				delete(h.repo.links, k)
			}
			for iid := 1; iid <= 3; iid++ {
				l := &domain.VCSLink{ID: uuid.New(), TaskID: h.taskA.ID, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR,
					ExternalID: fmt.Sprint(iid), URL: fmt.Sprintf("https://gitlab.example.invalid/throwaway/repo/-/merge_requests/%d", iid),
					Status: domain.VCSLinkStatusMerged, Metadata: []byte("{}")}
				h.repo.links[l.ID] = l
			}
			ordered, err := h.repo.ListMergedPRLinksWithoutMergedAt(context.Background(), uuid.Nil, 10)
			require.NoError(t, err)
			require.Len(t, ordered, 3)

			ctx, cancel := context.WithCancel(context.Background())
			src := &cancellingMRSource{n: cutAt, cancel: cancel}
			svc := NewVCSLinkService(h.repo, WithVCSTaskRepo(h.taskRepo), WithVCSProjectRepo(h.projectRepo),
				WithVCSMREvidenceSource(func(_ context.Context, ws uuid.UUID) (gitlabapi.MergeRequestEvidenceSource, bool) {
					return src, ws == h.wsA
				})).(MergedAtBackfiller)

			page, err := svc.BackfillMergedAt(ctx, uuid.Nil, 10)
			require.ErrorIs(t, err, context.Canceled)
			assert.False(t, page.Done, "a page cut short is not a finished pass")
			wantNext := uuid.Nil
			if cutAt > 1 {
				wantNext = ordered[cutAt-2].ID
			}
			assert.Equal(t, wantNext, page.Next, "cursor at the last link that got an answer")
			assert.Equal(t, cutAt-1, page.Filled)

			page, err = svc.BackfillMergedAt(context.Background(), page.Next, 10)
			require.NoError(t, err)
			assert.Equal(t, 3-(cutAt-1), page.Seen, "resumes at the interrupted link")
			assert.Equal(t, page.Seen, page.Filled)
			assert.True(t, page.Done)
			assert.Equal(t, uuid.Nil, page.Next)
			for _, l := range ordered {
				assert.NotNil(t, h.repo.links[l.ID].MergedAt, "every link filled across the two pages")
			}
		})
	}
}

// providerMergedAt answers only what the provider reports for a merged pr
// link it can resolve; every other case is nil with no invented time.
func TestProviderMergedAt_Cases(t *testing.T) {
	at := time.Date(2026, 11, 20, 15, 3, 4, 0, time.UTC)
	const glURL = "https://gitlab.example.invalid/throwaway/repo/-/merge_requests/42"
	const ghURL = "https://github.com/o/r/pull/81"
	h := newWorkspaceScopeHarness(t)
	mk := func(p domain.VCSProvider, url string) domain.VCSLink {
		return domain.VCSLink{ID: uuid.New(), TaskID: h.taskA.ID, Provider: p, LinkType: domain.VCSLinkTypePR, URL: url, Status: domain.VCSLinkStatusMerged}
	}
	gl := &fakeMREvidence{byRef: map[string]gitlabapi.MergeRequestEvidence{}}
	gh := &fakeGitHubEvidence{}
	ghOK := true
	svc := NewVCSLinkService(h.repo, WithVCSTaskRepo(h.taskRepo), WithVCSProjectRepo(h.projectRepo),
		WithVCSMREvidenceSource(func(_ context.Context, ws uuid.UUID) (gitlabapi.MergeRequestEvidenceSource, bool) {
			return gl, ws == h.wsA
		}),
		WithVCSGitHubEvidenceSource(func(_ context.Context, ws uuid.UUID) (githubapi.PullRequestEvidenceSource, bool) {
			return gh, ghOK && ws == h.wsA
		})).(*vcsLinkService)
	ctx := context.Background()

	// GitLab: reported time; namesake web_url; not merged; unreadable; bad URL.
	gl.byRef["throwaway/repo!42"] = gitlabapi.MergeRequestEvidence{State: "merged", WebURL: glURL, MergedAt: &at}
	got, err := svc.providerMergedAt(ctx, mk(domain.VCSProviderGitLab, glURL))
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got.Equal(at))
	gl.byRef["throwaway/repo!42"] = gitlabapi.MergeRequestEvidence{State: "merged", WebURL: "https://elsewhere/throwaway/repo/-/merge_requests/42", MergedAt: &at}
	got, err = svc.providerMergedAt(ctx, mk(domain.VCSProviderGitLab, glURL))
	require.NoError(t, err)
	assert.Nil(t, got, "a namesake MR elsewhere is not this link")
	gl.byRef["throwaway/repo!42"] = gitlabapi.MergeRequestEvidence{State: "opened", WebURL: glURL, MergedAt: &at}
	got, err = svc.providerMergedAt(ctx, mk(domain.VCSProviderGitLab, glURL))
	require.NoError(t, err)
	assert.Nil(t, got, "GitLab does not report it merged")
	gl.err = errors.New("502")
	_, err = svc.providerMergedAt(ctx, mk(domain.VCSProviderGitLab, glURL))
	require.Error(t, err)
	gl.err = nil
	got, err = svc.providerMergedAt(ctx, mk(domain.VCSProviderGitLab, "https://gitlab.example.invalid/throwaway/repo"))
	require.NoError(t, err)
	assert.Nil(t, got)

	// GitHub: reported time; unmerged; unreadable; bad URL; no integration;
	// a task outside any workspace.
	gh.head = githubapi.PullRequestHead{Merged: true, MergedAt: &at}
	got, err = svc.providerMergedAt(ctx, mk(domain.VCSProviderGitHub, ghURL))
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got.Equal(at))
	gh.head = githubapi.PullRequestHead{Merged: false, MergedAt: &at}
	got, err = svc.providerMergedAt(ctx, mk(domain.VCSProviderGitHub, ghURL))
	require.NoError(t, err)
	assert.Nil(t, got, "GitHub does not report it merged")
	gh.headErr = errors.New("403")
	_, err = svc.providerMergedAt(ctx, mk(domain.VCSProviderGitHub, ghURL))
	require.Error(t, err)
	gh.headErr = nil
	got, err = svc.providerMergedAt(ctx, mk(domain.VCSProviderGitHub, "https://github.com/o/r"))
	require.NoError(t, err)
	assert.Nil(t, got)
	ghOK = false
	got, err = svc.providerMergedAt(ctx, mk(domain.VCSProviderGitHub, ghURL))
	require.NoError(t, err)
	assert.Nil(t, got, "no GitHub integration in the workspace")
	ghOK = true
	orphan := mk(domain.VCSProviderGitHub, ghURL)
	orphan.TaskID = uuid.New()
	got, err = svc.providerMergedAt(ctx, orphan)
	require.NoError(t, err)
	assert.Nil(t, got)

	// Not a merged pr link, or an unknown provider: nothing is asked.
	open := mk(domain.VCSProviderGitHub, ghURL)
	open.Status = domain.VCSLinkStatusOpen
	got, err = svc.providerMergedAt(ctx, open)
	require.NoError(t, err)
	assert.Nil(t, got)
	got, err = svc.providerMergedAt(ctx, mk(domain.VCSProvider("bitbucket"), ghURL))
	require.NoError(t, err)
	assert.Nil(t, got)

	// fillMergedAt never re-asks a link that already has a time.
	known := mk(domain.VCSProviderGitHub, ghURL)
	known.MergedAt = &at
	w, err := svc.fillMergedAt(ctx, known)
	require.NoError(t, err)
	assert.False(t, w)
}

type setMergedAtErrRepo struct{ *fakeVCSLinkRepo }

func (setMergedAtErrRepo) SetMergedAt(context.Context, uuid.UUID, time.Time) (bool, error) {
	return false, errors.New("db down")
}

type listErrRepo struct{ *fakeVCSLinkRepo }

func (listErrRepo) ListMergedPRLinksWithoutMergedAt(context.Context, uuid.UUID, int) ([]domain.VCSLink, error) {
	return nil, errors.New("db down")
}

// Evidence sync stores the provider's merge time on a merged link set-once,
// before and regardless of the head checks, and surfaces a failed write.
func TestEvidenceSync_StoresProviderMergedAt(t *testing.T) {
	at := time.Date(2026, 11, 20, 15, 3, 4, 0, time.UTC)
	ctx := context.Background()

	t.Run("github", func(t *testing.T) {
		repo := newFakeVCSLinkRepo()
		l := mergedGitHubLinkWithShip(repo, "")
		src := &fakeGitHubEvidence{head: githubapi.PullRequestHead{Merged: true, MergedAt: &at}}
		w, err := syncGitHubLinkEvidence(ctx, repo, src, *l)
		require.NoError(t, err)
		assert.True(t, w, "merged_at alone is a write even without a head")
		require.NotNil(t, repo.links[l.ID].MergedAt)
		assert.True(t, repo.links[l.ID].MergedAt.Equal(at))

		later := at.Add(time.Hour)
		src.head.MergedAt = &later
		w, err = syncGitHubLinkEvidence(ctx, repo, src, *repo.links[l.ID])
		require.NoError(t, err)
		assert.False(t, w)
		assert.True(t, repo.links[l.ID].MergedAt.Equal(at), "a known time is kept")

		bad := newFakeVCSLinkRepo()
		bl := mergedGitHubLinkWithShip(bad, "")
		_, err = syncGitHubLinkEvidence(ctx, setMergedAtErrRepo{bad}, src, *bl)
		require.ErrorContains(t, err, "store github merged_at")
	})

	t.Run("gitlab", func(t *testing.T) {
		const url = "https://gitlab.example.invalid/throwaway/repo/-/merge_requests/42"
		h := newWorkspaceScopeHarness(t)
		l := &domain.VCSLink{ID: uuid.New(), TaskID: h.taskA.ID, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR,
			ExternalID: "42", URL: url, Status: domain.VCSLinkStatusMerged, Metadata: []byte("{}")}
		h.repo.links[l.ID] = l
		src := &fakeMREvidence{byRef: map[string]gitlabapi.MergeRequestEvidence{
			"throwaway/repo!42": {State: "merged", WebURL: url, MergedAt: &at}}}
		svc := NewVCSLinkService(h.repo).(*vcsLinkService)
		w, reason, err := syncGitLabLinkEvidence(ctx, svc, src, *l)
		require.NoError(t, err)
		assert.True(t, w, "merged_at alone is a write even without a head")
		assert.Equal(t, GitLabSyncReasonNoHeadSHA, reason)
		require.NotNil(t, h.repo.links[l.ID].MergedAt)
		assert.True(t, h.repo.links[l.ID].MergedAt.Equal(at))

		bad := NewVCSLinkService(setMergedAtErrRepo{newFakeVCSLinkRepo()}).(*vcsLinkService)
		_, _, err = syncGitLabLinkEvidence(ctx, bad, src, domain.VCSLink{ID: uuid.New(), Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR, ExternalID: "42", URL: url, Status: domain.VCSLinkStatusMerged})
		require.ErrorContains(t, err, "store gitlab merged_at")
	})

	t.Run("backfill list error keeps the cursor", func(t *testing.T) {
		svc := NewVCSLinkService(listErrRepo{newFakeVCSLinkRepo()}).(MergedAtBackfiller)
		after := uuid.New()
		page, err := svc.BackfillMergedAt(ctx, after, 10)
		require.Error(t, err)
		assert.Equal(t, after, page.Next)
	})

	t.Run("backfill logs a provider error and passes the link", func(t *testing.T) {
		h := newWorkspaceScopeHarness(t)
		for k := range h.repo.links {
			delete(h.repo.links, k)
		}
		l := &domain.VCSLink{ID: uuid.New(), TaskID: h.taskA.ID, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR,
			URL: "https://gitlab.example.invalid/throwaway/repo/-/merge_requests/42", Status: domain.VCSLinkStatusMerged, Metadata: []byte("{}")}
		h.repo.links[l.ID] = l
		src := &fakeMREvidence{err: errors.New("502")}
		svc := NewVCSLinkService(h.repo, WithVCSTaskRepo(h.taskRepo), WithVCSProjectRepo(h.projectRepo),
			WithVCSMREvidenceSource(func(context.Context, uuid.UUID) (gitlabapi.MergeRequestEvidenceSource, bool) { return src, true })).(MergedAtBackfiller)
		page, err := svc.BackfillMergedAt(ctx, uuid.Nil, 10)
		require.NoError(t, err)
		assert.Equal(t, 1, page.Seen)
		assert.Zero(t, page.Filled)
		assert.True(t, page.Done)
		assert.Nil(t, h.repo.links[l.ID].MergedAt, "an unreadable provider leaves it unknown")
	})
}
