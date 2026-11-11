package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	gitlabapi "github.com/entire-vc/evc-mesh/internal/integration/gitlab"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// A repository without merge requests (a local bare repository) delivers a
// commit, not an MR. The author may close such a card only when someone else
// recorded a SHIP on exactly that commit (ship_sha == commit sha, ship_set by
// a non-author); the MR rule for cards that have MR links is not softened.

const localCommitURLPrefix = "https://code.example.org/workspace/commit/"

func commitLinkWithShip(sha, ship string, shipBy uuid.UUID) domain.VCSLink {
	meta := map[string]any{}
	if ship != "" {
		meta[domain.VCSLinkMetaShipSHA] = ship
	}
	if shipBy != uuid.Nil {
		meta[domain.VCSLinkMetaShipSet] = map[string]string{"by_id": shipBy.String(), "by_type": "agent", "at": "2026-11-10T20:00:00Z"}
	}
	b, _ := json.Marshal(meta)
	return domain.VCSLink{ID: uuid.New(), Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypeCommit,
		ExternalID: sha, URL: localCommitURLPrefix + sha, Metadata: b}
}

func TestAuthorIfMergedShipped_CommitLink(t *testing.T) {
	author, reviewer := uuid.New(), uuid.New()
	task := &domain.Task{PreReviewAssigneeID: &author}
	sha, other := strings.Repeat("a", 40), strings.Repeat("b", 40)
	green := "success"
	cfg := domain.WorkflowRulesConfig{
		EnforcementMode: domain.RuleConfigEnforcementStrict,
		Transitions: map[string]domain.TransitionRule{
			"review": {AllowedActors: []string{"not_author", actorPatternAuthorIfMergedShipped}},
		},
	}
	good := commitLinkWithShip(sha, sha, reviewer)
	goodPR := shippedLink(domain.VCSLinkStatusMerged, "h1", "h1", &green)
	badPR := shippedLink(domain.VCSLinkStatusMerged, "h1", "", &green)
	relates := commitLinkWithShip(sha, sha, reviewer)
	relates.Metadata = json.RawMessage(`{"relation":"relates_to","ship_sha":"` + sha + `","ship_set":{"by_id":"` + reviewer.String() + `"}}`)

	cases := []struct {
		name    string
		links   []domain.VCSLink
		blocked bool
		reason  string
	}{
		{"no pr + commit SHIP by another agent: allowed", []domain.VCSLink{good}, false, ""},
		{"ship_sha written by the author", []domain.VCSLink{commitLinkWithShip(sha, sha, author)}, true, domain.ShipGapByAuthor},
		{"ship_sha names another commit", []domain.VCSLink{commitLinkWithShip(sha, other, reviewer)}, true, domain.ShipGapStaleShipSHA},
		{"no ship_sha", []domain.VCSLink{commitLinkWithShip(sha, "", uuid.Nil)}, true, domain.ShipGapMissingShipSHA},
		{"ship_sha without a writer stamp", []domain.VCSLink{commitLinkWithShip(sha, sha, uuid.Nil)}, true, domain.ShipGapWriterUnknown},
		{"external_id is not a full sha", []domain.VCSLink{commitLinkWithShip(sha[:12], sha, reviewer)}, true, domain.ShipGapNotFullSHA},
		{"no links: negative control", nil, true, ReasonNoDeliveryLink},
		{"relates_to commit is not delivery", []domain.VCSLink{relates}, true, ReasonNoDeliveryLink},
		{"one bad + one good commit: allowed", []domain.VCSLink{commitLinkWithShip(other, "", uuid.Nil), good}, false, ""},
		{"failing pr + valid commit: pr rule not weakened", []domain.VCSLink{badPR, good}, true, domain.ShipGapMissingShipSHA},
		{"passing pr + unproven commit: pr rule unchanged", []domain.VCSLink{goodPR, commitLinkWithShip(other, "", uuid.Nil)}, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, v, diag := evaluateTransitionGate("review", "done", transitionActor{ID: author, Type: domain.ActorTypeAgent}, task, c.links, cfg)
			require.Equal(t, c.blocked, v != "", "violation=%q", v)
			if !c.blocked {
				return
			}
			require.NotNil(t, diag)
			assert.Contains(t, diag.Reasons, c.reason)
		})
	}

	// The diagnosis of a pr card never lists its commit links.
	_, _, diag := evaluateTransitionGate("review", "done", transitionActor{ID: author, Type: domain.ActorTypeAgent}, task, []domain.VCSLink{badPR, good}, cfg)
	require.NotNil(t, diag)
	for _, l := range diag.Links {
		assert.Equal(t, domain.VCSLinkTypePR, l.LinkType)
	}

	// Non-author passes as before, with or without commit evidence.
	_, v, _ := evaluateTransitionGate("review", "done", transitionActor{ID: reviewer, Type: domain.ActorTypeAgent}, task, nil, cfg)
	assert.Empty(t, v)
}

// End to end: the ship_sha PATCH stamps the caller, the move reads the stamp.
// Reviewer's PATCH lets the author close; the author's own PATCH does not.
func TestMoveTask_CommitShipReceipt_ThroughPatchMetadata(t *testing.T) {
	author, reviewer := uuid.New(), uuid.New()
	sha := strings.Repeat("c", 40)
	tr := domain.TransitionRule{AllowedActors: []string{actorPatternAuthorIfMergedShipped}}
	as := func(id uuid.UUID) context.Context {
		return actorctx.WithActor(context.Background(), id, domain.ActorTypeAgent)
	}

	cases := []struct {
		name    string
		shipper uuid.UUID
		blocked bool
	}{
		{"SHIP written by the reviewer", reviewer, false},
		{"SHIP written by the author", author, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, _, taskID, toID := journalSetup(t, domain.RuleConfigEnforcementStrict, tr,
				func(tk *domain.Task) { tk.PreReviewAssigneeID = &author })
			repo := NewMockVCSLinkRepository()
			svc.vcsLinkRepo = repo
			links := NewVCSLinkService(repo)
			link, _, err := links.Create(as(reviewer), domain.CreateVCSLinkInput{
				TaskID: taskID, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypeCommit,
				ExternalID: sha, URL: localCommitURLPrefix + sha,
			})
			require.NoError(t, err)
			_, err = links.PatchMetadata(as(c.shipper), taskID, link.ID, map[string]any{"ship_sha": sha})
			require.NoError(t, err)

			err = svc.MoveTask(as(author), taskID, MoveTaskInput{StatusID: &toID})
			if !c.blocked {
				require.NoError(t, err)
				return
			}
			var ae *apierror.Error
			require.ErrorAs(t, err, &ae)
			assert.Equal(t, http.StatusForbidden, ae.Code)
			assert.Equal(t, "workflow_transition_blocked", ae.Message)
			assert.Contains(t, ae.Context["reasons"], domain.ShipGapByAuthor)
			assert.Contains(t, ae.Details, "commit "+sha+": "+domain.ShipGapByAuthor)
		})
	}
}

func shipSetOf(t *testing.T, l *domain.VCSLink) *domain.VCSActorStamp {
	t.Helper()
	st := l.ShipSet()
	require.NotNil(t, st, "ship_set must be stamped: %s", l.Metadata)
	return st
}

func TestPatchMetadata_ShipSHAIsStampedOnEveryNewValue(t *testing.T) {
	for _, lt := range []domain.VCSLinkType{domain.VCSLinkTypePR, domain.VCSLinkTypeCommit} {
		t.Run(string(lt), func(t *testing.T) {
			svc, _, link := patchMetaSetup(lt)
			first, second := uuid.New(), uuid.New()
			a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)

			got, err := svc.PatchMetadata(actorctx.WithActor(context.Background(), first, domain.ActorTypeAgent), link.TaskID, link.ID, map[string]any{"ship_sha": a})
			require.NoError(t, err)
			st := shipSetOf(t, got)
			assert.Equal(t, first.String(), st.ByID)
			assert.Equal(t, string(domain.ActorTypeAgent), st.ByType)
			assert.NotEmpty(t, st.At)

			// Same value again by someone else: nothing is written, the stamp stands.
			again, err := svc.PatchMetadata(actorctx.WithActor(context.Background(), second, domain.ActorTypeAgent), link.TaskID, link.ID, map[string]any{"ship_sha": a})
			require.NoError(t, err)
			assert.Equal(t, first.String(), shipSetOf(t, again).ByID)

			// A new value restamps with its writer.
			moved, err := svc.PatchMetadata(actorctx.WithActor(context.Background(), second, domain.ActorTypeUser), link.TaskID, link.ID, map[string]any{"ship_sha": b})
			require.NoError(t, err)
			assert.Equal(t, second.String(), shipSetOf(t, moved).ByID)
			assert.Equal(t, string(domain.ActorTypeUser), shipSetOf(t, moved).ByType)
		})
	}
}

// Writes that do not change ship_sha leave ship_set alone.
func TestPatchMetadata_OtherKeysDoNotStampShipSet(t *testing.T) {
	svc, _, link := patchMetaSetup(domain.VCSLinkTypePR)
	got, err := svc.PatchMetadata(actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent), link.TaskID, link.ID,
		map[string]any{"merged_sha": strings.Repeat("d", 40)})
	require.NoError(t, err)
	assert.Nil(t, got.ShipSet())
}

func TestPatchMetadata_ShipSetIsNotAgentWritable(t *testing.T) {
	for _, lt := range []domain.VCSLinkType{domain.VCSLinkTypePR, domain.VCSLinkTypeCommit} {
		t.Run(string(lt), func(t *testing.T) {
			svc, _, link := patchMetaSetup(lt)
			forged := map[string]any{"by_id": uuid.New().String(), "by_type": "agent", "at": "2026-11-10T20:00:00Z"}
			for name, patch := range map[string]map[string]any{
				"ship_set alone":         {"ship_set": forged},
				"ship_set with ship_sha": {"ship_sha": strings.Repeat("b", 40), "ship_set": forged},
			} {
				_, err := svc.PatchMetadata(context.Background(), link.TaskID, link.ID, patch)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr, name)
				assert.Equal(t, http.StatusBadRequest, apiErr.Code, name)
			}
			assert.NotContains(t, string(link.Metadata), "ship_set")
			assert.NotContains(t, string(link.Metadata), "ship_sha")
		})
	}
}

// A commit has no MR, CI or deploy semantics: only ship_sha is writable on it.
func TestPatchMetadata_CommitLinkTakesShipSHAOnly(t *testing.T) {
	svc, _, link := patchMetaSetup(domain.VCSLinkTypeCommit)
	sha := strings.Repeat("e", 40)
	for name, patch := range map[string]map[string]any{
		"merged_sha":         {"merged_sha": sha},
		"implementation_sha": {"implementation_sha": sha},
		"ci_verdict":         {"ci_verdict": map[string]any{"status": "success"}},
		"deployed":           {"deployed": map[string]any{"sha": sha, "environment": "prod"}},
		"acceptance":         {"acceptance": map[string]any{"result": "passed"}},
		"role":               {"role": "review"},
		"ship_sha + other":   {"ship_sha": sha, "merged_sha": sha},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.PatchMetadata(context.Background(), link.TaskID, link.ID, patch)
			var apiErr *apierror.Error
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, http.StatusBadRequest, apiErr.Code)
		})
	}
	assert.NotContains(t, string(link.Metadata), sha, "nothing written by a rejected call")

	got, err := svc.PatchMetadata(context.Background(), link.TaskID, link.ID, map[string]any{"ship_sha": sha})
	require.NoError(t, err)
	assert.Equal(t, sha, got.ShipSHA())
}

func TestPatchShipSHAByURL_StampsShipSet(t *testing.T) {
	repo := newFakeVCSLinkRepo()
	link := &domain.VCSLink{ID: uuid.New(), TaskID: uuid.New(), Provider: domain.VCSProviderGitLab,
		LinkType: domain.VCSLinkTypePR, ExternalID: "42",
		URL: "https://git.example.org/example/product/-/merge_requests/42"}
	repo.links[link.ID] = link
	svc := NewVCSLinkService(repo).(*vcsLinkService)
	reviewer := uuid.New()

	got, err := svc.PatchShipSHAByURL(actorctx.WithActor(context.Background(), reviewer, domain.ActorTypeAgent),
		uuid.Nil, link.URL, strings.Repeat("f", 40), nil)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, reviewer.String(), shipSetOf(t, &got[0]).ByID)
}

// The commit link of a repo without MRs is a stable identifier nobody opens:
// neither the GitLab live-status probe nor the GitHub evidence sync may touch it.
func TestCommitLink_NotTouchedByForgeSync(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)
	checker := &fakeMRChecker{state: gitlabapi.MergeRequestState{Merged: true, State: "merged"}}
	svc := liveStatusService(h, func(context.Context, uuid.UUID) (gitlabapi.MergeRequestChecker, bool) { return checker, true })
	sha := strings.Repeat("9", 40)

	link, created, err := svc.Create(context.Background(), domain.CreateVCSLinkInput{
		TaskID: task.ID, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypeCommit,
		ExternalID: sha, URL: localCommitURLPrefix + sha,
	})
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, 0, checker.calls, "no GitLab call for a commit link")
	assert.NotEqual(t, domain.VCSLinkStatusMerged, link.Status)

	gh := commitLinkWithShip(sha, sha, uuid.New())
	gh.Provider = domain.VCSProviderGitHub
	assert.False(t, githubLinkIsDelivery(gh))
	assert.False(t, githubLinkNeedsEvidence(gh))
}
