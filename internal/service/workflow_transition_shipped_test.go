package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func shippedLink(status domain.VCSLinkStatus, head, ship string, pipeline *string) domain.VCSLink {
	meta := map[string]string{}
	if head != "" {
		meta[domain.VCSLinkMetaHeadSHA] = head
		meta[domain.VCSLinkMetaPipelineSHA] = head
	}
	if ship != "" {
		meta[domain.VCSLinkMetaShipSHA] = ship
	}
	b, _ := json.Marshal(meta)
	return domain.VCSLink{LinkType: domain.VCSLinkTypePR, Status: status, Metadata: b, PipelineStatus: pipeline}
}

func TestAuthorIfMergedShipped_Gate(t *testing.T) {
	author, other := uuid.New(), uuid.New()
	task := &domain.Task{PreReviewAssigneeID: &author}
	green := "success"
	failed := "failed"
	cfg := func(enf string) domain.WorkflowRulesConfig {
		return domain.WorkflowRulesConfig{
			EnforcementMode: enf,
			Transitions: map[string]domain.TransitionRule{
				"review": {AllowedActors: []string{"not_author", actorPatternAuthorIfMergedShipped}},
			},
		}
	}
	agent := func(id uuid.UUID) transitionActor { return transitionActor{ID: id, Type: domain.ActorTypeAgent} }
	good := shippedLink(domain.VCSLinkStatusMerged, "abc123", "abc123", &green)

	cases := []struct {
		name    string
		actor   transitionActor
		links   []domain.VCSLink
		blocked bool
	}{
		{"author + merged + SHIP on head + CI green: allowed", agent(author), []domain.VCSLink{good}, false},
		{"author, not merged", agent(author), []domain.VCSLink{shippedLink(domain.VCSLinkStatusOpen, "abc123", "abc123", &green)}, true},
		{"author, SHIP on an older head", agent(author), []domain.VCSLink{shippedLink(domain.VCSLinkStatusMerged, "def456", "abc123", &green)}, true},
		{"author, no ship_sha", agent(author), []domain.VCSLink{shippedLink(domain.VCSLinkStatusMerged, "abc123", "", &green)}, true},
		{"author, no head_sha", agent(author), []domain.VCSLink{shippedLink(domain.VCSLinkStatusMerged, "", "abc123", &green)}, true},
		{"author, CI red", agent(author), []domain.VCSLink{shippedLink(domain.VCSLinkStatusMerged, "abc123", "abc123", &failed)}, true},
		{"author, CI unknown", agent(author), []domain.VCSLink{shippedLink(domain.VCSLinkStatusMerged, "abc123", "abc123", nil)}, true},
		{"author, no links", agent(author), nil, true},
		{"author, one good + one unproven MR", agent(author), []domain.VCSLink{good, shippedLink(domain.VCSLinkStatusMerged, "x", "", &green)}, true},
		{"author, good + MR closed without merge", agent(author), []domain.VCSLink{good, shippedLink(domain.VCSLinkStatusClosed, "x", "", nil)}, true},
		{"author, only a closed MR", agent(author), []domain.VCSLink{shippedLink(domain.VCSLinkStatusClosed, "x", "x", &green)}, true},
		{"non-author passes as before without evidence", agent(other), nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mode, v, _ := evaluateTransitionGate("review", "done", c.actor, task, c.links, cfg(domain.RuleConfigEnforcementStrict))
			assert.Equal(t, domain.RuleConfigEnforcementStrict, mode)
			assert.Equal(t, c.blocked, v != "", "violation=%q", v)
		})
	}
}

// Without the pattern in the rule nothing changes: the author is refused no
// matter what the links say.
func TestAuthorIfMergedShipped_AbsentPatternChangesNothing(t *testing.T) {
	author := uuid.New()
	task := &domain.Task{PreReviewAssigneeID: &author}
	green := "success"
	cfg := domain.WorkflowRulesConfig{
		EnforcementMode: domain.RuleConfigEnforcementStrict,
		Transitions:     map[string]domain.TransitionRule{"review": {AllowedActors: []string{"not_author"}}},
	}
	links := []domain.VCSLink{shippedLink(domain.VCSLinkStatusMerged, "a", "a", &green)}
	_, v, _ := evaluateTransitionGate("review", "done", transitionActor{ID: author, Type: domain.ActorTypeAgent}, task, links, cfg)
	assert.NotEmpty(t, v)
}

func TestAuthorIfMergedShipped_ReviewSubjectLinkIgnored(t *testing.T) {
	author := uuid.New()
	task := &domain.Task{PreReviewAssigneeID: &author}
	green := "success"
	good := shippedLink(domain.VCSLinkStatusMerged, "a", "a", &green)
	subject := shippedLink(domain.VCSLinkStatusMerged, "x", "", nil)
	subject.Metadata = []byte(`{"role":"review"}`)
	assert.True(t, isTransitionActorAllowed(transitionActor{ID: author, Type: domain.ActorTypeAgent}, task,
		[]domain.VCSLink{good, subject}, []string{actorPatternAuthorIfMergedShipped}))
	assert.False(t, isTransitionActorAllowed(transitionActor{ID: author, Type: domain.ActorTypeAgent}, task,
		[]domain.VCSLink{subject}, []string{actorPatternAuthorIfMergedShipped}))
}

func TestGitLabLinkMetadata(t *testing.T) {
	assert.JSONEq(t, `{"head_sha":""}`, string(gitLabLinkMetadata("")))
	assert.JSONEq(t, `{"head_sha":"abc"}`, string(gitLabLinkMetadata(" abc ")))
}

// End to end through MoveTask: the gate reads the card's links from the repo
// and refuses/permits the author in strict mode.
func TestMoveTask_AuthorIfMergedShipped_ReadsCardLinks(t *testing.T) {
	author := uuid.New()
	green := "success"
	tr := domain.TransitionRule{AllowedActors: []string{actorPatternAuthorIfMergedShipped}}
	set := func(tk *domain.Task) { tk.PreReviewAssigneeID = &author }
	actorCtx := func() context.Context { return actorctx.WithActor(context.Background(), author, domain.ActorTypeAgent) }

	cases := []struct {
		name    string
		link    *domain.VCSLink
		blocked bool
	}{
		{"merged + SHIP on head + green", ptrLink(shippedLink(domain.VCSLinkStatusMerged, "h1", "h1", &green)), false},
		{"SHIP on an older head", ptrLink(shippedLink(domain.VCSLinkStatusMerged, "h2", "h1", &green)), true},
		{"no links at all", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, _, taskID, toID := journalSetup(t, domain.RuleConfigEnforcementStrict, tr, set)
			repo := NewMockVCSLinkRepository()
			svc.vcsLinkRepo = repo
			if c.link != nil {
				c.link.ID, c.link.TaskID = uuid.New(), taskID
				require.NoError(t, repo.Create(context.Background(), c.link))
			}
			err := svc.MoveTask(actorCtx(), taskID, MoveTaskInput{StatusID: &toID})
			if c.blocked {
				var ae *apierror.Error
				require.ErrorAs(t, err, &ae)
				assert.Equal(t, "workflow_transition_blocked", ae.Message)
				assert.Equal(t, "author_if_merged_shipped", ae.Context["gate"], "refusal must carry the evidence diagnosis")
				assert.NotEmpty(t, ae.Context["reasons"])
				return
			}
			require.NoError(t, err)
		})
	}
}

func ptrLink(l domain.VCSLink) *domain.VCSLink { return &l }

func TestAuthorIfMergedShipped_ReportedAsPossibleForAgents(t *testing.T) {
	assert.True(t, isAgentAllowedByActors([]string{actorPatternAuthorIfMergedShipped}))
	assert.True(t, isConditionalActorPattern(actorPatternAuthorIfMergedShipped))
	assert.False(t, hasActorPattern([]string{"author"}, actorPatternAuthorIfMergedShipped))
}

func TestAuthorIfMergedShipped_Diagnosis(t *testing.T) {
	author, other := uuid.New(), uuid.New()
	task := &domain.Task{PreReviewAssigneeID: &author}
	green, red := "success", "failed"
	cfg := domain.WorkflowRulesConfig{
		EnforcementMode: domain.RuleConfigEnforcementStrict,
		Transitions: map[string]domain.TransitionRule{
			"review": {AllowedActors: []string{"not_author", actorPatternAuthorIfMergedShipped}},
		},
	}
	agent := func(id uuid.UUID) transitionActor { return transitionActor{ID: id, Type: domain.ActorTypeAgent} }
	stalePipeline := shippedLink(domain.VCSLinkStatusMerged, "h1", "h1", &green)
	stalePipeline.Metadata = []byte(`{"head_sha":"h1","ship_sha":"h1","pipeline_sha":"h0"}`)

	cases := []struct {
		name    string
		links   []domain.VCSLink
		reasons []string
	}{
		{"missing ship_sha", []domain.VCSLink{shippedLink(domain.VCSLinkStatusMerged, "h1", "", &green)}, []string{domain.ShipGapMissingShipSHA}},
		{"stale ship_sha", []domain.VCSLink{shippedLink(domain.VCSLinkStatusMerged, "h2", "h1", &green)}, []string{domain.ShipGapStaleShipSHA}},
		{"pipeline on older head", []domain.VCSLink{stalePipeline}, []string{domain.ShipGapPipelineSHA}},
		{"red CI", []domain.VCSLink{shippedLink(domain.VCSLinkStatusMerged, "h1", "h1", &red)}, []string{domain.ShipGapPipelineNotGreen}},
		{"open MR", []domain.VCSLink{shippedLink(domain.VCSLinkStatusOpen, "h1", "h1", &green)}, []string{domain.ShipGapMRNotMerged}},
		{"open MR without ship or CI", []domain.VCSLink{shippedLink(domain.VCSLinkStatusOpen, "h1", "", nil)},
			[]string{domain.ShipGapMRNotMerged, domain.ShipGapMissingShipSHA, domain.ShipGapPipelineNotGreen}},
		{"neither head_sha nor ship_sha", []domain.VCSLink{shippedLink(domain.VCSLinkStatusMerged, "", "", &green)},
			[]string{domain.ShipGapMissingShipSHA, domain.ShipGapMissingHeadSHA}},
		{"no delivery link", nil, []string{ReasonNoDeliveryLink}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, v, diag := evaluateTransitionGate("review", "done", agent(author), task, c.links, cfg)
			require.NotEmpty(t, v)
			require.NotNil(t, diag)
			assert.Equal(t, c.reasons, diag.Reasons)
			ae := diag.apiError(v)
			assert.Equal(t, "workflow_transition_blocked", ae.Message)
			assert.Equal(t, 403, ae.Code)
			assert.Contains(t, ae.Details, ShipNextAction)
			assert.Equal(t, c.reasons, ae.Context["reasons"])
		})
	}

	t.Run("good link passes, no diagnosis", func(t *testing.T) {
		_, v, diag := evaluateTransitionGate("review", "done", agent(author), task,
			[]domain.VCSLink{shippedLink(domain.VCSLinkStatusMerged, "h1", "h1", &green)}, cfg)
		assert.Empty(t, v)
		assert.Nil(t, diag)
	})
	t.Run("every delivery link is reported with its id", func(t *testing.T) {
		good := shippedLink(domain.VCSLinkStatusMerged, "h1", "h1", &green)
		bad := shippedLink(domain.VCSLinkStatusMerged, "h2", "", &green)
		bad.ID, bad.ExternalID = uuid.New(), "276"
		subject := shippedLink(domain.VCSLinkStatusOpen, "x", "", nil)
		subject.Metadata = []byte(`{"role":"review"}`)
		_, _, diag := evaluateTransitionGate("review", "done", agent(author), task, []domain.VCSLink{good, bad, subject}, cfg)
		require.NotNil(t, diag)
		require.Len(t, diag.Links, 2)
		assert.Empty(t, diag.Links[0].Reasons)
		assert.Equal(t, bad.ID, diag.Links[1].LinkID)
		assert.Equal(t, []string{domain.ShipGapMissingShipSHA}, diag.Links[1].Reasons)
	})
	t.Run("non-author refusal keeps its own plain type", func(t *testing.T) {
		strictOnly := cfg
		strictOnly.Transitions = map[string]domain.TransitionRule{"review": {AllowedActors: []string{actorPatternAuthorIfMergedShipped}}}
		_, v, diag := evaluateTransitionGate("review", "done", agent(other), task, nil, strictOnly)
		assert.NotEmpty(t, v)
		assert.Nil(t, diag)
	})
	t.Run("disallowed target keeps its own type", func(t *testing.T) {
		c2 := cfg
		c2.Transitions = map[string]domain.TransitionRule{"review": {Allowed: []string{"todo"}, AllowedActors: []string{actorPatternAuthorIfMergedShipped}}}
		_, v, diag := evaluateTransitionGate("review", "done", agent(author), task, nil, c2)
		assert.Contains(t, v, "not permitted; allowed targets")
		assert.Nil(t, diag)
	})
	t.Run("gaps agree with IsMergedShippedGreen", func(t *testing.T) {
		for _, l := range []domain.VCSLink{
			shippedLink(domain.VCSLinkStatusMerged, "h1", "h1", &green),
			shippedLink(domain.VCSLinkStatusMerged, "h1", "", &green),
			shippedLink(domain.VCSLinkStatusOpen, "h1", "h1", &green),
		} {
			assert.Equal(t, len(l.ShipEvidenceGaps()) == 0, l.IsMergedShippedGreen())
		}
	})
}
