package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// Exercise Create rather than just the classifier: ordinary comments must
// actually reach NotificationService, while housekeeping stays on the card.
func TestCommentNotifications_HousekeepingRequiresOriginAndPrefix(t *testing.T) {
	cases := []struct {
		name, body string
		author     domain.ActorType
		silent     bool
	}{
		{"agent fiddler", "[fiddler] reminder", domain.ActorTypeAgent, true},
		{"system auto", "  🤖 Auto: moved to triage", domain.ActorTypeSystem, true},
		{"agent auto", "🤖 auto: checkpoint", domain.ActorTypeAgent, true},
		{"human fiddler prefix", "[fiddler] needs a fix", domain.ActorTypeUser, false},
		{"human auto prefix", "🤖 auto: example I am discussing", domain.ActorTypeUser, false},
		{"agent triage discussion", "I recommend moving to triage until reviewed", domain.ActorTypeAgent, false},
		{"human triage discussion", "I recommend moving to triage until reviewed", domain.ActorTypeUser, false},
		{"quoted marker", "The log says [fiddler] reminder", domain.ActorTypeAgent, false},
		{"blockquote marker", "> [fiddler] reminder", domain.ActorTypeAgent, false},
		{"code marker", "`[fiddler] reminder`", domain.ActorTypeAgent, false},
		{"triage prefix is prose", "moving to triage would help", domain.ActorTypeAgent, false},
		{"missing origin", "[fiddler] reminder", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := setupCommentServiceWithUserMentions()
			env.svc.deliveryRepo = NewMockCommentDeliveryOutcomeRepository()
			task := &domain.Task{ID: uuid.New(), ProjectID: env.projID, CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser}
			env.taskRepo.items[task.ID] = task
			comment := &domain.Comment{TaskID: task.ID, AuthorID: uuid.New(), AuthorType: tc.author, Body: tc.body}
			require.NoError(t, env.svc.Create(context.Background(), comment))
			require.NotNil(t, env.commentRepo.items[comment.ID])
			if tc.silent {
				require.Empty(t, env.notifySvc.Calls())
			} else {
				require.Len(t, env.notifySvc.Calls(), 1)
				require.Equal(t, "comment.created", env.notifySvc.Calls()[0].EventType)
				require.Equal(t, []uuid.UUID{task.CreatedBy}, env.notifySvc.Calls()[0].RelevantUserIDs)
			}
		})
	}
}

// The same handle can name both an agent and a human. Housekeeping must keep
// the agent wake-up while suppressing the human path, on both create and edit.
func TestCommentNotifications_HousekeepingMentions(t *testing.T) {
	for _, edit := range []bool{false, true} {
		for _, author := range []domain.ActorType{domain.ActorTypeAgent, domain.ActorTypeSystem, domain.ActorTypeUser} {
			name := string(author) + "/create"
			if edit {
				name = string(author) + "/edit"
			}
			t.Run(name, func(t *testing.T) {
				env := setupCommentServiceWithUserMentions()
				env.svc.deliveryRepo = NewMockCommentDeliveryOutcomeRepository()
				user := &domain.User{ID: uuid.New(), Username: "alex"}
				env.userRepo.AddUser(env.wsID, user)
				agent := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "alex"}
				env.agentSvc.AddAgent(env.wsID, agent)
				task := &domain.Task{ID: uuid.New(), ProjectID: env.projID}
				env.taskRepo.items[task.ID] = task
				comment := &domain.Comment{ID: uuid.New(), TaskID: task.ID, AuthorID: uuid.New(), AuthorType: author, Body: "[fiddler] reminder for @alex"}
				ctx := actorctx.WithActor(context.Background(), comment.AuthorID, author)
				if edit {
					old := *comment
					old.Body = "prior comment"
					env.commentRepo.items[old.ID] = &old
					require.NoError(t, env.svc.Update(ctx, comment))
				} else {
					require.NoError(t, env.svc.Create(ctx, comment))
				}
				require.Equal(t, comment.Body, env.commentRepo.items[comment.ID].Body)
				agents := filterByEvent(env.agentNotify.Calls(), "task.mentioned")
				require.Len(t, agents, 1)
				require.Equal(t, agent.ID, agents[0].AgentID)
				calls := env.notifySvc.Calls()
				mentions := 0
				for _, call := range calls {
					if call.EventType == "task.mentioned" {
						mentions++
						require.Equal(t, user.ID, *call.TargetUserID)
					}
				}
				if author == domain.ActorTypeUser {
					require.Equal(t, 1, mentions)
				} else {
					require.Empty(t, calls)
				}
			})
		}
	}
}

// Removing the housekeeping prefix makes an existing human handle deliverable
// for the first time. An agent sharing that handle already received Create.
func TestCommentNotifications_HousekeepingTransitionRestoresOnlyHuman(t *testing.T) {
	for _, author := range []domain.ActorType{domain.ActorTypeAgent, domain.ActorTypeSystem, domain.ActorTypeUser} {
		t.Run(string(author), func(t *testing.T) {
			env := setupCommentServiceWithUserMentions()
			env.svc.deliveryRepo = NewMockCommentDeliveryOutcomeRepository()
			user := &domain.User{ID: uuid.New(), Username: "alex"}
			env.userRepo.AddUser(env.wsID, user)
			agent := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "alex"}
			env.agentSvc.AddAgent(env.wsID, agent)
			task := &domain.Task{ID: uuid.New(), ProjectID: env.projID}
			env.taskRepo.items[task.ID] = task
			comment := &domain.Comment{TaskID: task.ID, AuthorID: uuid.New(), AuthorType: author, Body: "[fiddler] reminder for @alex"}
			ctx := actorctx.WithActor(context.Background(), comment.AuthorID, author)
			require.NoError(t, env.svc.Create(ctx, comment))
			require.Len(t, filterByEvent(env.agentNotify.Calls(), "task.mentioned"), 1)
			initialHumans := 0
			if author == domain.ActorTypeUser {
				initialHumans = 1
			}
			require.Len(t, filterNotificationEvents(env.notifySvc.Calls(), "task.mentioned"), initialHumans)
			// A human cannot edit an agent/system comment by spoofing its
			// author in the request. Update authorizes against the saved row.
			if author != domain.ActorTypeUser {
				humanCtx := actorctx.WithActor(context.Background(), comment.AuthorID, domain.ActorTypeUser)
				require.Error(t, env.svc.Update(humanCtx, &domain.Comment{ID: comment.ID, AuthorType: domain.ActorTypeUser, Body: "@alex forged edit"}))
			}
			// Repeating housekeeping must not notify the human or re-send the agent.
			require.NoError(t, env.svc.Update(ctx, &domain.Comment{ID: comment.ID, AuthorType: domain.ActorTypeUser, Body: "[fiddler] another reminder for @alex"}))
			require.Len(t, filterNotificationEvents(env.notifySvc.Calls(), "task.mentioned"), initialHumans)
			// Only body is accepted from Update; a forged AuthorType must not classify
			// a human's saved comment as automation (or an agent's as human).
			inputAuthor := domain.ActorTypeAgent
			if author != domain.ActorTypeUser {
				inputAuthor = domain.ActorTypeUser
			}
			edit := &domain.Comment{ID: comment.ID, AuthorType: inputAuthor, Body: "@alex please review findings"}
			require.NoError(t, env.svc.Update(ctx, edit))
			require.Equal(t, author, edit.AuthorType)
			mentions := filterNotificationEvents(env.notifySvc.Calls(), "task.mentioned")
			require.Len(t, mentions, 1)
			require.Equal(t, user.ID, *mentions[0].TargetUserID)
			require.Len(t, filterByEvent(env.agentNotify.Calls(), "task.mentioned"), 1)
			// Subsequent ordinary edits deduplicate both recipients again.
			require.NoError(t, env.svc.Update(ctx, &domain.Comment{ID: comment.ID, Body: "@alex please review updated findings"}))
			require.Len(t, filterNotificationEvents(env.notifySvc.Calls(), "task.mentioned"), 1)
			require.Len(t, filterByEvent(env.agentNotify.Calls(), "task.mentioned"), 1)
		})
	}
}

func filterNotificationEvents(events []domain.NotificationEvent, event string) []domain.NotificationEvent {
	var out []domain.NotificationEvent
	for _, e := range events {
		if e.EventType == event {
			out = append(out, e)
		}
	}
	return out
}

func TestCommentNotifications_RestoreUsesPersistentHumanHistory(t *testing.T) {
	for _, startNormal := range []bool{false, true} {
		name := "suppressed first"
		if startNormal {
			name = "already attempted"
		}
		t.Run(name, func(t *testing.T) {
			env := setupCommentServiceWithUserMentions()
			history := NewMockCommentDeliveryOutcomeRepository()
			env.svc.deliveryRepo = history
			user := &domain.User{ID: uuid.New(), Username: "alex"}
			env.userRepo.AddUser(env.wsID, user)
			agent := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "alex"}
			env.agentSvc.AddAgent(env.wsID, agent)
			task := &domain.Task{ID: uuid.New(), ProjectID: env.projID}
			env.taskRepo.items[task.ID] = task
			body := "[fiddler] reminder for @alex"
			if startNormal {
				body = "@alex review findings"
			}
			comment := &domain.Comment{TaskID: task.ID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeAgent, Body: body}
			ctx := actorctx.WithActor(context.Background(), comment.AuthorID, comment.AuthorType)
			require.NoError(t, env.svc.Create(ctx, comment))
			require.NoError(t, env.svc.Update(ctx, &domain.Comment{ID: comment.ID, Body: "[fiddler] reminder for @alex"}))
			// A fresh service instance shares only persisted repositories, not a
			// remembered set of notified handles.
			replacement := *env.svc
			env.svc = &replacement
			require.NoError(t, env.svc.Update(ctx, &domain.Comment{ID: comment.ID, Body: "@alex please review findings"}))
			require.Len(t, filterNotificationEvents(env.notifySvc.Calls(), "task.mentioned"), 1)
			require.Len(t, filterByEvent(env.agentNotify.Calls(), "task.mentioned"), 1)
			// Removing and re-adding a normal mention retains the existing behavior.
			require.NoError(t, env.svc.Update(ctx, &domain.Comment{ID: comment.ID, Body: "ordinary text without a handle"}))
			require.NoError(t, env.svc.Update(ctx, &domain.Comment{ID: comment.ID, Body: "@alex review again"}))
			require.Len(t, filterNotificationEvents(env.notifySvc.Calls(), "task.mentioned"), 2)
			require.Len(t, filterByEvent(env.agentNotify.Calls(), "task.mentioned"), 2)
		})
	}
}

type unreadableCommentHistory struct {
	repository.CommentDeliveryOutcomeRepository
}

func (r unreadableCommentHistory) ListByCommentIDs(context.Context, []uuid.UUID) (map[uuid.UUID][]domain.CommentDeliveryOutcome, error) {
	return nil, errors.New("history unavailable")
}

func TestCommentNotifications_HistoryUnavailableOnlySkipsRestoredHuman(t *testing.T) {
	for _, mode := range []string{"error", "nil"} {
		t.Run(mode, func(t *testing.T) {
			env := setupCommentServiceWithUserMentions()
			env.svc.deliveryRepo = NewMockCommentDeliveryOutcomeRepository()
			for _, slug := range []string{"alex", "newuser"} {
				env.userRepo.AddUser(env.wsID, &domain.User{ID: uuid.New(), Username: slug})
			}
			agent := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "newagent"}
			env.agentSvc.AddAgent(env.wsID, agent)
			task := &domain.Task{ID: uuid.New(), ProjectID: env.projID}
			env.taskRepo.items[task.ID] = task
			comment := &domain.Comment{TaskID: task.ID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeAgent, Body: "[fiddler] reminder for @alex"}
			ctx := actorctx.WithActor(context.Background(), comment.AuthorID, comment.AuthorType)
			require.NoError(t, env.svc.Create(ctx, comment))
			if mode == "nil" {
				env.svc.deliveryRepo = nil
			} else {
				env.svc.deliveryRepo = unreadableCommentHistory{env.svc.deliveryRepo}
			}
			require.NoError(t, env.svc.Update(ctx, &domain.Comment{ID: comment.ID, Body: "@alex @newuser @newagent please review"}))
			calls := filterNotificationEvents(env.notifySvc.Calls(), "task.mentioned")
			require.Len(t, calls, 1)
			user, err := env.userRepo.GetByUsername(ctx, env.wsID, "newuser")
			require.NoError(t, err)
			require.Equal(t, user.ID, *calls[0].TargetUserID)
			require.Len(t, filterByEvent(env.agentNotify.Calls(), "task.mentioned"), 1)
		})
	}
}
