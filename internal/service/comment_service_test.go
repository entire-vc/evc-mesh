package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// mentionTestEnv holds all dependencies for @mention tests.
type mentionTestEnv struct {
	svc         *commentService
	commentRepo *MockCommentRepository
	taskRepo    *MockTaskRepository
	notifySvc   *MockAgentNotifyService
	agentSvc    *MockAgentService
	wsID        uuid.UUID
	projID      uuid.UUID
}

// setupCommentServiceWithMentions returns a commentService wired with mock notify + agent services.
func setupCommentServiceWithMentions() mentionTestEnv {
	commentRepo := NewMockCommentRepository()
	taskRepo := NewMockTaskRepository()
	activityRepo := NewMockActivityLogRepository()
	projectRepo := NewMockProjectRepository()
	notifySvc := NewMockAgentNotifyService()
	agentSvc := NewMockAgentService()

	wsID := uuid.New()
	projID := uuid.New()
	projectRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: wsID}

	timeNow = func() time.Time { return frozenTime }

	svc := NewCommentService(commentRepo, taskRepo, activityRepo,
		WithCommentAgentNotify(notifySvc),
		WithCommentAgentService(agentSvc),
		WithCommentProjectRepo(projectRepo),
	).(*commentService)

	return mentionTestEnv{svc, commentRepo, taskRepo, notifySvc, agentSvc, wsID, projID}
}

// setupCommentService returns a commentService wired to fresh mocks.
func setupCommentService() (*commentService, *MockCommentRepository, *MockTaskRepository) {
	commentRepo := NewMockCommentRepository()
	taskRepo := NewMockTaskRepository()
	activityRepo := NewMockActivityLogRepository()
	svc := NewCommentService(commentRepo, taskRepo, activityRepo).(*commentService)

	// Freeze the clock.
	timeNow = func() time.Time { return frozenTime }

	return svc, commentRepo, taskRepo
}

// ---------------------------------------------------------------------------
// TestCommentService_Create
// ---------------------------------------------------------------------------

func TestCommentService_Create(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(commentRepo *MockCommentRepository, taskRepo *MockTaskRepository) *domain.Comment
		wantErr   bool
		errCode   int
		errMsg    string
		checkFunc func(t *testing.T, comment *domain.Comment, repo *MockCommentRepository)
	}{
		{
			name: "success",
			setup: func(_ *MockCommentRepository, taskRepo *MockTaskRepository) *domain.Comment {
				taskID := uuid.New()
				taskRepo.items[taskID] = &domain.Task{ID: taskID, Title: "A task"}
				return &domain.Comment{
					TaskID:     taskID,
					AuthorID:   uuid.New(),
					AuthorType: domain.ActorTypeUser,
					Body:       "This is a comment",
				}
			},
			wantErr: false,
			checkFunc: func(t *testing.T, comment *domain.Comment, repo *MockCommentRepository) {
				assert.NotEqual(t, uuid.Nil, comment.ID)
				assert.Equal(t, frozenTime, comment.CreatedAt)
				assert.Equal(t, frozenTime, comment.UpdatedAt)
				stored := repo.items[comment.ID]
				require.NotNil(t, stored)
				assert.Equal(t, "This is a comment", stored.Body)
			},
		},
		{
			name: "success - with valid parent comment",
			setup: func(commentRepo *MockCommentRepository, taskRepo *MockTaskRepository) *domain.Comment {
				taskID := uuid.New()
				taskRepo.items[taskID] = &domain.Task{ID: taskID, Title: "A task"}

				parentID := uuid.New()
				commentRepo.items[parentID] = &domain.Comment{
					ID:     parentID,
					TaskID: taskID,
					Body:   "Parent comment",
				}

				return &domain.Comment{
					TaskID:          taskID,
					ParentCommentID: &parentID,
					AuthorID:        uuid.New(),
					AuthorType:      domain.ActorTypeAgent,
					Body:            "Reply to parent",
				}
			},
			wantErr: false,
			checkFunc: func(t *testing.T, comment *domain.Comment, _ *MockCommentRepository) {
				assert.NotNil(t, comment.ParentCommentID)
			},
		},
		{
			name: "error - empty body",
			setup: func(_ *MockCommentRepository, taskRepo *MockTaskRepository) *domain.Comment {
				taskID := uuid.New()
				taskRepo.items[taskID] = &domain.Task{ID: taskID, Title: "A task"}
				return &domain.Comment{
					TaskID:     taskID,
					AuthorID:   uuid.New(),
					AuthorType: domain.ActorTypeUser,
					Body:       "",
				}
			},
			wantErr: true,
			errCode: http.StatusBadRequest,
		},
		{
			name: "error - task not found",
			setup: func(_ *MockCommentRepository, _ *MockTaskRepository) *domain.Comment {
				return &domain.Comment{
					TaskID:     uuid.New(),
					AuthorID:   uuid.New(),
					AuthorType: domain.ActorTypeUser,
					Body:       "Orphan comment",
				}
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
		{
			name: "error - parent comment not found",
			setup: func(_ *MockCommentRepository, taskRepo *MockTaskRepository) *domain.Comment {
				taskID := uuid.New()
				taskRepo.items[taskID] = &domain.Task{ID: taskID, Title: "A task"}
				nonExistentParent := uuid.New()
				return &domain.Comment{
					TaskID:          taskID,
					ParentCommentID: &nonExistentParent,
					AuthorID:        uuid.New(),
					AuthorType:      domain.ActorTypeUser,
					Body:            "Reply to nothing",
				}
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
		{
			name: "error - parent comment belongs to different task",
			setup: func(commentRepo *MockCommentRepository, taskRepo *MockTaskRepository) *domain.Comment {
				taskID := uuid.New()
				otherTaskID := uuid.New()
				taskRepo.items[taskID] = &domain.Task{ID: taskID, Title: "Task A"}
				taskRepo.items[otherTaskID] = &domain.Task{ID: otherTaskID, Title: "Task B"}

				parentID := uuid.New()
				commentRepo.items[parentID] = &domain.Comment{
					ID:     parentID,
					TaskID: otherTaskID, // belongs to a different task
					Body:   "Parent on other task",
				}

				return &domain.Comment{
					TaskID:          taskID,
					ParentCommentID: &parentID,
					AuthorID:        uuid.New(),
					AuthorType:      domain.ActorTypeUser,
					Body:            "Cross-task reply",
				}
			},
			wantErr: true,
			errCode: http.StatusBadRequest,
			errMsg:  "does not belong to the same task",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, commentRepo, taskRepo := setupCommentService()
			ctx := context.Background()
			comment := tt.setup(commentRepo, taskRepo)

			err := svc.Create(ctx, comment)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
				if tt.errMsg != "" {
					assert.Contains(t, apiErr.Message, tt.errMsg)
				}
			} else {
				require.NoError(t, err)
				if tt.checkFunc != nil {
					tt.checkFunc(t, comment, commentRepo)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestCommentService_Update
// ---------------------------------------------------------------------------

func TestCommentService_Update(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(repo *MockCommentRepository) (context.Context, *domain.Comment)
		wantErr bool
		errCode int
	}{
		{
			name: "success - only body is updated",
			setup: func(repo *MockCommentRepository) (context.Context, *domain.Comment) {
				authorID := uuid.New()
				id := uuid.New()
				repo.items[id] = &domain.Comment{
					ID:         id,
					TaskID:     uuid.New(),
					AuthorID:   authorID,
					AuthorType: domain.ActorTypeUser,
					Body:       "Original body",
				}
				ctx := actorctx.WithActor(context.Background(), authorID, domain.ActorTypeUser)
				return ctx, &domain.Comment{ID: id, Body: "Updated body"}
			},
			wantErr: false,
		},
		{
			name: "error - comment not found",
			setup: func(_ *MockCommentRepository) (context.Context, *domain.Comment) {
				return context.Background(), &domain.Comment{ID: uuid.New(), Body: "Ghost"}
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
		{
			name: "error - forbidden when not the author",
			setup: func(repo *MockCommentRepository) (context.Context, *domain.Comment) {
				id := uuid.New()
				repo.items[id] = &domain.Comment{
					ID:         id,
					TaskID:     uuid.New(),
					AuthorID:   uuid.New(),
					AuthorType: domain.ActorTypeUser,
					Body:       "Original body",
				}
				// actor is a different user
				ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeUser)
				return ctx, &domain.Comment{ID: id, Body: "Tampered body"}
			},
			wantErr: true,
			errCode: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, commentRepo, _ := setupCommentService()
			ctx, comment := tt.setup(commentRepo)

			err := svc.Update(ctx, comment)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
			} else {
				require.NoError(t, err)
				stored := commentRepo.items[comment.ID]
				require.NotNil(t, stored)
				assert.Equal(t, "Updated body", stored.Body)
				assert.Equal(t, frozenTime, stored.UpdatedAt)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestCommentService_ListByTask
// ---------------------------------------------------------------------------

func TestCommentService_ListByTask(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(repo *MockCommentRepository) uuid.UUID
		wantLen int
	}{
		{
			name: "with matching comments",
			setup: func(repo *MockCommentRepository) uuid.UUID {
				taskID := uuid.New()
				for i := 0; i < 3; i++ {
					id := uuid.New()
					repo.items[id] = &domain.Comment{ID: id, TaskID: taskID, Body: "Comment"}
				}
				// Comment on another task.
				otherID := uuid.New()
				repo.items[otherID] = &domain.Comment{ID: otherID, TaskID: uuid.New(), Body: "Other"}
				return taskID
			},
			wantLen: 3,
		},
		{
			name: "empty result",
			setup: func(_ *MockCommentRepository) uuid.UUID {
				return uuid.New()
			},
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, commentRepo, _ := setupCommentService()
			ctx := context.Background()
			taskID := tt.setup(commentRepo)

			page, err := svc.ListByTask(ctx, taskID, repository.CommentFilter{}, pagination.Params{Page: 1, PageSize: 50})

			require.NoError(t, err)
			require.NotNil(t, page)
			assert.Len(t, page.Items, tt.wantLen)
		})
	}
}

// ---------------------------------------------------------------------------
// TestExtractMentionSlugs
// ---------------------------------------------------------------------------

func TestExtractMentionSlugs(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"simple mention", "@bill hello", []string{"bill"}},
		{"mention at start", "@bill", []string{"bill"}},
		{"mention after space", "hey @bill!", []string{"bill"}},
		{"mention in parens", "(@bill)", []string{"bill"}},
		{"mention in brackets", "[@bill]", []string{"bill"}},
		{"mention in braces", "{@bill}", []string{"bill"}},
		{"multiple unique", "@alice and @bob", []string{"alice", "bob"}},
		{"dedup same slug", "@alice @alice again", []string{"alice"}},
		{"email address excluded", "email bar@foo.com is not a mention", nil},
		{"hyphenated slug", "@bill-the-cat", []string{"bill-the-cat"}},
		{"single char (too short)", "@a", nil},
		{"uppercase normalized", "@BILL", nil}, // regex is lowercase only
		{"no mentions", "plain text here", nil},
		{"slug starting with hyphen rejected", "@-foo", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractMentionSlugs(tt.input)
			if len(tt.want) == 0 {
				assert.Empty(t, got)
			} else {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestNotifyMentions_*
// ---------------------------------------------------------------------------

func TestNotifyMentions_BasicMention(t *testing.T) {
	env := setupCommentServiceWithMentions()

	agent := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "bill", Name: "Bill"}
	env.agentSvc.AddAgent(env.wsID, agent)

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID, Title: "T"}

	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: "hey @bill check this"}
	task := env.taskRepo.items[taskID]

	env.svc.notifyMentions(context.Background(), comment, task, "", env.wsID)

	calls := env.notifySvc.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "task.mentioned", calls[0].EventType)
	assert.Equal(t, agent.ID, calls[0].AgentID)
	assert.Equal(t, map[string]any{"mentioned_slug": "bill"}, calls[0].Payload)
}

func TestNotifyMentions_SelfMentionSkipped(t *testing.T) {
	env := setupCommentServiceWithMentions()

	agentID := uuid.New()
	agent := &domain.Agent{ID: agentID, WorkspaceID: env.wsID, Slug: "self-agent"}
	env.agentSvc.AddAgent(env.wsID, agent)

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID}

	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: "@self-agent talking to myself"}
	ctx := actorctx.WithActor(context.Background(), agentID, domain.ActorTypeAgent)

	env.svc.notifyMentions(ctx, comment, env.taskRepo.items[taskID], "", env.wsID)

	assert.Empty(t, env.notifySvc.Calls())
}

func TestNotifyMentions_NewOnlyOnEdit(t *testing.T) {
	env := setupCommentServiceWithMentions()

	alice := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "alice"}
	bob := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "bob"}
	env.agentSvc.AddAgent(env.wsID, alice)
	env.agentSvc.AddAgent(env.wsID, bob)

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID}

	// oldBody had @alice, new body adds @bob — only bob should be notified.
	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: "@alice and @bob"}
	oldBody := "@alice something"
	task := env.taskRepo.items[taskID]

	env.svc.notifyMentions(context.Background(), comment, task, oldBody, env.wsID)

	calls := env.notifySvc.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, bob.ID, calls[0].AgentID)
}

func TestNotifyMentions_DedupSameAgent(t *testing.T) {
	env := setupCommentServiceWithMentions()

	agent := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "alice"}
	env.agentSvc.AddAgent(env.wsID, agent)

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID}

	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: "@alice first and @alice again"}
	task := env.taskRepo.items[taskID]

	env.svc.notifyMentions(context.Background(), comment, task, "", env.wsID)

	assert.Len(t, env.notifySvc.Calls(), 1)
}

func TestNotifyMentions_UnknownSlugSkipped(t *testing.T) {
	env := setupCommentServiceWithMentions()

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID}

	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: "@noone is here"}
	task := env.taskRepo.items[taskID]

	require.NoError(t, func() error {
		env.svc.notifyMentions(context.Background(), comment, task, "", env.wsID)
		return nil
	}())

	assert.Empty(t, env.notifySvc.Calls())
}

func TestNotifyMentions_NoMentionsNoOp(t *testing.T) {
	env := setupCommentServiceWithMentions()

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID}

	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: "just a plain comment"}
	task := env.taskRepo.items[taskID]

	env.svc.notifyMentions(context.Background(), comment, task, "", env.wsID)

	assert.Empty(t, env.notifySvc.Calls())
}

// TestCommentService_Create_FiresMention verifies that Create triggers mention notifications.
func TestCommentService_Create_FiresMention(t *testing.T) {
	env := setupCommentServiceWithMentions()

	agent := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "garfield"}
	env.agentSvc.AddAgent(env.wsID, agent)

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID, Title: "Test"}

	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeUser,
		Body:       "please review @garfield",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))

	mentionCalls := filterByEvent(env.notifySvc.Calls(), "task.mentioned")
	require.Len(t, mentionCalls, 1)
	assert.Equal(t, agent.ID, mentionCalls[0].AgentID)
}

// ---------------------------------------------------------------------------
// TestCommentService_Create_CommentNotify* — regression coverage for
// comment.created going to only the users a comment actually concerns
// (mentioned / assignee / reviewer), not a workspace-wide broadcast.
// ---------------------------------------------------------------------------

// setupCommentServiceForCommentNotify wires a commentService with the deps needed
// to exercise the comment.created targeted-dispatch path: user repo (for @mention
// resolution) and the in-app NotificationService (distinct from MockAgentNotifyService,
// which only covers the agent SSE/push path).
func setupCommentServiceForCommentNotify() (svc *commentService, taskRepo *MockTaskRepository, userRepo *MockUserRepository, notifySvc *MockNotificationService, wsID, projID uuid.UUID) {
	commentRepo := NewMockCommentRepository()
	taskRepo = NewMockTaskRepository()
	activityRepo := NewMockActivityLogRepository()
	projectRepo := NewMockProjectRepository()
	userRepo = NewMockUserRepository()
	notifySvc = NewMockNotificationService()

	wsID = uuid.New()
	projID = uuid.New()
	projectRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: wsID}

	timeNow = func() time.Time { return frozenTime }

	svc = NewCommentService(commentRepo, taskRepo, activityRepo,
		WithCommentUserRepo(userRepo),
		WithCommentProjectRepo(projectRepo),
		WithCommentNotificationService(notifySvc),
	).(*commentService)

	return svc, taskRepo, userRepo, notifySvc, wsID, projID
}

// TestCommentService_Create_CommentNotifiesOnlyMentionedAssigneeReviewer is the
// direct regression test for the bug: comment.created used to broadcast to every
// workspace subscriber (no TargetUserID at all). It must now reach only the
// @-mentioned user, the task assignee, and the task reviewer — and a workspace
// "witness" subscriber with no connection to the task must get nothing.
func TestCommentService_Create_CommentNotifiesOnlyMentionedAssigneeReviewer(t *testing.T) {
	svc, taskRepo, userRepo, notifySvc, wsID, projID := setupCommentServiceForCommentNotify()

	mentioned := &domain.User{ID: uuid.New(), Username: "mentioned-user"}
	assignee := &domain.User{ID: uuid.New(), Username: "assignee-user"}
	reviewer := &domain.User{ID: uuid.New(), Username: "reviewer-user"}
	author := &domain.User{ID: uuid.New(), Username: "author-user"}
	// witness: a subscriber with no relation to the task at all — must not be notified.
	witness := &domain.User{ID: uuid.New(), Username: "witness-user"}
	userRepo.AddUser(wsID, mentioned)
	userRepo.AddUser(wsID, assignee)
	userRepo.AddUser(wsID, reviewer)
	userRepo.AddUser(wsID, author)
	userRepo.AddUser(wsID, witness)

	reviewerType := domain.AssigneeTypeUser
	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{
		ID:           taskID,
		ProjectID:    projID,
		Title:        "Test task",
		AssigneeID:   &assignee.ID,
		AssigneeType: domain.AssigneeTypeUser,
		ReviewerID:   &reviewer.ID,
		ReviewerType: &reviewerType,
	}

	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   author.ID,
		AuthorType: domain.ActorTypeUser,
		Body:       "cc @mentioned-user please take a look",
	}
	require.NoError(t, svc.Create(context.Background(), comment))

	calls := filterNotifyByEvent(notifySvc.Calls(), "comment.created")
	require.Len(t, calls, 3)

	var targets []uuid.UUID
	for _, c := range calls {
		require.NotNil(t, c.TargetUserID, "comment.created must always be targeted, never a workspace broadcast")
		targets = append(targets, *c.TargetUserID)
	}
	assert.ElementsMatch(t, []uuid.UUID{mentioned.ID, assignee.ID, reviewer.ID}, targets)
	assert.NotContains(t, targets, witness.ID)
	assert.NotContains(t, targets, author.ID)
}

// TestCommentService_Create_CommentAuthorNotSelfNotified verifies the assignee
// commenting on their own task does not generate a comment.created addressed to
// themselves.
func TestCommentService_Create_CommentAuthorNotSelfNotified(t *testing.T) {
	svc, taskRepo, userRepo, notifySvc, wsID, projID := setupCommentServiceForCommentNotify()

	assignee := &domain.User{ID: uuid.New(), Username: "self-assignee"}
	userRepo.AddUser(wsID, assignee)

	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{
		ID:           taskID,
		ProjectID:    projID,
		Title:        "Own task",
		AssigneeID:   &assignee.ID,
		AssigneeType: domain.AssigneeTypeUser,
	}

	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   assignee.ID,
		AuthorType: domain.ActorTypeUser,
		Body:       "progress update, no mentions here",
	}
	require.NoError(t, svc.Create(context.Background(), comment))

	assert.Empty(t, filterNotifyByEvent(notifySvc.Calls(), "comment.created"))
}

func filterNotifyByEvent(calls []domain.NotificationEvent, eventType string) []domain.NotificationEvent {
	var out []domain.NotificationEvent
	for _, c := range calls {
		if c.EventType == eventType {
			out = append(out, c)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// TestCommentService_Create_NoNotifyOnTerminalTask (incident #56a6d5b2)
// ---------------------------------------------------------------------------

// TestCommentService_Create_NoNotifyOnTerminalTask verifies that task.commented
// is NOT sent to the assigned agent when the task is in a terminal status
// (done or cancelled). Without this guard the dispatcher spawns a new session
// whose prompt instructs checkout + move_to_in_progress, reactivating closed work.
func TestCommentService_Create_NoNotifyOnTerminalTask(t *testing.T) {
	cases := []struct {
		name     string
		category domain.StatusCategory
		wantSent bool
	}{
		{"done task suppresses notify", domain.StatusCategoryDone, false},
		{"cancelled task suppresses notify", domain.StatusCategoryCancelled, false},
		{"in_progress task sends notify", domain.StatusCategoryInProgress, true},
		{"todo task sends notify", domain.StatusCategoryTodo, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			commentRepo := NewMockCommentRepository()
			taskRepo := NewMockTaskRepository()
			activityRepo := NewMockActivityLogRepository()
			statusRepo := NewMockTaskStatusRepository()
			projectRepo := NewMockProjectRepository()
			notifySvc := NewMockAgentNotifyService()

			wsID := uuid.New()
			projID := uuid.New()
			projectRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: wsID}

			statusID := uuid.New()
			statusRepo.items[statusID] = &domain.TaskStatus{
				ID: statusID, ProjectID: projID, Category: tc.category, Name: tc.name,
			}

			agentID := uuid.New()
			taskID := uuid.New()
			taskRepo.items[taskID] = &domain.Task{
				ID:           taskID,
				ProjectID:    projID,
				Title:        "Done task",
				StatusID:     statusID,
				AssigneeID:   &agentID,
				AssigneeType: domain.AssigneeTypeAgent,
			}

			timeNow = func() time.Time { return frozenTime }

			svc := NewCommentService(commentRepo, taskRepo, activityRepo,
				WithCommentAgentNotify(notifySvc),
				WithCommentStatusRepo(statusRepo),
				WithCommentProjectRepo(projectRepo),
			).(*commentService)

			comment := &domain.Comment{
				TaskID:     taskID,
				AuthorID:   uuid.New(),
				AuthorType: domain.ActorTypeUser,
				Body:       "nice work team",
			}
			require.NoError(t, svc.Create(context.Background(), comment))

			commentedCalls := filterByEvent(notifySvc.Calls(), "task.commented")
			if tc.wantSent {
				assert.Len(t, commentedCalls, 1, "task.commented should be sent for non-terminal task")
			} else {
				assert.Empty(t, commentedCalls, "task.commented must not be sent for terminal task (reactivation guard)")
			}
		})
	}
}

// filterByEvent returns only the calls matching the given event_type.
func filterByEvent(calls []AgentNotification, eventType string) []AgentNotification {
	var out []AgentNotification
	for _, c := range calls {
		if c.EventType == eventType {
			out = append(out, c)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// TestCommentService_CreateEnrichesAuthorName
// ---------------------------------------------------------------------------

func TestCommentService_CreateEnrichesAuthorName(t *testing.T) {
	svc, commentRepo, taskRepo := setupCommentService()

	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{ID: taskID, Title: "A task"}

	authorName := "Admin"
	commentRepo.enrichedAuthorName = &authorName

	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeUser,
		Body:       "Hello world",
	}

	err := svc.Create(context.Background(), comment)

	require.NoError(t, err)
	require.NotNil(t, comment.AuthorName, "author_name must be populated after Create")
	assert.Equal(t, "Admin", *comment.AuthorName)
}

// ---------------------------------------------------------------------------
// TestCommentService_UpdateEnrichesAuthorName
// ---------------------------------------------------------------------------

func TestCommentService_UpdateEnrichesAuthorName(t *testing.T) {
	svc, commentRepo, _ := setupCommentService()

	authorID := uuid.New()
	id := uuid.New()
	commentRepo.items[id] = &domain.Comment{
		ID:         id,
		TaskID:     uuid.New(),
		AuthorID:   authorID,
		AuthorType: domain.ActorTypeUser,
		Body:       "Original",
	}

	authorName := "Admin"
	commentRepo.enrichedAuthorName = &authorName

	ctx := actorctx.WithActor(context.Background(), authorID, domain.ActorTypeUser)
	comment := &domain.Comment{ID: id, Body: "Updated"}

	err := svc.Update(ctx, comment)

	require.NoError(t, err)
	require.NotNil(t, comment.AuthorName, "author_name must be populated after Update")
	assert.Equal(t, "Admin", *comment.AuthorName)
	assert.Equal(t, "Updated", comment.Body)
}

// ---------------------------------------------------------------------------
// TestCommentService_ListByAuthor
// ---------------------------------------------------------------------------

func TestCommentService_ListByAuthor(t *testing.T) {
	t.Run("happy path returns empty page with nil cursor", func(t *testing.T) {
		svc, commentRepo, _ := setupCommentService()
		authorID := uuid.New()

		page, err := svc.ListByAuthor(context.Background(), authorID, repository.CommentViewFilter{Limit: 50})

		require.NoError(t, err)
		require.NotNil(t, page)
		assert.Empty(t, page.Items)
		assert.Nil(t, page.NextCursor)
		_ = commentRepo // repo used via svc
	})

	t.Run("with before cursor filter — no error", func(t *testing.T) {
		svc, _, _ := setupCommentService()
		authorID := uuid.New()
		before := frozenTime

		page, err := svc.ListByAuthor(context.Background(), authorID, repository.CommentViewFilter{
			Limit:  10,
			Before: &before,
		})

		require.NoError(t, err)
		require.NotNil(t, page)
	})

	t.Run("repo error propagates", func(t *testing.T) {
		svc, commentRepo, _ := setupCommentService()
		commentRepo.errToReturn = fmt.Errorf("db unavailable")

		_, err := svc.ListByAuthor(context.Background(), uuid.New(), repository.CommentViewFilter{Limit: 50})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "db unavailable")
	})
}

// ---------------------------------------------------------------------------
// TestCommentService_ListRecentByWorkspace
// ---------------------------------------------------------------------------

func TestCommentService_ListRecentByWorkspace(t *testing.T) {
	t.Run("happy path returns empty page with nil cursor", func(t *testing.T) {
		svc, _, _ := setupCommentService()
		wsID := uuid.New()

		page, err := svc.ListRecentByWorkspace(context.Background(), wsID, repository.CommentViewFilter{Limit: 50})

		require.NoError(t, err)
		require.NotNil(t, page)
		assert.Empty(t, page.Items)
		assert.Nil(t, page.NextCursor)
	})

	t.Run("with before cursor filter — no error", func(t *testing.T) {
		svc, _, _ := setupCommentService()
		before := frozenTime

		page, err := svc.ListRecentByWorkspace(context.Background(), uuid.New(), repository.CommentViewFilter{
			Limit:  25,
			Before: &before,
		})

		require.NoError(t, err)
		require.NotNil(t, page)
	})

	t.Run("repo error propagates", func(t *testing.T) {
		svc, commentRepo, _ := setupCommentService()
		commentRepo.errToReturn = fmt.Errorf("connection timeout")

		_, err := svc.ListRecentByWorkspace(context.Background(), uuid.New(), repository.CommentViewFilter{Limit: 50})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "connection timeout")
	})
}

// --- comment.metadata validation (task #13e391d2) ---
//
// The acceptance criterion these encode: metadata is either stored as sent, or refused
// with a reason. The one outcome that is not allowed is 200-and-discarded, which is what
// shipped for months and left three consumers filtering on a field nobody could set.

func TestValidateCommentMetadata(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
		errWant string
	}{
		{name: "absent is legal", raw: "", wantErr: false},
		{name: "explicit null is legal", raw: `null`, wantErr: false},
		{name: "object accepted", raw: `{"source":"pr-task-driver","auto":true}`, wantErr: false},
		{name: "empty object accepted", raw: `{}`, wantErr: false},
		{name: "nested object accepted", raw: `{"source":"x","ctx":{"pr":435}}`, wantErr: false},

		// Refused shapes — each must name what was actually sent.
		{name: "array refused", raw: `["source","x"]`, wantErr: true, errWant: "got array"},
		{name: "string refused", raw: `"pr-task-driver"`, wantErr: true, errWant: "got string"},
		{name: "number refused", raw: `42`, wantErr: true, errWant: "got number"},
		{name: "bool refused", raw: `true`, wantErr: true, errWant: "got boolean"},
		{name: "malformed refused", raw: `{"source":`, wantErr: true, errWant: "valid JSON object"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCommentMetadata(json.RawMessage(tt.raw))
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err, "shape must be refused, not silently dropped")
			// The reason must be legible to the CALLER, which reads the structured
			// validation map off the JSON body — Error() only carries "[400] Validation
			// failed", so asserting on it would pass for any rejection whatsoever and
			// would not prove the caller is told what it sent wrong.
			apiErr, ok := err.(*apierror.Error)
			require.True(t, ok, "must be an apierror so the 4xx reaches the caller as 400")
			assert.Equal(t, http.StatusBadRequest, apiErr.Code)
			assert.Contains(t, apiErr.Validation["metadata"], tt.errWant)
		})
	}
}

func TestValidateCommentMetadata_SizeCap(t *testing.T) {
	// A valid object that is simply too large is refused by size, not truncated —
	// truncation would be the same silent-corruption failure in a new costume.
	big := `{"pad":"` + strings.Repeat("x", maxCommentMetadataBytes) + `"}`
	err := validateCommentMetadata(json.RawMessage(big))
	require.Error(t, err)
	apiErr, ok := err.(*apierror.Error)
	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, apiErr.Code)
	assert.Contains(t, apiErr.Validation["metadata"], "at most")

	justUnder := `{"pad":"` + strings.Repeat("x", 100) + `"}`
	require.NoError(t, validateCommentMetadata(json.RawMessage(justUnder)))
}

func TestCommentService_Create_PersistsMetadata(t *testing.T) {
	svc, commentRepo, taskRepo := setupCommentService()

	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{ID: taskID, Title: "A task"}

	c := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Body:       "🤖 Auto: nudge",
		Metadata:   json.RawMessage(`{"source":"pr-task-driver","auto":true}`),
	}

	require.NoError(t, svc.Create(context.Background(), c))

	stored := commentRepo.items[c.ID]
	require.NotNil(t, stored)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(stored.Metadata, &decoded))
	assert.Equal(t, "pr-task-driver", decoded["source"])
	assert.Equal(t, true, decoded["auto"])
}

func TestCommentService_Create_RejectsNonObjectMetadata(t *testing.T) {
	svc, commentRepo, taskRepo := setupCommentService()

	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{ID: taskID, Title: "A task"}

	c := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Body:       "bad metadata",
		Metadata:   json.RawMessage(`["not","an","object"]`),
	}

	err := svc.Create(context.Background(), c)
	require.Error(t, err)
	// And nothing was written — a refused comment must not land half-way.
	assert.Empty(t, commentRepo.items)
}
