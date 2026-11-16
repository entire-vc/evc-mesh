package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// setupCommentWSEventEnv wires a comment service whose task lives in a project
// of workspace "acme", with the live WS publisher recorded.
func setupCommentWSEventEnv(t *testing.T) (svc CommentService, pub *MockWSPublisher, taskID, projectID, wsID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	workspaces := NewMockWorkspaceRepository()
	projects := NewMockProjectRepository()
	tasks := NewMockTaskRepository()
	wsID, projectID, taskID = uuid.New(), uuid.New(), uuid.New()
	require.NoError(t, workspaces.Create(ctx, &domain.Workspace{ID: wsID, Slug: "acme"}))
	require.NoError(t, projects.Create(ctx, &domain.Project{ID: projectID, WorkspaceID: wsID}))
	tasks.items[taskID] = &domain.Task{ID: taskID, ProjectID: projectID, Title: "t"}
	pub = NewMockWSPublisher()
	svc = NewCommentService(NewMockCommentRepository(), tasks, NewMockActivityLogRepository(),
		WithCommentProjectRepo(projects),
		WithCommentWorkspaceRepo(workspaces),
		WithCommentWSPublisher(pub),
	)
	return svc, pub, taskID, projectID, wsID
}

// commentCreatedFrames returns the comment.created frames the service handed
// to the WS publisher, keyed by channel.
func commentCreatedFrames(pub *MockWSPublisher) (channels []string, frames []map[string]any) {
	pub.mu.Lock()
	defer pub.mu.Unlock()
	for i, ev := range pub.events {
		m, ok := ev.(map[string]any)
		if !ok || m["event_type"] != "comment.created" {
			continue
		}
		channels = append(channels, pub.channels[i])
		frames = append(frames, m)
	}
	return channels, frames
}

// A new task comment reaches the workspace channel the hub fans out from, as an
// id-only frame whose event_type the hub turns into the WS message type.
func TestCommentCreate_PublishesCommentCreatedToWorkspaceChannel(t *testing.T) {
	svc, pub, taskID, projectID, wsID := setupCommentWSEventEnv(t)
	c := &domain.Comment{TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeUser, Body: "secret body"}
	require.NoError(t, svc.Create(context.Background(), c))

	channels, frames := commentCreatedFrames(pub)
	require.Len(t, frames, 1, "exactly one comment.created frame")
	assert.Equal(t, "ws:acme", channels[0])
	assert.Equal(t, map[string]any{
		"event_type":   "comment.created",
		"workspace_id": wsID.String(),
		"project_id":   projectID.String(),
		"task_id":      taskID.String(),
		"comment_id":   c.ID.String(),
	}, frames[0], "ids only: no body, no author")
}

// An internal note stays off the workspace channel: the hub hands every frame
// to all ws:<slug> subscribers, project access or not.
func TestCommentCreate_InternalNotePublishesNoCommentCreated(t *testing.T) {
	svc, pub, taskID, _, _ := setupCommentWSEventEnv(t)
	c := &domain.Comment{TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeUser, Body: "note", IsInternal: true}
	require.NoError(t, svc.Create(context.Background(), c))

	_, frames := commentCreatedFrames(pub)
	assert.Empty(t, frames)
}
