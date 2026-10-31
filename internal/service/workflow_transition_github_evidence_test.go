package service

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	githubapi "github.com/entire-vc/evc-mesh/internal/integration/github"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// gateGitHub answers per PR number as GitHub would right now.
type gateGitHub struct {
	heads map[int]githubapi.PullRequestHead
	sums  map[int]githubapi.ChecksSummary
}

func (g *gateGitHub) GetPullRequestState(_ context.Context, _, _ string, n int) (githubapi.PullRequestState, error) {
	return githubapi.PullRequestState{Merged: g.heads[n].Merged, State: "closed"}, nil
}

func (g *gateGitHub) GetPullRequestHead(_ context.Context, _, _ string, n int) (githubapi.PullRequestHead, error) {
	return g.heads[n], nil
}

func (g *gateGitHub) GetCheckRunsSummary(_ context.Context, _, _, sha string) (githubapi.ChecksSummary, error) {
	for n, h := range g.heads {
		if h.HeadSHA == sha {
			return g.sums[n], nil
		}
	}
	return githubapi.ChecksSummary{}, nil
}

// cachedGreenGitHubLink is a link whose stored facts alone satisfy the
// author_if_merged_shipped evidence: merged, head, SHIP on head, green on head.
func cachedGreenGitHubLink(taskID uuid.UUID, pr int, head string) domain.VCSLink {
	green, id := "success", int64(5)
	meta, _ := json.Marshal(map[string]string{"head_sha": head, "ship_sha": head, "pipeline_sha": head})
	return domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitHub, LinkType: domain.VCSLinkTypePR,
		ExternalID: strconv.Itoa(pr), URL: "https://github.com/entire-vc/evc-mesh-mcp/pull/" + strconv.Itoa(pr),
		Status: domain.VCSLinkStatusMerged, Metadata: meta, PipelineStatus: &green, PipelineID: &id,
	}
}

// The done transition goes through applyTransitionGate: cached facts that
// already satisfy author_if_merged_shipped must still be re-checked against
// GitHub, for every delivery link, so one unconfirmed link is not lost.
func TestMoveTask_AuthorIfMergedShipped_ReverifiesGitHubEvidence(t *testing.T) {
	author := uuid.New()
	h81, h82 := strings.Repeat("a", 40), strings.Repeat("b", 40)
	tr := domain.TransitionRule{AllowedActors: []string{actorPatternAuthorIfMergedShipped}}
	green := githubapi.ChecksSummary{Total: 6, Status: "success", RunID: 9}

	cases := []struct {
		name      string
		gh        *gateGitHub // nil: no GitHub checker can be resolved
		blocked   bool
		wantGapOn string
	}{
		{"positive control: GitHub confirms both heads", &gateGitHub{
			heads: map[int]githubapi.PullRequestHead{81: {Merged: true, HeadSHA: h81}, 82: {Merged: true, HeadSHA: h82}},
			sums:  map[int]githubapi.ChecksSummary{81: green, 82: green}}, false, ""},
		{"second link has no checks on GitHub", &gateGitHub{
			heads: map[int]githubapi.PullRequestHead{81: {Merged: true, HeadSHA: h81}, 82: {Merged: true, HeadSHA: h82}},
			sums:  map[int]githubapi.ChecksSummary{81: green}}, true, domain.ShipGapPipelineSHA},
		{"second link failed on GitHub", &gateGitHub{
			heads: map[int]githubapi.PullRequestHead{81: {Merged: true, HeadSHA: h81}, 82: {Merged: true, HeadSHA: h82}},
			sums:  map[int]githubapi.ChecksSummary{81: green, 82: {Total: 6, Status: "failed", RunID: 9}}}, true, domain.ShipGapPipelineNotGreen},
		{"first link is not merged on GitHub", &gateGitHub{
			heads: map[int]githubapi.PullRequestHead{81: {Merged: false, HeadSHA: h81}, 82: {Merged: true, HeadSHA: h82}},
			sums:  map[int]githubapi.ChecksSummary{81: green, 82: green}}, true, domain.ShipGapPipelineSHA},
		{"head moved on GitHub, SHIP is for the old one", &gateGitHub{
			heads: map[int]githubapi.PullRequestHead{81: {Merged: true, HeadSHA: strings.Repeat("c", 40)}, 82: {Merged: true, HeadSHA: h82}},
			sums:  map[int]githubapi.ChecksSummary{81: green, 82: green}}, true, domain.ShipGapStaleShipSHA},
		{"no GitHub checker can be resolved", nil, true, domain.ShipGapPipelineSHA},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			set := func(tk *domain.Task) { tk.PreReviewAssigneeID = &author }
			svc, _, taskID, toID := journalSetup(t, domain.RuleConfigEnforcementStrict, tr, set)
			repo := NewMockVCSLinkRepository()
			svc.vcsLinkRepo = repo
			if c.gh != nil {
				svc.githubPRChecker = c.gh
			}
			for pr, h := range map[int]string{81: h81, 82: h82} {
				l := cachedGreenGitHubLink(taskID, pr, h)
				require.True(t, l.IsMergedShippedGreen(), "red first: cached facts alone prove delivery")
				require.NoError(t, repo.Create(context.Background(), &l))
			}
			ctx := actorctx.WithActor(context.Background(), author, domain.ActorTypeAgent)
			err := svc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &toID})
			if !c.blocked {
				require.NoError(t, err)
				return
			}
			var ae *apierror.Error
			require.ErrorAs(t, err, &ae)
			assert.Equal(t, "author_if_merged_shipped", ae.Context["gate"])
			assert.Contains(t, ae.Context["reasons"], c.wantGapOn)
		})
	}
}

// failingWrites keeps reads working but refuses every metadata write, as a
// database that went read-only would.
type failingWrites struct{ *MockVCSLinkRepository }

func (failingWrites) MergeMetadata(context.Context, uuid.UUID, uuid.UUID, json.RawMessage) (*domain.VCSLink, error) {
	return nil, errors.New("read-only")
}

// errHeadGitHub fails every head read, as a GitHub outage does.
type errHeadGitHub struct{ gateGitHub }

func (errHeadGitHub) GetPullRequestHead(context.Context, string, string, int) (githubapi.PullRequestHead, error) {
	return githubapi.PullRequestHead{}, errors.New("github down")
}

// A provider read error plus a failed unbind write must not leave the stored
// green result vouching for delivery at the gate.
func TestMoveTask_AuthorIfMergedShipped_ProviderAndWriteFailureStaysClosed(t *testing.T) {
	author := uuid.New()
	head := strings.Repeat("a", 40)
	tr := domain.TransitionRule{AllowedActors: []string{actorPatternAuthorIfMergedShipped}}
	set := func(tk *domain.Task) { tk.PreReviewAssigneeID = &author }
	svc, _, taskID, toID := journalSetup(t, domain.RuleConfigEnforcementStrict, tr, set)
	repo := NewMockVCSLinkRepository()
	svc.vcsLinkRepo = failingWrites{repo}
	svc.githubPRChecker = &errHeadGitHub{}
	l := cachedGreenGitHubLink(taskID, 81, head)
	require.True(t, l.IsMergedShippedGreen(), "red first: cached facts alone prove delivery")
	require.NoError(t, repo.Create(context.Background(), &l))

	ctx := actorctx.WithActor(context.Background(), author, domain.ActorTypeAgent)
	err := svc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &toID})

	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, "author_if_merged_shipped", ae.Context["gate"])
	assert.Contains(t, ae.Context["reasons"], domain.ShipGapPipelineSHA)
}

// With no GitHub checker, only GitHub delivery links lose their cached green
// in the gate's view; a GitLab link's evidence is left as stored.
func TestSyncGitHubEvidenceForGate_NoCheckerKeepsGitLabEvidence(t *testing.T) {
	head := strings.Repeat("b", 40)
	task := &domain.Task{ID: uuid.New()}
	svc, _, _, _ := journalSetup(t, domain.RuleConfigEnforcementStrict, domain.TransitionRule{}, nil)
	repo := NewMockVCSLinkRepository()
	svc.vcsLinkRepo = repo
	svc.githubPRChecker = nil
	gh := cachedGreenGitHubLink(task.ID, 81, head)
	gl := cachedGreenGitHubLink(task.ID, 7, head)
	gl.Provider = domain.VCSProviderGitLab
	gl.URL = "https://gitlab.example.invalid/entire-vc/evc-mesh/-/merge_requests/7"
	require.True(t, gl.IsMergedShippedGreen(), "red first: the GitLab link proves delivery as stored")
	require.NoError(t, repo.Create(context.Background(), &gh))
	require.NoError(t, repo.Create(context.Background(), &gl))

	out := svc.syncGitHubEvidenceForGate(context.Background(), task, []domain.VCSLink{gh, gl})

	require.Len(t, out, 2)
	assert.False(t, out[0].IsMergedShippedGreen(), "unconfirmable GitHub link loses its cached green")
	assert.True(t, out[1].IsMergedShippedGreen(), "GitLab link keeps its evidence")
}

// An old link that never got head_sha / pipeline_status (the webhook created
// it with empty metadata) is healed by the done transition itself: the gate
// reads GitHub, stores the facts, and only then judges them.
func TestMoveTask_AuthorIfMergedShipped_RestoresOldLinkMissingHeadAndCI(t *testing.T) {
	author := uuid.New()
	head := strings.Repeat("a", 40)
	tr := domain.TransitionRule{AllowedActors: []string{actorPatternAuthorIfMergedShipped}}
	gh := func(sum githubapi.ChecksSummary) *gateGitHub {
		return &gateGitHub{
			heads: map[int]githubapi.PullRequestHead{81: {Merged: true, HeadSHA: head}},
			sums:  map[int]githubapi.ChecksSummary{81: sum}}
	}
	cases := []struct {
		name    string
		sum     githubapi.ChecksSummary
		blocked bool
	}{
		{"checks green on GitHub: gate opens", githubapi.ChecksSummary{Total: 6, Status: "success", RunID: 9}, false},
		{"checks failed on GitHub: gate stays closed", githubapi.ChecksSummary{Total: 6, Status: "failed", RunID: 9}, true},
		{"no checks on GitHub: gate stays closed", githubapi.ChecksSummary{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			set := func(tk *domain.Task) { tk.PreReviewAssigneeID = &author }
			svc, _, taskID, toID := journalSetup(t, domain.RuleConfigEnforcementStrict, tr, set)
			repo := NewMockVCSLinkRepository()
			svc.vcsLinkRepo = repo
			svc.githubPRChecker = gh(c.sum)
			meta, _ := json.Marshal(map[string]string{"ship_sha": head})
			old := domain.VCSLink{
				ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitHub, LinkType: domain.VCSLinkTypePR,
				ExternalID: "81", URL: "https://github.com/entire-vc/evc-mesh-mcp/pull/81",
				Status: domain.VCSLinkStatusMerged, Metadata: meta,
			}
			require.False(t, old.IsMergedShippedGreen(), "red first: the old link alone does not prove delivery")
			require.NoError(t, repo.Create(context.Background(), &old))

			ctx := actorctx.WithActor(context.Background(), author, domain.ActorTypeAgent)
			err := svc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &toID})
			links, lerr := repo.ListByTask(context.Background(), taskID)
			require.NoError(t, lerr)
			require.Len(t, links, 1)
			assert.Equal(t, head, links[0].HeadSHA(), "head restored from GitHub")
			if !c.blocked {
				require.NoError(t, err)
				require.NotNil(t, links[0].PipelineStatus)
				assert.Equal(t, "success", *links[0].PipelineStatus)
				assert.Equal(t, head, links[0].PipelineSHA())
				return
			}
			var ae *apierror.Error
			require.ErrorAs(t, err, &ae)
			assert.Equal(t, "author_if_merged_shipped", ae.Context["gate"])
		})
	}
}
