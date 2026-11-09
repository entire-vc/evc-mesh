package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Task dependency delivery fix: a decision from the ask's addressee must reach the gate owner as ONE
// bounded admission, without widening who may clear a gate.

type gateDecisionEnv struct {
	triageTestEnv
	taskID        uuid.UUID
	ownerID       uuid.UUID
	coordinatorID uuid.UUID
	leadID        uuid.UUID
	userID        uuid.UUID
	markerID      uuid.UUID
}

func setupGateDecisionEnv(t *testing.T) gateDecisionEnv {
	t.Helper()
	agents := NewMockAgentService()
	env := setupTriageEnvWithOptions(t, true, WithCommentAgentService(agents))
	e := gateDecisionEnv{triageTestEnv: env, ownerID: uuid.New(), coordinatorID: uuid.New(), leadID: uuid.New(), userID: uuid.New()}
	agents.AddAgent(env.wsID, &domain.Agent{ID: e.ownerID, WorkspaceID: env.wsID, Slug: "crm-lane"})
	agents.AddAgent(env.wsID, &domain.Agent{ID: e.coordinatorID, WorkspaceID: env.wsID, Slug: "coordinator"})
	agents.AddAgent(env.wsID, &domain.Agent{ID: e.leadID, WorkspaceID: env.wsID, Slug: "some-lead"})
	e.taskID = env.seedGatedTask(env.inProgressID)
	e.markerID = e.add(e.ownerID, domain.ActorTypeAgent, "❓ **Blocking @coordinator**: спам или клиент?", -3*time.Hour)
	return e
}

func (e gateDecisionEnv) add(author uuid.UUID, at domain.ActorType, body string, off time.Duration) uuid.UUID {
	id := uuid.New()
	e.commentRepo.items[id] = &domain.Comment{ID: id, TaskID: e.taskID, AuthorID: author, AuthorType: at, Body: body, CreatedAt: frozenTime.Add(off)}
	return id
}

func TestGateDecision_AddresseeAnswer_IsPendingForOwner(t *testing.T) {
	e := setupGateDecisionEnv(t)
	did := e.add(e.coordinatorID, domain.ActorTypeAgent, "Это спам, отменяем карточку.", -2*time.Hour)
	info, err := e.svc.GetHumanGateOwner(context.Background(), e.taskID)
	require.NoError(t, err)
	assert.True(t, info.DecisionPending)
	require.NotNil(t, info.DecisionCommentID)
	assert.Equal(t, did, *info.DecisionCommentID)
	assert.Equal(t, *info.OwnerAgentID, e.ownerID)
	assert.Contains(t, info.NextAction, "withdraw")
	assert.Empty(t, e.taskMover.humanGateCalls(), "reporting a decision must never touch the gate")
}

func TestGateDecision_NegativeControls(t *testing.T) {
	cases := map[string]func(e gateDecisionEnv){
		"lead who was not asked": func(e gateDecisionEnv) { e.add(e.leadID, domain.ActorTypeAgent, "resolved, спам", -2*time.Hour) },
		"quote only": func(e gateDecisionEnv) {
			e.add(e.coordinatorID, domain.ActorTypeAgent, "> это спам, отменяем", -2*time.Hour)
		},
		"service comment": func(e gateDecisionEnv) {
			e.add(e.coordinatorID, domain.ActorTypeAgent, "[balancer] спам, отменяем", -2*time.Hour)
		},
		"owner's own comment": func(e gateDecisionEnv) {
			e.add(e.ownerID, domain.ActorTypeAgent, "Это спам, отменяем.", -2*time.Hour)
		},
		"system comment": func(e gateDecisionEnv) {
			e.add(uuid.Nil, domain.ActorTypeSystem, "Это спам, отменяем.", -2*time.Hour)
		},
		"decision older than marker": func(e gateDecisionEnv) {
			e.add(e.coordinatorID, domain.ActorTypeAgent, "Это спам.", -4*time.Hour)
		},
		"answer then concurrent new marker": func(e gateDecisionEnv) {
			e.add(e.coordinatorID, domain.ActorTypeAgent, "Это спам.", -2*time.Hour)
			e.add(e.ownerID, domain.ActorTypeAgent, "❓ **Blocking @coordinator**: а точно?", -1*time.Hour)
		},
	}
	for name, seed := range cases {
		t.Run(name, func(t *testing.T) {
			e := setupGateDecisionEnv(t)
			seed(e)
			info, err := e.svc.GetHumanGateOwner(context.Background(), e.taskID)
			require.NoError(t, err)
			assert.False(t, info.DecisionPending, name)
			assert.Nil(t, info.DecisionCommentID)
		})
	}
}

func TestGateDecision_HumanMustBeTheNamedAddressee(t *testing.T) {
	agents := NewMockAgentService()
	env := setupTriageEnvWithOptions(t, true, WithCommentAgentService(agents))
	alex, _ := env.userRepo.GetByUsername(context.Background(), env.wsID, "alex")
	require.NotNil(t, alex)
	stranger := uuid.New()
	env.userRepo.AddUser(env.wsID, &domain.User{ID: stranger, Username: "maria", Name: "Maria"})
	e := gateDecisionEnv{triageTestEnv: env, ownerID: uuid.New()}
	e.taskID = env.seedGatedTask(env.inProgressID)
	e.add(e.ownerID, domain.ActorTypeAgent, "❓ **Blocking @alex**: спам или клиент?", -3*time.Hour)

	e.add(stranger, domain.ActorTypeUser, "Это спам, отменяем.", -2*time.Hour)
	info, err := e.svc.GetHumanGateOwner(context.Background(), e.taskID)
	require.NoError(t, err)
	assert.False(t, info.DecisionPending, "a human the marker does not name must not count")

	did := e.add(alex.ID, domain.ActorTypeUser, "Спам, отменяем.", -1*time.Hour)
	info, err = e.svc.GetHumanGateOwner(context.Background(), e.taskID)
	require.NoError(t, err)
	require.True(t, info.DecisionPending)
	assert.Equal(t, did, *info.DecisionCommentID)
}

func TestGateDecision_RepeatMapsToNewestDecisionOnly(t *testing.T) {
	e := setupGateDecisionEnv(t)
	e.add(e.coordinatorID, domain.ActorTypeAgent, "Это спам.", -2*time.Hour)
	second := e.add(e.coordinatorID, domain.ActorTypeAgent, "Подтверждаю: спам.", -1*time.Hour)
	info, err := e.svc.GetHumanGateOwner(context.Background(), e.taskID)
	require.NoError(t, err)
	require.NotNil(t, info.DecisionCommentID)
	assert.Equal(t, second, *info.DecisionCommentID)
}

func TestDecideDelivery_GateOwnerDecision_IsTheOnlyGatedException(t *testing.T) {
	agent := &domain.Agent{ID: uuid.New()}
	base := deliveryFacts{Agent: agent, IsAssignee: true, Gated: true, StatusCategory: string(domain.StatusCategoryInProgress), Presence: domain.ComputedStatusOnline}

	o, r, ch, _ := decideDelivery(base)
	assert.Equal(t, domain.ReasonThreadOnly, r, "ordinary comment on a gated own card stays thread_only")
	assert.Equal(t, domain.ChannelThread, ch)
	_ = o

	base.GateOwnerDecision = true
	o, r, ch, _ = decideDelivery(base)
	assert.Equal(t, domain.DeliveryDelivered, o)
	assert.Equal(t, domain.ReasonGateOwnerDecision, r)
	assert.Equal(t, domain.ChannelGateOwner, ch)

	base.IsAssignee = false
	_, r, _, _ = decideDelivery(base)
	assert.NotEqual(t, domain.ReasonGateOwnerDecision, r, "a non-assignee is never admitted")
}

func TestDecideDelivery_GateOwnerDecision_PassesAFutureStartAfterButNothingElseDoes(t *testing.T) {
	agent := &domain.Agent{ID: uuid.New()}
	sched := deliveryFacts{Agent: agent, IsAssignee: true, Gated: true, Scheduled: true,
		StatusCategory: string(domain.StatusCategoryTodo), Presence: domain.ComputedStatusOnline}

	// positive: scheduled + authorized owner of the live decision
	pos := sched
	pos.GateOwnerDecision = true
	o, r, ch, _ := decideDelivery(pos)
	assert.Equal(t, domain.DeliveryDelivered, o)
	assert.Equal(t, domain.ReasonGateOwnerDecision, r)
	assert.Equal(t, domain.ChannelGateOwner, ch)

	// ordinary work on the same scheduled+gated card: still not reached
	o, r, _, _ = decideDelivery(sched)
	assert.Equal(t, domain.DeliverySkipped, o)
	assert.NotEqual(t, domain.ReasonGateOwnerDecision, r)

	// foreign recipient (not the assignee), even when flagged as decision owner
	foreign := pos
	foreign.IsAssignee = false
	o, r, _, _ = decideDelivery(foreign)
	assert.Equal(t, domain.DeliverySkipped, o)
	assert.NotEqual(t, domain.ReasonGateOwnerDecision, r)

	// scheduled, ungated card: the schedule still holds (no decision to admit)
	ungated := deliveryFacts{Agent: agent, IsAssignee: true, InTaskQueue: true, Scheduled: true,
		StatusCategory: string(domain.StatusCategoryTodo), Presence: domain.ComputedStatusOnline}
	o, r, _, _ = decideDelivery(ungated)
	assert.Equal(t, domain.DeliverySkipped, o)
	assert.Equal(t, domain.ReasonTaskScheduled, r)
}

func TestGateWithdrawalReceipt_FourOutcomes(t *testing.T) {
	e := setupGateDecisionEnv(t)
	task := e.taskRepo.items[e.taskID]
	mk := func(author uuid.UUID) *domain.Comment {
		return &domain.Comment{ID: uuid.New(), TaskID: e.taskID, AuthorID: author, AuthorType: domain.ActorTypeAgent,
			Body: "Вопрос снят — не нужно.", CreatedAt: frozenTime}
	}
	ctx := context.Background()

	// not_owner: someone else's withdrawal on a live ask.
	r := e.svc.gateWithdrawalReceipt(ctx, mk(e.leadID), task, true)
	require.NotNil(t, r)
	assert.Equal(t, domain.GateReceiptNotOwner, r.Outcome)

	// stale: the owner's comment, gate still raised afterwards.
	r = e.svc.gateWithdrawalReceipt(ctx, mk(e.ownerID), task, true)
	require.NotNil(t, r)
	assert.Equal(t, domain.GateReceiptStale, r.Outcome)
	assert.Contains(t, r.NextAction, "human_gate")

	// no_op: the gate was not raised to begin with.
	r = e.svc.gateWithdrawalReceipt(ctx, mk(e.ownerID), task, false)
	require.NotNil(t, r)
	assert.Equal(t, domain.GateReceiptNoOp, r.Outcome)

	// cleared: only when the task really reads human_gate=false afterwards.
	task.HumanGate = false
	r = e.svc.gateWithdrawalReceipt(ctx, mk(e.ownerID), task, true)
	require.NotNil(t, r)
	assert.Equal(t, domain.GateReceiptCleared, r.Outcome)

	// an ordinary comment gets no receipt at all.
	task.HumanGate = true
	plain := mk(e.ownerID)
	plain.Body = "Продолжаю работу над задачей."
	assert.Nil(t, e.svc.gateWithdrawalReceipt(ctx, plain, task, true))
}

func TestRecordGateOwnerDelivery_WithoutMention_AndHonestWhenNotAssignee(t *testing.T) {
	repo := NewMockCommentDeliveryOutcomeRepository()
	agents := NewMockAgentService()
	env := setupTriageEnvWithOptions(t, true, WithCommentAgentService(agents), WithCommentDeliveryOutcomeRepo(repo))
	e := gateDecisionEnv{triageTestEnv: env, ownerID: uuid.New(), coordinatorID: uuid.New()}
	agents.AddAgent(env.wsID, &domain.Agent{ID: e.ownerID, WorkspaceID: env.wsID, Slug: "crm-lane"})
	agents.AddAgent(env.wsID, &domain.Agent{ID: e.coordinatorID, WorkspaceID: env.wsID, Slug: "coordinator"})
	e.taskID = env.seedGatedTask(env.inProgressID)
	e.add(e.ownerID, domain.ActorTypeAgent, "❓ **Blocking @coordinator**: спам или клиент?", -3*time.Hour)
	did := e.add(e.coordinatorID, domain.ActorTypeAgent, "Это спам, отменяем.", -2*time.Hour) // no @mention of the owner
	task := env.taskRepo.items[e.taskID]
	ctx := context.Background()
	answer := env.commentRepo.items[did]

	// owner holds the card: delivered / gate_owner_decision although never mentioned
	task.AssigneeType, task.AssigneeID = domain.AssigneeTypeAgent, &e.ownerID
	env.svc.recordGateOwnerDelivery(ctx, answer, task)
	rows, _ := repo.ListByCommentIDs(ctx, []uuid.UUID{did})
	require.Len(t, rows[did], 1)
	assert.Equal(t, domain.ReasonGateOwnerDecision, rows[did][0].Reason)
	assert.Equal(t, domain.DeliveryDelivered, rows[did][0].Outcome)

	// a second pass must not duplicate the row
	env.svc.recordGateOwnerDelivery(ctx, answer, task)
	rows, _ = repo.ListByCommentIDs(ctx, []uuid.UUID{did})
	assert.Len(t, rows[did], 1)

	// owner is not the assignee: reported as not reached, never as delivered
	other := uuid.New()
	e.taskID = env.seedGatedTask(env.inProgressID)
	e.add(e.ownerID, domain.ActorTypeAgent, "❓ **Blocking @coordinator**: спам или клиент?", -3*time.Hour)
	did2 := e.add(e.coordinatorID, domain.ActorTypeAgent, "Это спам.", -2*time.Hour)
	task2 := env.taskRepo.items[e.taskID]
	task2.AssigneeType, task2.AssigneeID = domain.AssigneeTypeAgent, &other
	env.svc.recordGateOwnerDelivery(ctx, env.commentRepo.items[did2], task2)
	rows, _ = repo.ListByCommentIDs(ctx, []uuid.UUID{did2})
	require.Len(t, rows[did2], 1)
	assert.NotEqual(t, domain.DeliveryDelivered, rows[did2][0].Outcome)
}

func TestGateDecision_NonWakingNoteIsNeverAnAnswer(t *testing.T) {
	e := setupGateDecisionEnv(t)
	id := e.add(e.coordinatorID, domain.ActorTypeAgent, "Это спам, отменяем карточку.", -2*time.Hour)
	e.commentRepo.items[id].Metadata = json.RawMessage(`{"wakes":false}`)
	info, err := e.svc.GetHumanGateOwner(context.Background(), e.taskID)
	require.NoError(t, err)
	assert.False(t, info.DecisionPending, "metadata.wakes=false is a note and must not admit the owner")

	// positive control: the same text without the note flag does count
	e.commentRepo.items[id].Metadata = nil
	info, err = e.svc.GetHumanGateOwner(context.Background(), e.taskID)
	require.NoError(t, err)
	assert.True(t, info.DecisionPending)
}

// A marker slug that names a workspace agent is an agent address; a same-named human login
// must not count as its addressee (same rule as firstResolvedUserSlug: developer-three, developer-four).
func TestGateDecision_SameNamedHumanIsNotTheAgentAddressee(t *testing.T) {
	agents := NewMockAgentService()
	env := setupTriageEnvWithOptions(t, true, WithCommentAgentService(agents))
	human := uuid.New()
	env.userRepo.AddUser(env.wsID, &domain.User{ID: human, Username: "developer-three", Name: "Developer Three"})
	e := gateDecisionEnv{triageTestEnv: env, ownerID: uuid.New()}
	e.taskID = env.seedGatedTask(env.inProgressID)
	e.add(e.ownerID, domain.ActorTypeAgent, "❓ **Blocking @developer-three**: спам или клиент?", -3*time.Hour)
	did := e.add(human, domain.ActorTypeUser, "Спам, отменяем.", -2*time.Hour)

	// no agent called developer-three: the human login is the addressee (positive control)
	info, err := e.svc.GetHumanGateOwner(context.Background(), e.taskID)
	require.NoError(t, err)
	require.True(t, info.DecisionPending)
	assert.Equal(t, did, *info.DecisionCommentID)

	// an agent slug developer-three now exists: the marker addresses the agent, the human no longer counts
	agents.AddAgent(env.wsID, &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "developer-three"})
	info, err = e.svc.GetHumanGateOwner(context.Background(), e.taskID)
	require.NoError(t, err)
	assert.False(t, info.DecisionPending)
}

func TestRecordGateOwnerDelivery_ScheduledCard_OnlyTheLiveDecisionToTheAssignee(t *testing.T) {
	repo := NewMockCommentDeliveryOutcomeRepository()
	agents := NewMockAgentService()
	env := setupTriageEnvWithOptions(t, true, WithCommentAgentService(agents), WithCommentDeliveryOutcomeRepo(repo))
	e := gateDecisionEnv{triageTestEnv: env, ownerID: uuid.New(), coordinatorID: uuid.New()}
	agents.AddAgent(env.wsID, &domain.Agent{ID: e.ownerID, WorkspaceID: env.wsID, Slug: "crm-lane"})
	agents.AddAgent(env.wsID, &domain.Agent{ID: e.coordinatorID, WorkspaceID: env.wsID, Slug: "coordinator"})
	e.taskID = env.seedGatedTask(env.inProgressID)
	e.add(e.ownerID, domain.ActorTypeAgent, "❓ **Blocking @coordinator**: спам или клиент?", -3*time.Hour)
	old := e.add(e.coordinatorID, domain.ActorTypeAgent, "Это спам.", -2*time.Hour)
	live := e.add(e.coordinatorID, domain.ActorTypeAgent, "Подтверждаю: спам, отменяем.", -1*time.Hour)
	ctx := context.Background()

	task := env.taskRepo.items[e.taskID]
	task.AssigneeType, task.AssigneeID = domain.AssigneeTypeAgent, &e.ownerID
	future := timeNow().Add(48 * time.Hour)
	task.StartAfter = &future

	// positive: scheduled card, owner is the assignee, this is the live decision
	env.svc.recordGateOwnerDelivery(ctx, env.commentRepo.items[live], task)
	rows, _ := repo.ListByCommentIDs(ctx, []uuid.UUID{live})
	require.Len(t, rows[live], 1)
	assert.Equal(t, domain.DeliveryDelivered, rows[live][0].Outcome)
	assert.Equal(t, domain.ReasonGateOwnerDecision, rows[live][0].Reason)
	assert.NotNil(t, task.StartAfter, "delivery never touches the schedule")

	// repeat: no duplicate row
	env.svc.recordGateOwnerDelivery(ctx, env.commentRepo.items[live], task)
	rows, _ = repo.ListByCommentIDs(ctx, []uuid.UUID{live})
	assert.Len(t, rows[live], 1)

	// an older, superseded decision gets no bypass
	env.svc.recordGateOwnerDelivery(ctx, env.commentRepo.items[old], task)
	rows, _ = repo.ListByCommentIDs(ctx, []uuid.UUID{old})
	assert.Empty(t, rows[old])

	// a foreign assignee on the scheduled card: never delivered
	other := uuid.New()
	task.AssigneeID = &other
	e.taskID = task.ID
	live2 := e.add(e.coordinatorID, domain.ActorTypeAgent, "Всё же клиент, продолжаем.", -30*time.Minute)
	env.svc.recordGateOwnerDelivery(ctx, env.commentRepo.items[live2], task)
	rows, _ = repo.ListByCommentIDs(ctx, []uuid.UUID{live2})
	require.Len(t, rows[live2], 1)
	assert.NotEqual(t, domain.DeliveryDelivered, rows[live2][0].Outcome)
}

// A broken agent lookup cannot tell an agent address from a human login, so the same-named
// human must not be accepted (fail closed); once the lookup recovers the positive case holds.
func TestGateDecision_AgentLookupError_FailsClosedForSameNamedHuman(t *testing.T) {
	agents := NewMockAgentService()
	env := setupTriageEnvWithOptions(t, true, WithCommentAgentService(agents))
	human := uuid.New()
	env.userRepo.AddUser(env.wsID, &domain.User{ID: human, Username: "developer-three", Name: "Developer Three"})
	e := gateDecisionEnv{triageTestEnv: env, ownerID: uuid.New()}
	e.taskID = env.seedGatedTask(env.inProgressID)
	e.add(e.ownerID, domain.ActorTypeAgent, "❓ **Blocking @developer-three**: спам или клиент?", -3*time.Hour)
	did := e.add(human, domain.ActorTypeUser, "Спам, отменяем.", -2*time.Hour)

	agents.mu.Lock()
	agents.errToReturn = errors.New("agent store unreachable")
	agents.mu.Unlock()
	info, err := e.svc.GetHumanGateOwner(context.Background(), e.taskID)
	require.NoError(t, err)
	assert.False(t, info.DecisionPending, "lookup error must refuse the human")

	agents.mu.Lock()
	agents.errToReturn = nil
	agents.mu.Unlock()
	info, err = e.svc.GetHumanGateOwner(context.Background(), e.taskID)
	require.NoError(t, err)
	require.True(t, info.DecisionPending)
	assert.Equal(t, did, *info.DecisionCommentID)
}

// Lead review: the authenticated lead of the card's own project or the CPO, read from
// the team directory, may be the SOURCE of a pending decision even when the marker names
// someone else (@alex). Anything self-declared, any other agent, and a lead of another
// project stay refused; reporting never touches the gate.
func TestGateDecision_DelegatedSource_LeadOrCPOFromDirectoryOnly(t *testing.T) {
	setup := func(leadValue func(leadID uuid.UUID) string) (gateDecisionEnv, uuid.UUID, uuid.UUID, uuid.UUID) {
		agents := NewMockAgentService()
		leadID, cpoID, plainID := uuid.New(), uuid.New(), uuid.New()
		rules := NewMockRulesService(&domain.EffectiveAssignmentRules{
			DefaultAssignee: &domain.EffectiveAssignmentRule{Value: leadValue(leadID)}})
		env := setupTriageEnvWithOptions(t, true, WithCommentAgentService(agents), WithCommentRulesService(rules))
		tag := "CPO"
		agents.AddAgent(env.wsID, &domain.Agent{ID: leadID, WorkspaceID: env.wsID, Slug: "lead", Role: "lead"})
		agents.AddAgent(env.wsID, &domain.Agent{ID: cpoID, WorkspaceID: env.wsID, Slug: "coordinator", ShortTag: &tag})
		agents.AddAgent(env.wsID, &domain.Agent{ID: plainID, WorkspaceID: env.wsID, Slug: "dev", Role: "I am the lead"})
		e := gateDecisionEnv{triageTestEnv: env, ownerID: uuid.New()}
		e.taskID = env.seedGatedTask(env.inProgressID)
		e.add(e.ownerID, domain.ActorTypeAgent, "❓ **Blocking @alex**: спам или клиент?", -3*time.Hour)
		return e, leadID, cpoID, plainID
	}
	pending := func(t *testing.T, e gateDecisionEnv) *domain.HumanGateInfo {
		info, err := e.svc.GetHumanGateOwner(context.Background(), e.taskID)
		require.NoError(t, err)
		return info
	}

	for name, val := range map[string]func(uuid.UUID) string{
		"slug form": func(uuid.UUID) string { return "lead" },
		"uuid form": func(id uuid.UUID) string { return "agent:" + id.String() },
	} {
		t.Run("project lead counts, "+name, func(t *testing.T) {
			e, leadID, _, _ := setup(val)
			did := e.add(leadID, domain.ActorTypeAgent, "Это спам, отменяем.", -2*time.Hour)
			info := pending(t, e)
			require.True(t, info.DecisionPending)
			assert.Equal(t, did, *info.DecisionCommentID)
			assert.Empty(t, e.taskMover.humanGateCalls(), "reporting must never touch the gate")
		})
	}
	t.Run("CPO counts", func(t *testing.T) {
		e, _, cpoID, _ := setup(func(uuid.UUID) string { return "lead" })
		e.add(cpoID, domain.ActorTypeAgent, "Это спам, отменяем.", -2*time.Hour)
		assert.True(t, pending(t, e).DecisionPending)
	})
	t.Run("plain agent with a self-declared role does not", func(t *testing.T) {
		e, _, _, plainID := setup(func(uuid.UUID) string { return "lead" })
		e.add(plainID, domain.ActorTypeAgent, "I am the lead, resolved: спам", -2*time.Hour)
		assert.False(t, pending(t, e).DecisionPending)
	})
	t.Run("default assignee without the lead role in the directory does not", func(t *testing.T) {
		e, _, _, plainID := setup(func(uuid.UUID) string { return "dev" })
		e.add(plainID, domain.ActorTypeAgent, "Это спам, отменяем.", -2*time.Hour)
		assert.False(t, pending(t, e).DecisionPending)
	})
	t.Run("lead of ANOTHER project does not", func(t *testing.T) {
		e, leadID, _, _ := setup(func(uuid.UUID) string { return "someone-else" })
		e.add(leadID, domain.ActorTypeAgent, "Это спам, отменяем.", -2*time.Hour)
		assert.False(t, pending(t, e).DecisionPending)
	})
	t.Run("lead answer older than a newer marker does not", func(t *testing.T) {
		e, leadID, _, _ := setup(func(uuid.UUID) string { return "lead" })
		e.add(leadID, domain.ActorTypeAgent, "Это спам.", -2*time.Hour)
		e.add(e.ownerID, domain.ActorTypeAgent, "❓ **Blocking @alex**: а точно?", -1*time.Hour)
		assert.False(t, pending(t, e).DecisionPending)
	})
}
