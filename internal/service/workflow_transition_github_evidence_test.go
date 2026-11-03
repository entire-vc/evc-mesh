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
	"github.com/entire-vc/evc-mesh/internal/repository"
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

// versionBumpRepo models the vcs_task_snapshot trigger: the first read sees
// version 1, every later read sees laterVersion (default 2): each link row the
// gate's sync wrote bumped it once.
// onLaterRead can also change the row, as a real concurrent writer would.
type versionBumpRepo struct {
	repository.TaskRepository
	reads        int
	laterVersion int64 // version later reads report; default 2
	onLaterRead  func(*domain.Task)
	expectedSeen int64
}

func (r *versionBumpRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
	t, err := r.TaskRepository.GetByID(ctx, id)
	if err != nil || t == nil {
		return t, err
	}
	cp := *t
	r.reads++
	cp.Version = 1
	if r.reads > 1 {
		cp.Version = 2
		if r.laterVersion != 0 {
			cp.Version = r.laterVersion
		}
		if r.onLaterRead != nil {
			r.onLaterRead(&cp)
		}
	}
	return &cp, nil
}

func (r *versionBumpRepo) UpdateTransition(ctx context.Context, task *domain.Task, in domain.TaskTransition) error {
	r.expectedSeen = in.ExpectedVersion
	// Conditional like the production writer: the row is at the version the
	// reads last reported, anything else is a lost CAS.
	// The gate's link write has already happened by now, so the row is at 2
	// whether or not the caller re-read it.
	current := int64(2)
	if r.laterVersion != 0 {
		current = r.laterVersion
	}
	if in.ExpectedVersion != current {
		return &CASConflictError{CurrentVersion: current, CurrentStatusID: task.StatusID, CurrentUpdatedAt: task.UpdatedAt, CurrentAssigneeID: task.AssigneeID}
	}
	return r.Update(ctx, task)
}

// The gate's GitHub sync writes vcs_links, which advances tasks.version. The
// transition must expect that bump (else review→done 409s for ever), but a
// real concurrent write to the row must still be expected at the old version.
func TestMoveTask_GateSyncVersionBumpDoesNotSelfConflict(t *testing.T) {
	author := uuid.New()
	head := strings.Repeat("a", 40)
	tr := domain.TransitionRule{AllowedActors: []string{actorPatternAuthorIfMergedShipped}}
	cases := []struct {
		name         string
		concurrent   func(*domain.Task)
		laterVersion int64
		wantVersion  int64
		wantConflict bool
	}{
		{"only the child-table bump: CAS expects the new version", nil, 3, 3, false},
		{"row changed too: CAS keeps the original version and refuses", func(tk *domain.Task) { tk.AssigneeID = &author }, 3, 1, true},
		// The gate's sync wrote the link twice (head sha, check result: +2) and an
		// unrelated comment/artifact/link write landed too (+1): row fields are
		// untouched, only the version says so.
		{"unrelated child write on top of the gate's own: CAS refuses", nil, 4, 1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			set := func(tk *domain.Task) { tk.PreReviewAssigneeID = &author }
			svc, _, taskID, toID := journalSetup(t, domain.RuleConfigEnforcementStrict, tr, set)
			br := &versionBumpRepo{TaskRepository: svc.taskRepo, onLaterRead: c.concurrent, laterVersion: c.laterVersion}
			svc.taskRepo = br
			repo := NewMockVCSLinkRepository()
			svc.vcsLinkRepo = repo
			svc.githubPRChecker = &gateGitHub{
				heads: map[int]githubapi.PullRequestHead{81: {Merged: true, HeadSHA: head}},
				sums:  map[int]githubapi.ChecksSummary{81: {Total: 6, Status: "success", RunID: 9}}}
			meta, _ := json.Marshal(map[string]string{"ship_sha": head})
			require.NoError(t, repo.Create(context.Background(), &domain.VCSLink{
				ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitHub, LinkType: domain.VCSLinkTypePR,
				ExternalID: "81", URL: "https://github.com/entire-vc/evc-mesh-mcp/pull/81",
				Status: domain.VCSLinkStatusMerged, Metadata: meta,
			}))
			ctx := actorctx.WithActor(context.Background(), author, domain.ActorTypeAgent)
			// One attempt: MoveTask retries implicit-CAS moves and would mask the refusal.
			err := svc.moveTaskOnce(ctx, taskID, MoveTaskInput{StatusID: &toID})
			if c.wantConflict {
				var cas *CASConflictError
				require.ErrorAs(t, err, &cas)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, c.wantVersion, br.expectedSeen)
		})
	}
}

// A version bump the gate did not cause (a concurrent comment, artifact or
// link write) must not be adopted: the gate reports it wrote nothing when the
// task has no GitHub delivery link to sync.
func TestApplyTransitionGate_NoGitHubLinkReportsNoSync(t *testing.T) {
	author := uuid.New()
	tr := domain.TransitionRule{AllowedActors: []string{actorPatternAuthorIfMergedShipped}}
	svc, _, taskID, toID := journalSetup(t, domain.RuleConfigEnforcementStrict, tr, func(tk *domain.Task) { tk.PreReviewAssigneeID = &author })
	svc.vcsLinkRepo = NewMockVCSLinkRepository()
	task, err := svc.taskRepo.GetByID(context.Background(), taskID)
	require.NoError(t, err)
	to, err := svc.statusRepo.GetByID(context.Background(), toID)
	require.NoError(t, err)
	ctx := actorctx.WithActor(context.Background(), author, domain.ActorTypeAgent)
	_, _, _, writes := svc.applyTransitionGate(ctx, task, task.StatusID, to)
	assert.Zero(t, writes)
}

// MergeMetadata answers (nil, nil) when its UPDATE matched no row. That fires
// no vcs_task_snapshot trigger, so it must not count as one of the gate's own
// version bumps: a concurrent write could otherwise fill the gap and be adopted.
func TestCountingVCSLinkRepo_CountsOnlyRowsActuallyWritten(t *testing.T) {
	ctx := context.Background()
	taskID := uuid.New()
	inner := NewMockVCSLinkRepository()
	link := &domain.VCSLink{ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitHub, LinkType: domain.VCSLinkTypePR}
	require.NoError(t, inner.Create(ctx, link))
	repo := &countingVCSLinkRepo{VCSLinkRepository: inner}
	patch := json.RawMessage(`{"head_sha":"abc"}`)

	got, err := repo.MergeMetadata(ctx, taskID, uuid.New(), patch) // no such link
	require.NoError(t, err)
	require.Nil(t, got)
	assert.Zero(t, repo.writes)

	got, err = repo.MergeMetadata(ctx, taskID, link.ID, patch)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, int64(1), repo.writes)
}
