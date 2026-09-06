package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// ---------------------------------------------------------------------------
// Mention-handoff gate (task #9d8f7606, audit §3.1).
//
// Three branches, per the task's acceptance criterion:
//   - gated:                plain @-mention, no queue path, no accompanying
//     assign_task/create_subtask → refused.
//   - fyi-allowed:           "fyi: @slug" bypasses the gate entirely.
//   - accompanied-by-assignment: a recent assign_task or create_subtask onto
//     the mentioned agent counts as the hand-off the gate asks for.
//
// Plus the edges the design explicitly calls out: the handoff window expires,
// self-mentions and human usernames are never gated, an unresolved handle is
// out of scope, and a per-agent capability flag opts a lane out entirely.
// ---------------------------------------------------------------------------

func newGatedTaskEnv() (mentionTestEnv, uuid.UUID) {
	env := setupCommentServiceWithMentions()
	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: env.projID, Title: "T",
		AssigneeType: domain.AssigneeTypeUnassigned,
	}
	return env, taskID
}

// --- Branch 1: gated -------------------------------------------------------

// TestEnforceMentionHandoffGate_PlainMentionWithNoQueuePathIsRefused is the
// headline case the whole gate exists for: "@wally — нужен вердикт" with no
// assign_task and no move to todo. Before this gate the comment published
// and notified nothing that mattered (audit §3.1: 3.8% delivered by feed).
// enforceMentionHandoff opts a test into ENFORCEMENT. Shadow mode is the shipped
// default (see enforceMentionHandoffGate's note: hard-refusing today would 422 roughly
// two thirds of current agent-slug mentions, most of which are ordinary in-thread
// addressing rather than mis-believed handoffs). Tests that assert a refusal are testing
// the decision, so they say so out loud rather than inheriting whatever the default
// happens to be — if the default flips later, these keep testing what they claim to.
func enforceMentionHandoff(t *testing.T) {
	t.Helper()
	t.Setenv("MENTION_HANDOFF_ENFORCE", "1")
}

func TestEnforceMentionHandoffGate_PlainMentionWithNoQueuePathIsRefused(t *testing.T) {
	enforceMentionHandoff(t)
	env, taskID := newGatedTaskEnv()
	agent := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "wally"}
	env.agentSvc.AddAgent(env.wsID, agent)

	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeUser,
		Body:       "@wally — нужен вердикт",
	}
	err := env.svc.Create(context.Background(), comment)

	require.Error(t, err)
	var handoffErr *MentionHandoffRequiredError
	require.True(t, errors.As(err, &handoffErr), "want MentionHandoffRequiredError, got %T: %v", err, err)
	assert.Equal(t, []string{"wally"}, handoffErr.Slugs)
	assert.Equal(t, taskID, handoffErr.TaskID)

	// Assert on the RESULT, not just the returned error: the comment must
	// never have been written, and no mention/notify side effect must have
	// fired for a hand-off that never happened.
	assert.Empty(t, env.commentRepo.items, "refused comment must not be persisted")
	assert.Empty(t, env.notifySvc.Calls(), "no notification for a refused mention")
}

// TestEnforceMentionHandoffGate_RefusalNamesEveryUngatedSlug covers more than
// one blocked handle on the same comment: both must be named, not just the
// first found.
func TestEnforceMentionHandoffGate_RefusalNamesEveryUngatedSlug(t *testing.T) {
	enforceMentionHandoff(t)
	env, taskID := newGatedTaskEnv()
	env.agentSvc.AddAgent(env.wsID, &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "wally"})
	env.agentSvc.AddAgent(env.wsID, &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "frank"})

	comment := &domain.Comment{
		TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeUser,
		Body: "@wally @frank взгляните",
	}
	err := env.svc.Create(context.Background(), comment)

	var handoffErr *MentionHandoffRequiredError
	require.True(t, errors.As(err, &handoffErr))
	assert.ElementsMatch(t, []string{"wally", "frank"}, handoffErr.Slugs)
}

// --- Branch 2: fyi-allowed ---------------------------------------------------

// TestEnforceMentionHandoffGate_FyiPrefixIsExempt is the escape hatch: naming
// an agent purely for awareness, not as a hand-off, still goes through and
// still emits task.mentioned — the gate only refuses, it never silently
// swallows the mention.
func TestEnforceMentionHandoffGate_FyiPrefixIsExempt(t *testing.T) {
	env, taskID := newGatedTaskEnv()
	agent := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "wally"}
	env.agentSvc.AddAgent(env.wsID, agent)

	comment := &domain.Comment{
		TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeUser,
		Body: "fyi: @wally мы решили X, действия не нужны",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))

	assert.NotEmpty(t, env.commentRepo.items, "an fyi-exempt comment must persist")
	mentionCalls := filterByEvent(env.notifySvc.Calls(), "task.mentioned")
	require.Len(t, mentionCalls, 1, "fyi mentions still emit task.mentioned, unchanged")
	assert.Equal(t, agent.ID, mentionCalls[0].AgentID)
}

// TestEnforceMentionHandoffGate_FyiExemptsOnlyItsOwnSlug: a body with BOTH an
// fyi-prefixed mention and a bare one for a DIFFERENT agent must still gate
// the bare one. The exemption is per occurrence-intent, not a blanket pass.
func TestEnforceMentionHandoffGate_FyiExemptsOnlyItsOwnSlug(t *testing.T) {
	enforceMentionHandoff(t)
	env, taskID := newGatedTaskEnv()
	env.agentSvc.AddAgent(env.wsID, &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "wally"})
	env.agentSvc.AddAgent(env.wsID, &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "frank"})

	comment := &domain.Comment{
		TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeUser,
		Body: "fyi: @wally, но @frank нужно разобраться",
	}
	err := env.svc.Create(context.Background(), comment)

	var handoffErr *MentionHandoffRequiredError
	require.True(t, errors.As(err, &handoffErr))
	assert.Equal(t, []string{"frank"}, handoffErr.Slugs, "wally is fyi-exempt, frank is not")
}

// --- Branch 3: accompanied-by-assignment ------------------------------------

// TestEnforceMentionHandoffGate_RecentAssignAccompaniesMention: assign_task
// landed on THIS task within the handoff window, even though the follow-up
// move to a todo-category status has not (yet) landed as a separate call —
// the assignment itself, this recently, is the accompanying hand-off.
func TestEnforceMentionHandoffGate_RecentAssignAccompaniesMention(t *testing.T) {
	env, taskID := newGatedTaskEnv()
	agent := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "linus"}
	env.agentSvc.AddAgent(env.wsID, agent)

	// Simulate `assign_task` 10s ago: current task state points at the agent
	// (no statusRepo wired, so taskInTodo is false — this exercises the
	// "not yet moved to todo" half of the accompanying-assignment path).
	task := env.taskRepo.items[taskID]
	task.AssigneeType = domain.AssigneeTypeAgent
	task.AssigneeID = &agent.ID

	require.NoError(t, env.svc.activityRepo.Create(context.Background(), &domain.ActivityLog{
		ID: uuid.New(), WorkspaceID: env.wsID, EntityType: "task", EntityID: taskID,
		Action: "task.assigned", CreatedAt: frozenTime.Add(-10 * time.Second),
	}))

	comment := &domain.Comment{
		TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeUser,
		Body: "@linus держи, разбери",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))
	assert.NotEmpty(t, env.commentRepo.items)
}

// TestEnforceMentionHandoffGate_StaleAssignIsNotAccompanying proves the
// window matters: the exact same setup as above, but the assignment is 6
// hours old — no longer "accompanying", so the gate still refuses.
func TestEnforceMentionHandoffGate_StaleAssignIsNotAccompanying(t *testing.T) {
	enforceMentionHandoff(t)
	env, taskID := newGatedTaskEnv()
	agent := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "linus"}
	env.agentSvc.AddAgent(env.wsID, agent)

	task := env.taskRepo.items[taskID]
	task.AssigneeType = domain.AssigneeTypeAgent
	task.AssigneeID = &agent.ID

	require.NoError(t, env.svc.activityRepo.Create(context.Background(), &domain.ActivityLog{
		ID: uuid.New(), WorkspaceID: env.wsID, EntityType: "task", EntityID: taskID,
		Action: "task.assigned", CreatedAt: frozenTime.Add(-6 * time.Hour),
	}))

	comment := &domain.Comment{
		TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeUser,
		Body: "@linus взгляни ещё раз",
	}
	err := env.svc.Create(context.Background(), comment)

	var handoffErr *MentionHandoffRequiredError
	require.True(t, errors.As(err, &handoffErr), "a 6h-old assignment is not an accompanying hand-off")
}

// TestEnforceMentionHandoffGate_RecentSubtaskAccompaniesMention: the mention
// is on the PARENT task, but a subtask of it, assigned to the mentioned
// agent, was just created — the parent's own assignee never changed, but the
// mention points at real work the agent will see.
func TestEnforceMentionHandoffGate_RecentSubtaskAccompaniesMention(t *testing.T) {
	env, parentID := newGatedTaskEnv()
	agent := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "bart"}
	env.agentSvc.AddAgent(env.wsID, agent)

	subID := uuid.New()
	env.taskRepo.items[subID] = &domain.Task{
		ID: subID, ProjectID: env.projID, Title: "sub", ParentTaskID: &parentID,
		AssigneeType: domain.AssigneeTypeAgent, AssigneeID: &agent.ID,
		CreatedAt: frozenTime.Add(-5 * time.Second),
	}

	comment := &domain.Comment{
		TaskID: parentID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeUser,
		Body: "@bart смотри подзадачу выше",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))
	assert.NotEmpty(t, env.commentRepo.items)
}

// TestEnforceMentionHandoffGate_StaleSubtaskIsNotAccompanying: the window
// applies to create_subtask the same way it applies to assign_task.
func TestEnforceMentionHandoffGate_StaleSubtaskIsNotAccompanying(t *testing.T) {
	enforceMentionHandoff(t)
	env, parentID := newGatedTaskEnv()
	agent := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "bart"}
	env.agentSvc.AddAgent(env.wsID, agent)

	subID := uuid.New()
	env.taskRepo.items[subID] = &domain.Task{
		ID: subID, ProjectID: env.projID, Title: "sub", ParentTaskID: &parentID,
		AssigneeType: domain.AssigneeTypeAgent, AssigneeID: &agent.ID,
		CreatedAt: frozenTime.Add(-3 * 24 * time.Hour),
	}

	comment := &domain.Comment{
		TaskID: parentID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeUser,
		Body: "@bart там же было что-то",
	}
	err := env.svc.Create(context.Background(), comment)

	var handoffErr *MentionHandoffRequiredError
	require.True(t, errors.As(err, &handoffErr))
}

// --- Exemptions orthogonal to the three branches ----------------------------

// TestEnforceMentionHandoffGate_SelfMentionExempt: nobody hands work to
// themselves; the gate must not refuse an agent naming its own slug.
func TestEnforceMentionHandoffGate_SelfMentionExempt(t *testing.T) {
	env, taskID := newGatedTaskEnv()
	agent := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "howard"}
	env.agentSvc.AddAgent(env.wsID, agent)

	ctx := actorctx.WithActor(context.Background(), agent.ID, domain.ActorTypeAgent)
	comment := &domain.Comment{
		TaskID: taskID, AuthorID: agent.ID, AuthorType: domain.ActorTypeAgent,
		Body: "заметка себе: @howard проверить завтра",
	}
	require.NoError(t, env.svc.Create(ctx, comment))
	assert.NotEmpty(t, env.commentRepo.items)
}

// TestEnforceMentionHandoffGate_HumanUsernameNeverGated: @pavel and any other
// human has a real notification channel this gate has no business
// second-guessing — only agent slugs are ever refused.
func TestEnforceMentionHandoffGate_HumanUsernameNeverGated(t *testing.T) {
	env, taskID := newGatedTaskEnv()
	userRepo := NewMockUserRepository()
	pavel := &domain.User{ID: uuid.New(), Username: "pavel"}
	userRepo.AddUser(env.wsID, pavel)
	env.svc.userRepo = userRepo

	comment := &domain.Comment{
		TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeUser,
		Body: "❓ **Blocking @pavel**: нужен approve",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))
	assert.NotEmpty(t, env.commentRepo.items)
}

// TestEnforceMentionHandoffGate_UnresolvedHandleNotGated: a typo'd or
// nonexistent handle is a separate, already-recorded case
// (skipped/recipient_unknown on the delivery ledger) — not a hand-off
// attempt this gate should judge.
func TestEnforceMentionHandoffGate_UnresolvedHandleNotGated(t *testing.T) {
	env, taskID := newGatedTaskEnv()

	comment := &domain.Comment{
		TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeUser,
		Body: "@nobody-such-agent смотри",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))
	assert.NotEmpty(t, env.commentRepo.items)
}

// TestEnforceMentionHandoffGate_CapabilityFlagExempt: an agent row that
// claims `capabilities.mention_wakes: true` opts out of the gate entirely —
// today that means the dispatcher-driven lane, expressed as data on its own
// row rather than a hardcoded slug in this file.
func TestEnforceMentionHandoffGate_CapabilityFlagExempt(t *testing.T) {
	env, taskID := newGatedTaskEnv()
	agent := &domain.Agent{
		ID: uuid.New(), WorkspaceID: env.wsID, Slug: "riker",
		Capabilities: []byte(`{"mention_wakes": true}`),
	}
	env.agentSvc.AddAgent(env.wsID, agent)

	comment := &domain.Comment{
		TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeUser,
		Body: "@riker нужен рестарт диспетчера",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))
	assert.NotEmpty(t, env.commentRepo.items)
}

// TestEnforceMentionHandoffGate_CapabilityFlagFalseStillGated: the flag must
// be an explicit opt-in — false, malformed, or absent capabilities all gate
// normally.
func TestEnforceMentionHandoffGate_CapabilityFlagFalseStillGated(t *testing.T) {
	enforceMentionHandoff(t)
	env, taskID := newGatedTaskEnv()
	agent := &domain.Agent{
		ID: uuid.New(), WorkspaceID: env.wsID, Slug: "marcus",
		Capabilities: []byte(`{"mention_wakes": false}`),
	}
	env.agentSvc.AddAgent(env.wsID, agent)

	comment := &domain.Comment{
		TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeUser,
		Body: "@marcus взгляни",
	}
	err := env.svc.Create(context.Background(), comment)

	var handoffErr *MentionHandoffRequiredError
	require.True(t, errors.As(err, &handoffErr))
}

// --- agentMentionAlreadyWakes / fyiExemptSlugs unit coverage ----------------

func TestAgentMentionAlreadyWakes(t *testing.T) {
	cases := []struct {
		name string
		a    *domain.Agent
		want bool
	}{
		{"nil agent", nil, false},
		{"no capabilities", &domain.Agent{}, false},
		{"malformed json", &domain.Agent{Capabilities: []byte(`not-json`)}, false},
		{"flag true", &domain.Agent{Capabilities: []byte(`{"mention_wakes": true}`)}, true},
		{"flag false", &domain.Agent{Capabilities: []byte(`{"mention_wakes": false}`)}, false},
		{"flag wrong type", &domain.Agent{Capabilities: []byte(`{"mention_wakes": "yes"}`)}, false},
		{"unrelated keys only", &domain.Agent{Capabilities: []byte(`{"no_lane": true}`)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, agentMentionAlreadyWakes(tc.a))
		})
	}
}

func TestFyiExemptSlugs(t *testing.T) {
	cases := []struct {
		name string
		body string
		want map[string]bool
	}{
		{"no fyi", "@wally please look", map[string]bool{}},
		{"simple fyi", "fyi: @wally already handled", map[string]bool{"wally": true}},
		{"case insensitive", "FYI: @Wally fixed", map[string]bool{"wally": true}},
		{"extra space before colon", "fyi : @wally ok", map[string]bool{"wally": true}},
		{"fyi word without colon does not exempt", "for your info @wally look", map[string]bool{}},
		{"two fyi mentions", "fyi: @wally and fyi: @frank", map[string]bool{"wally": true, "frank": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fyiExemptSlugs(tc.body)
			assert.Equal(t, len(tc.want), len(got))
			for k := range tc.want {
				assert.True(t, got[k], "expected %q exempt", k)
			}
		})
	}
}

// TestEnforceMentionHandoffGate_ShadowIsTheDefault pins the shipped behaviour. Without
// MENTION_HANDOFF_ENFORCE the gate must decide exactly as it would when enforcing, and
// then NOT refuse — the comment is persisted untouched.
//
// This is the half that protects the fleet: independent verification measured that hard
// enforcement would refuse ~65-78% of current agent-slug mentions on day one, most of
// them ordinary in-thread addressing rather than mis-believed handoffs. A regression
// that silently flipped the default to enforce would break inter-agent commenting
// fleet-wide within one deploy.
func TestEnforceMentionHandoffGate_ShadowIsTheDefault(t *testing.T) {
	// deliberately NOT calling enforceMentionHandoff(t)
	env, taskID := newGatedTaskEnv()
	agent := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "wally"}
	env.agentSvc.AddAgent(env.wsID, agent)

	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeUser,
		Body:       "@wally принято, правку не делай.",
	}
	err := env.svc.Create(context.Background(), comment)

	require.NoError(t, err,
		"shadow mode must not refuse — ordinary in-thread addressing is legitimate per "+
			"CLAUDE-communication.md §5a, and refusing it fleet-wide unannounced breaks "+
			"more than it fixes")
	assert.Len(t, env.commentRepo.items, 1,
		"and the comment must actually be stored, not merely un-errored")
}

// TestEnforceMentionHandoffGate_EnforceFlagIsHonoured is the negative control for the
// test above: the SAME input, differing only by the flag, must be refused and must NOT
// persist. Without it, "shadow does not refuse" would pass equally on a gate that
// decided nothing at all.
func TestEnforceMentionHandoffGate_EnforceFlagIsHonoured(t *testing.T) {
	enforceMentionHandoff(t)
	env, taskID := newGatedTaskEnv()
	agent := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "wally"}
	env.agentSvc.AddAgent(env.wsID, agent)

	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeUser,
		Body:       "@wally принято, правку не делай.",
	}
	err := env.svc.Create(context.Background(), comment)

	require.Error(t, err, "with the flag set, the identical mention must be refused")
	var handoffErr *MentionHandoffRequiredError
	require.True(t, errors.As(err, &handoffErr), "got %T: %v", err, err)
	assert.Equal(t, []string{"wally"}, handoffErr.Slugs)
	assert.Empty(t, env.commentRepo.items, "a refused comment must not be stored")
}
