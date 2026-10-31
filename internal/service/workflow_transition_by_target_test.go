package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func reviewByTargetCfg() domain.WorkflowRulesConfig {
	return domain.WorkflowRulesConfig{
		EnforcementMode: domain.RuleConfigEnforcementStrict,
		Transitions: map[string]domain.TransitionRule{
			"review": {
				AllowedActors: []string{"not_author"},
				AllowedActorsByTarget: map[string][]string{
					"done":        {"not_author", "user", actorPatternAuthorIfMergedShipped},
					"in_progress": {"*"},
					"todo":        {"*"},
				},
			},
		},
	}
}

func TestAllowedActorsByTarget_Gate(t *testing.T) {
	author, other := uuid.New(), uuid.New()
	task := &domain.Task{PreReviewAssigneeID: &author}
	green := "success"
	good := []domain.VCSLink{shippedLink(domain.VCSLinkStatusMerged, "a", "a", &green)}
	agent := func(id uuid.UUID) transitionActor { return transitionActor{ID: id, Type: domain.ActorTypeAgent} }

	cases := []struct {
		name    string
		actor   transitionActor
		to      string
		links   []domain.VCSLink
		blocked bool
	}{
		{"author review->in_progress allowed", agent(author), "in_progress", nil, false},
		{"author review->todo allowed", agent(author), "todo", nil, false},
		{"author review->done without merged+SHIP refused", agent(author), "done", nil, true},
		{"author review->done with merged+SHIP allowed", agent(author), "done", good, false},
		{"non-author review->done allowed", agent(other), "done", nil, false},
		{"target without entry falls back to allowed_actors: author refused", agent(author), "cancelled", nil, true},
		{"target without entry falls back to allowed_actors: non-author allowed", agent(other), "cancelled", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, v, _ := evaluateTransitionGate("review", c.to, c.actor, task, c.links, reviewByTargetCfg())
			assert.Equal(t, c.blocked, v != "", "violation=%q", v)
		})
	}
}

// Without the new field the rule behaves exactly as before.
func TestAllowedActorsByTarget_AbsentFieldChangesNothing(t *testing.T) {
	author := uuid.New()
	task := &domain.Task{PreReviewAssigneeID: &author}
	cfg := reviewByTargetCfg()
	tr := cfg.Transitions["review"]
	tr.AllowedActorsByTarget = nil
	cfg.Transitions["review"] = tr
	for _, to := range []string{"in_progress", "todo", "done"} {
		_, v, _ := evaluateTransitionGate("review", to, transitionActor{ID: author, Type: domain.ActorTypeAgent}, task, nil, cfg)
		assert.NotEmpty(t, v, to)
	}
}

func TestAllowedActorsByTarget_JSONRoundTrip(t *testing.T) {
	b, err := json.Marshal(reviewByTargetCfg())
	require.NoError(t, err)
	var back domain.WorkflowRulesConfig
	require.NoError(t, json.Unmarshal(b, &back))
	assert.Equal(t, []string{"*"}, back.Transitions["review"].AllowedActorsByTarget["todo"])
	assert.Contains(t, string(b), `"allowed_actors_by_target"`)
}

func TestValidateAllowedActorsByTarget(t *testing.T) {
	known := map[string]bool{"done": true, "todo": true, "in_progress": true, "review": true}
	assert.NoError(t, validateAllowedActorsByTarget(reviewByTargetCfg(), known))
	assert.NoError(t, validateAllowedActorsByTarget(reviewByTargetCfg(), nil))

	bad := func(target string, actors ...string) domain.WorkflowRulesConfig {
		return domain.WorkflowRulesConfig{Transitions: map[string]domain.TransitionRule{
			"review": {AllowedActorsByTarget: map[string][]string{target: actors}},
		}}
	}
	assert.Error(t, validateAllowedActorsByTarget(bad("nope", "*"), known), "unknown target status")
	assert.Error(t, validateAllowedActorsByTarget(bad("done", "not_writer"), known), "unknown pattern")
	assert.Error(t, validateAllowedActorsByTarget(bad("done", "bogus"), nil), "unknown pattern without status list")
}

// PUT must fail closed on unknown targets: an empty status list or a failed
// status lookup may not let an unchecked target through.
func TestSetProjectWorkflowRules_ByTargetFailsClosed(t *testing.T) {
	cfg := reviewByTargetCfg()

	empty := NewMockTaskStatusRepository()
	svc := &rulesService{statusRepo: empty}
	assert.Error(t, svc.SetProjectWorkflowRules(context.Background(), uuid.New(), cfg), "empty status list")

	broken := NewMockTaskStatusRepository()
	broken.errToReturn = errors.New("db down")
	svc = &rulesService{statusRepo: broken}
	assert.Error(t, svc.SetProjectWorkflowRules(context.Background(), uuid.New(), cfg), "status lookup error")
}

func TestSetProjectWorkflowRules_ByTargetWithoutStatusRepoRefused(t *testing.T) {
	svc := &rulesService{}
	assert.Error(t, svc.SetProjectWorkflowRules(context.Background(), uuid.New(), reviewByTargetCfg()))
}
