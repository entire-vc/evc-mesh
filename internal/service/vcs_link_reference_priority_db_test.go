package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	gitlabapi "github.com/entire-vc/evc-mesh/internal/integration/gitlab"
)

// Use real ordered rows and re-read the explicit PR by ID: the earlier
// reference already has the right HEAD and must not hide a stale PR.
func TestHandleMR_ExplicitPRWinsReferenceRegardlessOfOrder(t *testing.T) {
	for _, order := range []string{"reference-first", "pr-first", "normalized-url", "reference-only", "other-repo", "other-host", "other-provider", "mismatched-project", "foreign-workspace"} {
		t.Run(order, func(t *testing.T) {
			g := newGitLabSyncFixture(t)
			taskID := g.task(t, g.f, order)
			statusBefore := g.f.statusID
			base := time.Now().Add(-time.Hour)
			ref := domain.VCSLink{ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypeReference,
				ExternalID: "4242", URL: syncMRURL, Status: domain.VCSLinkStatusOpen,
				Metadata: []byte(`{"head_sha":"` + syncHeadSHA + `","reference_only":true}`), CreatedAt: base}
			pr := ref
			pr.ID, pr.LinkType, pr.CreatedAt = uuid.New(), domain.VCSLinkTypePR, base.Add(time.Second)
			pr.Metadata = []byte(`{"ship_sha":"` + syncOtherSHA + `","pipeline_sha":"` + syncOtherSHA + `","ci_verdict":{"sha":"` + syncOtherSHA + `","status":"success"},"acceptance":{"result":"passed"}}`)
			if order == "pr-first" {
				pr.CreatedAt, ref.CreatedAt = ref.CreatedAt, pr.CreatedAt
			}
			if order == "other-repo" {
				pr.URL = "https://git.example.org/other/repo/-/merge_requests/4242"
			}
			if order == "other-host" {
				pr.URL = "https://other.example/example/product/-/merge_requests/4242"
			}
			if order == "normalized-url" {
				pr.URL = "http://GIT.EXAMPLE.ORG/example/product/-/merge_requests/4242/#note"
			}
			if order == "other-provider" {
				pr.Provider = domain.VCSProviderGitHub
			}
			require.NoError(t, g.repo.Create(g.ctx, &ref))
			if order != "reference-only" {
				require.NoError(t, g.repo.Create(g.ctx, &pr))
			}
			ws := g.f.workspaceID
			if order == "foreign-workspace" {
				ws = uuid.New()
			}
			projectPath := "example/product"
			if order == "mismatched-project" {
				projectPath = "other/repo"
			}
			_, err := g.svc.HandleGitLabMergeRequestEvent(g.ctx, GitLabWebhookEvent{WorkspaceID: ws, MRIID: 4242, MRState: "opened", Action: "update",
				ProjectPath: projectPath, MRURL: syncMRURL, MRTitle: "fix MESH-" + taskID.String(), HeadSHA: syncHeadSHA})
			require.NoError(t, err)
			if order == "reference-first" || order == "pr-first" || order == "normalized-url" {
				got := g.get(t, pr.ID)
				assert.Equal(t, syncHeadSHA, got.HeadSHA())
				require.NotNil(t, got.StatusSyncedAt)
				beforeMeta, afterMeta := metaMap(t, pr), metaMap(t, got)
				for key, value := range beforeMeta {
					assert.JSONEq(t, string(value), string(afterMeta[key]), key)
				}
			} else if order != "reference-only" {
				got := g.get(t, pr.ID)
				assert.JSONEq(t, string(pr.Metadata), string(got.Metadata))
				assert.Nil(t, got.StatusSyncedAt)
			}
			gotRef := g.get(t, ref.ID)
			assert.JSONEq(t, string(ref.Metadata), string(gotRef.Metadata), "reference stays a reference")
			var statusAfter uuid.UUID
			require.NoError(t, g.f.db.Get(&statusAfter, `SELECT status_id FROM tasks WHERE id=$1`, taskID))
			assert.Equal(t, statusBefore, statusAfter)
			links, err := g.repo.ListByTask(g.ctx, taskID)
			require.NoError(t, err)
			want := 2
			if order == "reference-only" {
				want = 1
			}
			assert.Len(t, links, want)
		})
	}
}

func TestSyncLink_OpenMRRefreshesExplicitPRWithoutMovingVerdict(t *testing.T) {
	g := newGitLabSyncFixture(t)
	taskID := g.task(t, g.f, "open MR")
	l := g.lateLink(t, taskID, syncMRURL, "4242")
	// The stored SHIP is deliberately for a different HEAD. Sync must never
	// make the previous review/acceptance apply to the current commit.
	patch, err := json.Marshal(map[string]any{domain.VCSLinkMetaShipSHA: syncOtherSHA,
		domain.VCSLinkMetaCIVerdict: map[string]string{"sha": syncOtherSHA, "status": "success"}})
	require.NoError(t, err)
	_, err = g.repo.MergeMetadata(g.ctx, taskID, l.ID, patch)
	require.NoError(t, err)
	_, err = g.repo.SetMergedAt(g.ctx, l.ID, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	before := g.get(t, l.ID)
	g.src.byRef["example/product!4242"] = gitlabapi.MergeRequestEvidence{State: "opened", SHA: syncHeadSHA, WebURL: syncMRURL,
		HeadPipeline: &gitlabapi.HeadPipeline{ID: 26519, Status: "success", SHA: syncHeadSHA}}
	res, err := g.svc.SyncLink(g.ctx, taskID, l.ID)
	require.NoError(t, err)
	assert.True(t, res.Changed)
	assert.Empty(t, res.Reason)
	after := g.get(t, l.ID)
	assert.Equal(t, syncHeadSHA, after.HeadSHA())
	assert.Equal(t, syncHeadSHA, after.PipelineSHA())
	assert.Equal(t, domain.VCSLinkStatusOpen, after.Status)
	require.NotNil(t, after.StatusSyncedAt)
	assert.Nil(t, after.MergedAt)
	for key, value := range metaMap(t, before) {
		assert.JSONEq(t, string(value), string(metaMap(t, after)[key]), key)
	}
	var statusAfter uuid.UUID
	require.NoError(t, g.f.db.Get(&statusAfter, `SELECT status_id FROM tasks WHERE id=$1`, taskID))
	assert.Equal(t, g.f.statusID, statusAfter)
}

func TestSyncLink_MergedMRConfirmsStatusAndProviderTime(t *testing.T) {
	g := newGitLabSyncFixture(t)
	taskID := g.task(t, g.f, "merged MR with stale open link")
	l := g.lateLink(t, taskID, syncMRURL, "4242")
	l.Status = domain.VCSLinkStatusOpen
	_, err := g.repo.Upsert(g.ctx, &l)
	require.NoError(t, err)
	mergedAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	ev := mergedEvidence(syncMRURL, syncHeadSHA, nil)
	ev.MergedAt = &mergedAt
	g.src.byRef["example/product!4242"] = ev
	res, err := g.svc.SyncLink(g.ctx, taskID, l.ID)
	require.NoError(t, err)
	assert.True(t, res.Changed)
	after := g.get(t, l.ID)
	assert.Equal(t, domain.VCSLinkStatusMerged, after.Status)
	require.NotNil(t, after.MergedAt)
	assert.True(t, mergedAt.Equal(*after.MergedAt))
	assert.Equal(t, syncHeadSHA, after.HeadSHA())
	var statusAfter uuid.UUID
	require.NoError(t, g.f.db.Get(&statusAfter, `SELECT status_id FROM tasks WHERE id=$1`, taskID))
	assert.Equal(t, g.f.statusID, statusAfter)
}

func TestSyncLink_ConfirmsOnlyRequestedDuplicateID(t *testing.T) {
	g := newGitLabSyncFixture(t)
	taskID := g.task(t, g.f, "legacy equivalent PR URLs")
	older := g.lateLink(t, taskID, syncMRURL, "4242")
	newer := older
	newer.ID = uuid.New()
	newer.URL = "http://GIT.EXAMPLE.ORG/example/product/-/merge_requests/4242/#note"
	newer.Metadata = []byte(`{"ship_sha":"` + syncOtherSHA + `","ci_verdict":{"sha":"` + syncOtherSHA + `","status":"success"},"acceptance":{"result":"passed","by":"separate-reviewer"}}`)
	// Legacy rows can have equivalent URLs. Bypass Create/Upsert's current
	// deduplication to reproduce the stored rows SyncLink must preserve.
	_, err := g.f.db.Exec(`INSERT INTO vcs_links
		(id, task_id, provider, link_type, external_id, url, title, status, metadata, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, newer.ID, taskID, newer.Provider,
		newer.LinkType, newer.ExternalID, newer.URL, "later explicit PR", newer.Status,
		[]byte(newer.Metadata), older.CreatedAt.Add(time.Second))
	require.NoError(t, err)
	olderBefore, newerBefore := g.get(t, older.ID), g.get(t, newer.ID)
	g.src.byRef["example/product!4242"] = gitlabapi.MergeRequestEvidence{
		State: "opened", SHA: syncHeadSHA, WebURL: syncMRURL,
		HeadPipeline: &gitlabapi.HeadPipeline{ID: 26519, Status: "success", SHA: syncHeadSHA}}
	res, err := g.svc.SyncLink(g.ctx, taskID, newer.ID)
	require.NoError(t, err)
	assert.True(t, res.Changed)
	after := g.get(t, newer.ID)
	assert.Equal(t, olderBefore, g.get(t, older.ID))
	assert.Equal(t, newerBefore.URL, after.URL)
	assert.Equal(t, newerBefore.Title, after.Title)
	assert.Equal(t, syncHeadSHA, after.HeadSHA())
	assert.Equal(t, syncHeadSHA, after.PipelineSHA())
	assert.Equal(t, domain.VCSLinkStatusOpen, after.Status)
	require.NotNil(t, after.StatusSyncedAt)
	for key, value := range metaMap(t, newerBefore) {
		assert.JSONEq(t, string(value), string(metaMap(t, after)[key]), key)
	}
	res, err = g.svc.SyncLink(g.ctx, taskID, newer.ID)
	require.NoError(t, err)
	assert.False(t, res.Changed)
	assert.Equal(t, after.StatusSyncedAt, g.get(t, newer.ID).StatusSyncedAt)
}
