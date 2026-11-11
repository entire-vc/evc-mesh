package service

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	gitlabapi "github.com/entire-vc/evc-mesh/internal/integration/gitlab"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// A GitLab MR link added after the merge never sees the merge or pipeline
// webhook, so its head_sha and pipeline stayed empty and the delivery-proof
// gate refused the card for ever. SyncLink (and Create for a
// merged link) now read them from GitLab for exactly that link. Real
// Postgres; only the GitLab API (a true external boundary) is faked.

const (
	syncHeadSHA  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	syncOtherSHA = "1111111111111111111111111111111111111111"
	syncMRURL    = "https://git.example.org/example/product/-/merge_requests/42"
)

// fakeMREvidence is the GitLab API: answers per "project!iid".
type fakeMREvidence struct {
	mu    sync.Mutex
	byRef map[string]gitlabapi.MergeRequestEvidence
	err   error
	calls []string
}

func (f *fakeMREvidence) GetMergeRequestEvidence(_ context.Context, projectPath string, iid int) (gitlabapi.MergeRequestEvidence, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ref := projectPath + "!" + strconv.Itoa(iid)
	f.calls = append(f.calls, ref)
	if f.err != nil {
		return gitlabapi.MergeRequestEvidence{}, f.err
	}
	ev, ok := f.byRef[ref]
	if !ok {
		return gitlabapi.MergeRequestEvidence{}, errors.New("404")
	}
	return ev, nil
}

func mergedEvidence(webURL, sha string, hp *gitlabapi.HeadPipeline) gitlabapi.MergeRequestEvidence {
	return gitlabapi.MergeRequestEvidence{State: "merged", SHA: sha, WebURL: webURL, HeadPipeline: hp}
}

type gitlabSyncFixture struct {
	f        *recurringActorTypeFixture
	repo     *postgres.VCSLinkRepo
	svc      VCSLinkService
	src      *fakeMREvidence
	ctx      context.Context
	resolved []uuid.UUID
}

func newGitLabSyncFixture(t *testing.T) *gitlabSyncFixture {
	t.Helper()
	f := newRecurringActorTypeFixture(t)
	g := &gitlabSyncFixture{
		f:    f,
		repo: postgres.NewVCSLinkRepo(f.db),
		src:  &fakeMREvidence{byRef: map[string]gitlabapi.MergeRequestEvidence{}},
		ctx:  actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent),
	}
	g.svc = NewVCSLinkService(g.repo,
		WithVCSTaskRepo(postgres.NewTaskRepo(f.db)),
		WithVCSProjectRepo(postgres.NewProjectRepo(f.db)),
		WithVCSMREvidenceSource(func(_ context.Context, ws uuid.UUID) (gitlabapi.MergeRequestEvidenceSource, bool) {
			g.resolved = append(g.resolved, ws)
			if ws != f.workspaceID {
				return nil, false // only the fixture's workspace has a GitLab integration
			}
			return g.src, true
		}),
	)
	return g
}

func (g *gitlabSyncFixture) task(t *testing.T, f *recurringActorTypeFixture, title string) uuid.UUID {
	t.Helper()
	task := &domain.Task{ID: uuid.New(), ProjectID: f.projectID, StatusID: f.statusID, Title: title, AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium, CreatedByType: domain.ActorTypeSystem}
	require.NoError(t, f.taskSvc.Create(g.ctx, task))
	t.Cleanup(func() { _, _ = f.db.Exec(`DELETE FROM vcs_links WHERE task_id=$1`, task.ID) })
	return task.ID
}

// lateLink stores a merged link the way add_vcs_link status=merged does after
// the merge: delivery evidence present, HEAD and pipeline absent.
func (g *gitlabSyncFixture) lateLink(t *testing.T, taskID uuid.UUID, url, iid string) domain.VCSLink {
	t.Helper()
	meta := `{"status_declared_manually":{"status":"merged","actor_type":"agent"},` +
		`"implementation_sha":"` + syncHeadSHA + `",` +
		`"ci_verdict":{"sha":"` + syncHeadSHA + `","status":"success"},` +
		`"acceptance":{"result":"passed","by":"Reviewer"},` +
		`"deployed":{"sha":"` + syncHeadSHA + `","environment":"prod","at":"2026-11-03T10:00:00Z"}}`
	l := domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR,
		ExternalID: iid, URL: url, Status: domain.VCSLinkStatusMerged, Metadata: []byte(meta), CreatedAt: time.Now(),
	}
	require.NoError(t, g.repo.Create(g.ctx, &l))
	return l
}

func (g *gitlabSyncFixture) get(t *testing.T, id uuid.UUID) domain.VCSLink {
	t.Helper()
	l, err := g.repo.GetByID(g.ctx, id)
	require.NoError(t, err)
	require.NotNil(t, l)
	return *l
}

func metaMap(t *testing.T, l domain.VCSLink) map[string]json.RawMessage {
	t.Helper()
	m := map[string]json.RawMessage{}
	require.NoError(t, json.Unmarshal(l.Metadata, &m))
	return m
}

// assertEvidenceKept checks the delivery evidence keys survive the sync.
func assertEvidenceKept(t *testing.T, before, after domain.VCSLink) {
	t.Helper()
	b, a := metaMap(t, before), metaMap(t, after)
	for _, k := range []string{"status_declared_manually", domain.VCSLinkMetaImplementationSHA, domain.VCSLinkMetaCIVerdict, domain.VCSLinkMetaAcceptance, domain.VCSLinkMetaDeployed} {
		require.Contains(t, a, k)
		assert.JSONEq(t, string(b[k]), string(a[k]), "%s must survive the sync", k)
	}
	_, hasShip := a[domain.VCSLinkMetaShipSHA]
	assert.False(t, hasShip, "sync never sets ship_sha")
	assert.Equal(t, before.Status, after.Status, "sync never changes the link status")
}

func TestSyncLink_LateLinkWithoutWebhook_FillsHeadAndPipelineOnlyOnTarget(t *testing.T) {
	g := newGitLabSyncFixture(t)
	g.src.byRef["example/product!42"] = mergedEvidence(syncMRURL, syncHeadSHA, &gitlabapi.HeadPipeline{ID: 123, Status: "success", SHA: syncHeadSHA})

	taskID := g.task(t, g.f, "late link")
	target := g.lateLink(t, taskID, syncMRURL, "42")
	// Same iid elsewhere: another repo on the same task, another card of the
	// same workspace, the same URL in another workspace.
	otherRepo := g.lateLink(t, taskID, "https://git.example.org/example/other-product/-/merge_requests/42", "42")
	sibling := g.lateLink(t, g.task(t, g.f, "sibling"), syncMRURL, "42")
	foreign := newRecurringActorTypeFixture(t)
	foreignLink := g.lateLink(t, g.task(t, foreign, "foreign"), syncMRURL, "42")

	var statusBefore uuid.UUID
	require.NoError(t, g.f.db.Get(&statusBefore, `SELECT status_id FROM tasks WHERE id=$1`, taskID))

	res, err := g.svc.SyncLink(g.ctx, taskID, target.ID)
	require.NoError(t, err)
	assert.True(t, res.Changed)
	assert.Empty(t, res.Reason)
	after := g.get(t, target.ID)
	assert.Equal(t, syncHeadSHA, after.HeadSHA())
	assert.Equal(t, syncHeadSHA, after.PipelineSHA())
	require.NotNil(t, after.PipelineStatus)
	assert.Equal(t, "success", *after.PipelineStatus)
	assertEvidenceKept(t, target, after)
	assert.Equal(t, []string{"example/product!42"}, g.src.calls, "exactly one GitLab read, for the link's own project and iid")

	for _, other := range []domain.VCSLink{otherRepo, sibling, foreignLink} {
		got := g.get(t, other.ID)
		assert.JSONEq(t, string(other.Metadata), string(got.Metadata), "link %s with the same iid must stay untouched", other.URL)
		assert.Nil(t, got.PipelineStatus)
	}

	var statusAfter uuid.UUID
	require.NoError(t, g.f.db.Get(&statusAfter, `SELECT status_id FROM tasks WHERE id=$1`, taskID))
	assert.Equal(t, statusBefore, statusAfter, "sync never moves the card")
	links, err := g.repo.ListByTask(g.ctx, taskID)
	require.NoError(t, err)
	assert.Len(t, links, 2, "sync never creates links")

	// Repeat: nothing new from GitLab, nothing written.
	var rawBefore []byte
	var updBefore *time.Time
	require.NoError(t, g.f.db.QueryRow(`SELECT metadata::text, pipeline_updated_at FROM vcs_links WHERE id=$1`, target.ID).Scan(&rawBefore, &updBefore))
	res, err = g.svc.SyncLink(g.ctx, taskID, target.ID)
	require.NoError(t, err)
	assert.False(t, res.Changed)
	assert.Equal(t, GitLabSyncReasonUnchanged, res.Reason)
	var rawAfter []byte
	var updAfter *time.Time
	require.NoError(t, g.f.db.QueryRow(`SELECT metadata::text, pipeline_updated_at FROM vcs_links WHERE id=$1`, target.ID).Scan(&rawAfter, &updAfter))
	assert.Equal(t, string(rawBefore), string(rawAfter), "repeat must leave metadata byte for byte")
	require.NotNil(t, updBefore)
	require.NotNil(t, updAfter)
	assert.True(t, updBefore.Equal(*updAfter), "repeat must not rewrite the pipeline row")
}

func TestSyncLink_ScopedToTheLinksTask(t *testing.T) {
	g := newGitLabSyncFixture(t)
	g.src.byRef["example/product!42"] = mergedEvidence(syncMRURL, syncHeadSHA, &gitlabapi.HeadPipeline{ID: 123, Status: "success", SHA: syncHeadSHA})
	taskID := g.task(t, g.f, "owner")
	l := g.lateLink(t, taskID, syncMRURL, "42")

	// A task of another workspace naming this link id.
	foreign := newRecurringActorTypeFixture(t)
	foreignTask := g.task(t, foreign, "foreign")
	_, err := g.svc.SyncLink(g.ctx, foreignTask, l.ID)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())

	// Another card of the same workspace, and an unknown link id.
	_, err = g.svc.SyncLink(g.ctx, g.task(t, g.f, "neighbour"), l.ID)
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
	_, err = g.svc.SyncLink(g.ctx, taskID, uuid.New())
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())

	assert.Empty(t, g.src.calls, "a refused call must not reach GitLab")
	assert.JSONEq(t, string(l.Metadata), string(g.get(t, l.ID).Metadata))
}

func TestSyncLink_PipelineOfAnotherSHAIsNotBound(t *testing.T) {
	g := newGitLabSyncFixture(t)
	g.src.byRef["example/product!42"] = mergedEvidence(syncMRURL, syncHeadSHA, &gitlabapi.HeadPipeline{ID: 122, Status: "success", SHA: syncOtherSHA})
	taskID := g.task(t, g.f, "mismatch")
	l := g.lateLink(t, taskID, syncMRURL, "42")

	res, err := g.svc.SyncLink(g.ctx, taskID, l.ID)
	require.NoError(t, err)
	assert.True(t, res.Changed, "the head commit is still GitLab's word")
	assert.Equal(t, GitLabSyncReasonPipelineMismatch, res.Reason)
	after := g.get(t, l.ID)
	assert.Equal(t, syncHeadSHA, after.HeadSHA())
	assert.Nil(t, after.PipelineStatus)
	assert.Empty(t, after.PipelineSHA())
	assertEvidenceKept(t, l, after)
}

func TestSyncLink_NoConfirmedDataWritesNothing(t *testing.T) {
	cases := []struct {
		name   string
		ev     *gitlabapi.MergeRequestEvidence
		err    error
		reason string
		head   string
	}{
		{name: "not merged", ev: &gitlabapi.MergeRequestEvidence{State: "opened", SHA: syncHeadSHA, WebURL: syncMRURL, HeadPipeline: &gitlabapi.HeadPipeline{ID: 1, Status: "success", SHA: syncHeadSHA}}, reason: GitLabSyncReasonNotMerged},
		{name: "merged without sha", ev: &gitlabapi.MergeRequestEvidence{State: "merged", WebURL: syncMRURL}, reason: GitLabSyncReasonNoHeadSHA},
		{name: "another host", ev: func() *gitlabapi.MergeRequestEvidence {
			e := mergedEvidence("https://gitlab.example.com/example/product/-/merge_requests/42", syncHeadSHA, &gitlabapi.HeadPipeline{ID: 1, Status: "success", SHA: syncHeadSHA})
			return &e
		}(), reason: GitLabSyncReasonURLMismatch},
		{name: "no pipeline", ev: func() *gitlabapi.MergeRequestEvidence { e := mergedEvidence(syncMRURL, syncHeadSHA, nil); return &e }(), reason: GitLabSyncReasonNoPipeline, head: syncHeadSHA},
		{name: "gitlab error", err: errors.New("502 bad gateway")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newGitLabSyncFixture(t)
			if tc.ev != nil {
				g.src.byRef["example/product!42"] = *tc.ev
			}
			g.src.err = tc.err
			taskID := g.task(t, g.f, tc.name)
			l := g.lateLink(t, taskID, syncMRURL, "42")

			res, err := g.svc.SyncLink(g.ctx, taskID, l.ID)
			if tc.err != nil {
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, 503, apiErr.StatusCode())
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.reason, res.Reason)
				assert.Equal(t, tc.head != "", res.Changed)
			}
			after := g.get(t, l.ID)
			assert.Equal(t, tc.head, after.HeadSHA())
			assert.Nil(t, after.PipelineStatus, "no confirmed pipeline, none stored")
			assert.Empty(t, after.PipelineSHA())
			assertEvidenceKept(t, l, after)
		})
	}
}

func TestSyncLink_WorkspaceWithoutGitLabIs503(t *testing.T) {
	g := newGitLabSyncFixture(t)
	foreign := newRecurringActorTypeFixture(t)
	taskID := g.task(t, foreign, "no integration")
	l := g.lateLink(t, taskID, syncMRURL, "42")

	_, err := g.svc.SyncLink(g.ctx, taskID, l.ID)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 503, apiErr.StatusCode())
	assert.Equal(t, []uuid.UUID{foreign.workspaceID}, g.resolved, "the client is the link's own workspace's")
	assert.Empty(t, g.src.calls)
}

func TestCreate_MergedGitLabLinkGetsHeadAndPipeline(t *testing.T) {
	g := newGitLabSyncFixture(t)
	g.src.byRef["example/product!42"] = mergedEvidence(syncMRURL, syncHeadSHA, &gitlabapi.HeadPipeline{ID: 123, Status: "success", SHA: syncHeadSHA})
	taskID := g.task(t, g.f, "create merged")

	link, created, err := g.svc.Create(g.ctx, domain.CreateVCSLinkInput{
		TaskID: taskID, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR,
		ExternalID: "42", URL: syncMRURL, Status: domain.VCSLinkStatusMerged,
	})
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, syncHeadSHA, link.HeadSHA(), "the returned link carries the synced head")
	stored := g.get(t, link.ID)
	assert.Equal(t, syncHeadSHA, stored.HeadSHA())
	assert.Equal(t, syncHeadSHA, stored.PipelineSHA())
	require.NotNil(t, stored.PipelineStatus)
	assert.Equal(t, "success", *stored.PipelineStatus)
	assert.Equal(t, domain.VCSLinkStatusMerged, stored.Status)
	assert.Contains(t, metaMap(t, stored), "status_declared_manually", "the manual declaration trail stays")
}

func TestCreate_GitLabFailureDoesNotFailCreation(t *testing.T) {
	g := newGitLabSyncFixture(t)
	g.src.err = errors.New("timeout")
	taskID := g.task(t, g.f, "create gitlab down")

	link, created, err := g.svc.Create(g.ctx, domain.CreateVCSLinkInput{
		TaskID: taskID, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR,
		ExternalID: "42", URL: syncMRURL, Status: domain.VCSLinkStatusMerged,
	})
	require.NoError(t, err)
	assert.True(t, created)
	stored := g.get(t, link.ID)
	assert.Equal(t, domain.VCSLinkStatusMerged, stored.Status)
	assert.Empty(t, stored.HeadSHA())
	assert.Nil(t, stored.PipelineStatus)
}
