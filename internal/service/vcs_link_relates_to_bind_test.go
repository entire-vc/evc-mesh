package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	gitlabapi "github.com/entire-vc/evc-mesh/internal/integration/gitlab"
)

// The webhook stored a card's own MR (a repository its project had
// no links to) as a relates_to mention. An explicit add_vcs_link of that MR is
// the binding "this is the card's work" and must clear the mark; before the
// fix the metadata merge kept it and the gate never read the link.

const mentionMRURL = "https://git.example.org/entire-vc/evc-mesh-runner/-/merge_requests/500"

func seedMention(repo *fakeVCSLinkRepo, status domain.VCSLinkStatus) *domain.VCSLink {
	l := &domain.VCSLink{
		ID: uuid.New(), TaskID: uuid.New(), Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR,
		ExternalID: "500", URL: mentionMRURL, Status: status,
		Metadata: json.RawMessage(`{"relation":"relates_to","head_sha":"` + strings.Repeat("8", 40) + `"}`),
	}
	repo.links[l.ID] = l
	repo.upsertKey[conflictKey(l.TaskID, l.Provider, l.LinkType, l.ExternalID, l.URL)] = l.ID
	return l
}

func addInput(l *domain.VCSLink, status domain.VCSLinkStatus) domain.CreateVCSLinkInput {
	return domain.CreateVCSLinkInput{
		TaskID: l.TaskID, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR,
		ExternalID: "500", URL: mentionMRURL, Status: status,
	}
}

func TestCreate_ExplicitStatusBindsRelatesToMention(t *testing.T) {
	repo := newFakeVCSLinkRepo()
	mention := seedMention(repo, domain.VCSLinkStatusMerged)
	svc := NewVCSLinkService(repo)

	got, created, err := svc.Create(context.Background(), addInput(mention, domain.VCSLinkStatusMerged))
	require.NoError(t, err)
	assert.False(t, created, "the existing row is updated, not duplicated")
	assert.Equal(t, mention.ID, got.ID)

	stored := repo.links[mention.ID]
	assert.False(t, stored.IsRelatesTo(), "explicit add must clear relation=relates_to: %s", stored.Metadata)
	var meta map[string]any
	require.NoError(t, json.Unmarshal(stored.Metadata, &meta))
	assert.Equal(t, strings.Repeat("8", 40), meta["head_sha"], "other metadata survives")
	assert.NotNil(t, meta[domain.VCSLinkMetaRelationBound], "who bound it is recorded")

	judged, _ := judgeDeliveryLinks([]domain.VCSLink{*stored}, nil)
	assert.Len(t, judged, 1, "the gate now reads the link")
}

func TestCreate_WithoutStatusBindsMentionInPlace(t *testing.T) {
	repo := newFakeVCSLinkRepo()
	mention := seedMention(repo, domain.VCSLinkStatusMerged)
	svc := NewVCSLinkService(repo)

	got, created, err := svc.Create(context.Background(), addInput(mention, ""))
	require.NoError(t, err, "a re-add of a mention is a binding, not a duplicate")
	assert.False(t, created)
	assert.Equal(t, mention.ID, got.ID)
	stored := repo.links[mention.ID]
	assert.False(t, stored.IsRelatesTo())
	assert.Equal(t, domain.VCSLinkStatusMerged, stored.Status, "binding never resets the measured status")
}

// Positive control: a plain duplicate of a delivery link still fails loudly,
// and a link of another url keeps its mention mark.
func TestCreate_NonMentionDuplicateStillRefused(t *testing.T) {
	repo := newFakeVCSLinkRepo()
	mention := seedMention(repo, domain.VCSLinkStatusMerged)
	repo.links[mention.ID].Metadata = json.RawMessage(`{}`)
	svc := NewVCSLinkService(repo)

	_, _, err := svc.Create(context.Background(), addInput(mention, ""))
	require.Error(t, err)

	other := seedMention(repo, domain.VCSLinkStatusMerged)
	in := addInput(other, "")
	in.URL, in.ExternalID = strings.Replace(mentionMRURL, "/500", "/501", 1), "501"
	_, created, err := svc.Create(context.Background(), in)
	require.NoError(t, err)
	assert.True(t, created)
	assert.True(t, repo.links[other.ID].IsRelatesTo(), "an add of another url leaves the mention alone")
}

func TestDiagnosis_NoDeliveryLinkNamesRelatesToLink(t *testing.T) {
	author := uuid.New()
	task := &domain.Task{PreReviewAssigneeID: &author}
	cfg := domain.WorkflowRulesConfig{
		EnforcementMode: domain.RuleConfigEnforcementStrict,
		Transitions: map[string]domain.TransitionRule{
			"review": {AllowedActors: []string{"not_author", actorPatternAuthorIfMergedShipped}},
		},
	}
	green := "success"
	l := shippedLink(domain.VCSLinkStatusMerged, "h1", "h1", &green)
	l.ID, l.URL = uuid.New(), mentionMRURL
	l.Metadata = []byte(`{"relation":"relates_to","head_sha":"h1","ship_sha":"h1"}`)

	_, v, diag := evaluateTransitionGate("review", "done", transitionActor{ID: author, Type: domain.ActorTypeAgent}, task, []domain.VCSLink{l}, cfg)
	require.NotEmpty(t, v)
	require.NotNil(t, diag)
	assert.Equal(t, []string{ReasonNoDeliveryLink}, diag.Reasons)
	require.Len(t, diag.Mentions, 1)
	assert.Equal(t, l.ID, diag.Mentions[0].LinkID)

	ae := diag.apiError(v)
	next, _ := ae.Context["next_action"].(string)
	assert.Contains(t, next, l.ID.String())
	assert.Contains(t, next, "relation=relates_to")
	assert.NotContains(t, next, "re-publish it so", "must not send the author to re-publish a receipt")
	assert.Contains(t, ae.Details, next)
	assert.NotNil(t, ae.Context["mentions"])
}

// Review of !1831: with a GitLab live check wired, a no-status bind must still
// take the metadata-only path: no live call, stored status untouched.
func TestCreate_WithoutStatusBindsMention_LiveCheckDoesNotOverwriteStatus(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryReview)
	mention := seedMention(h.repo, domain.VCSLinkStatusOpen)
	h.repo.links[mention.ID].TaskID = task.ID
	checker := &fakeMRChecker{state: gitlabapi.MergeRequestState{State: "closed"}}
	svc := liveStatusService(h, func(context.Context, uuid.UUID) (gitlabapi.MergeRequestChecker, bool) { return checker, true })

	in := addInput(mention, "")
	in.TaskID = task.ID
	got, created, err := svc.Create(context.Background(), in)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, mention.ID, got.ID)
	stored := h.repo.links[mention.ID]
	assert.False(t, stored.IsRelatesTo())
	assert.Equal(t, domain.VCSLinkStatusOpen, stored.Status, "a bind without status never rewrites the stored status")
	assert.Equal(t, 0, checker.calls, "no live check for a metadata-only bind")
}
