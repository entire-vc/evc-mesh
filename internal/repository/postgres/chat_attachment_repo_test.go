package postgres

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func TestChatMaxAttachmentBytes(t *testing.T) {
	cases := []struct {
		name string
		mb   sql.NullInt64
		want int64
	}{
		{"no setting is the default", sql.NullInt64{}, ChatAttachmentHardLimit},
		{"lower setting wins", sql.NullInt64{Int64: 1, Valid: true}, 1 << 20},
		{"setting at the ceiling", sql.NullInt64{Int64: 25, Valid: true}, ChatAttachmentHardLimit},
		{"setting above the ceiling is clamped", sql.NullInt64{Int64: 100, Valid: true}, ChatAttachmentHardLimit},
		{"zero is not a limit", sql.NullInt64{Int64: 0, Valid: true}, ChatAttachmentHardLimit},
		{"negative is not a limit", sql.NullInt64{Int64: -3, Valid: true}, ChatAttachmentHardLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, chatMaxAttachmentBytes(tc.mb)) })
	}
}

func TestChatAttachmentErrors(t *testing.T) {
	var e *apierror.Error
	require.ErrorAs(t, chatTooLarge(), &e)
	require.Equal(t, http.StatusRequestEntityTooLarge, e.Code)
	require.Equal(t, "attachment_too_large", e.Details)
	require.ErrorAs(t, chatThreadNotFound(), &e)
	require.Equal(t, "thread_not_found", e.Details)
	require.ErrorAs(t, chatAttachmentNotFound(), &e)
	require.Equal(t, "attachment_not_found", e.Details)
}

// Agents, connectors and anonymous callers are refused before any query runs.
func TestChatAttachmentRepo_RefusesNonUsers(t *testing.T) {
	repo := NewChatAttachmentRepo(nil)
	for name, actor := range map[string]UserRuntimeActor{
		"agent":     {AgentID: uuid.New()},
		"connector": {UserID: uuid.New(), Connector: true},
		"anonymous": {},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := repo.Create(context.Background(), actor, uuid.New(), uuid.New(), "f", "text/plain", 1, nil, nil, func(_, _ uuid.UUID) string { return "k" })
			var e *apierror.Error
			require.ErrorAs(t, err, &e)
			require.Equal(t, http.StatusForbidden, e.Code)
			_, err = repo.GetOwned(context.Background(), actor, uuid.New(), uuid.New())
			require.ErrorAs(t, err, &e)
			require.Equal(t, http.StatusForbidden, e.Code)
		})
	}
}

func chatRepoDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to run the chat attachment repository against PostgreSQL")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestChatAttachmentRepo_LimitOwnershipAndLifecycle(t *testing.T) {
	db := chatRepoDB(t)
	ctx := context.Background()
	ws, project, agent, owner, stranger := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, u := range []uuid.UUID{owner, stranger} {
		_, err := db.Exec(`INSERT INTO users (id, email, username, password_hash, display_name) VALUES ($1,$2,$3,'x','chat user')`, u, u.String()+"@chat.test", "cu-"+u.String()[:30])
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id=$1`, u) })
	}
	_, err := db.Exec(`INSERT INTO workspaces (id, name, slug, owner_id) VALUES ($1,'chat attach',$2,$3)`, ws, ws.String(), owner)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id=$1`, ws) })
	_, err = db.Exec(`INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1,$2,'p',$3)`, project, ws, project.String())
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix) VALUES ($1,$2,'a',$3,'h','p')`, agent, ws, agent.String())
	require.NoError(t, err)
	thread := uuid.New()
	_, err = db.Exec(`INSERT INTO ext_agent_chat_threads (id, workspace_id, project_id, agent_id, user_id) VALUES ($1,$2,$3,$4,$5)`, thread, ws, project, agent, owner)
	require.NoError(t, err)

	repo := NewChatAttachmentRepo(db)
	key := func(w, id uuid.UUID) string { return "chat/" + w.String() + "/" + id.String() }
	create := func(who uuid.UUID, th uuid.UUID, size int64) (ChatAttachment, error) {
		w, h := 640, 480
		return repo.Create(ctx, UserRuntimeActor{UserID: who}, project, th, "shot.png", "image/png", size, &w, &h, key)
	}

	att, err := create(owner, thread, ChatAttachmentHardLimit)
	require.NoError(t, err)
	require.Equal(t, key(ws, att.ID), att.S3Key)
	require.Equal(t, ws, att.WorkspaceID)

	var e *apierror.Error
	_, err = create(owner, thread, ChatAttachmentHardLimit+1)
	require.ErrorAs(t, err, &e)
	require.Equal(t, http.StatusRequestEntityTooLarge, e.Code)

	// A workspace setting lowers the limit; a non-numeric one is ignored.
	_, err = db.Exec(`INSERT INTO integration_configs (workspace_id, provider, config, is_active) VALUES ($1,'agent-chat','{"max_attachment_mb": 1}',true)`, ws)
	require.NoError(t, err)
	_, err = create(owner, thread, 2<<20)
	require.ErrorAs(t, err, &e)
	require.Equal(t, http.StatusRequestEntityTooLarge, e.Code)
	_, err = create(owner, thread, 1<<20)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE integration_configs SET config='{"max_attachment_mb": "lots"}' WHERE workspace_id=$1`, ws)
	require.NoError(t, err)
	_, err = create(owner, thread, 2<<20)
	require.NoError(t, err, "an unreadable setting falls back to the default")

	// Ownership: the stranger gets thread_not_found / attachment_not_found, the owner succeeds.
	_, err = create(stranger, thread, 10)
	require.ErrorAs(t, err, &e)
	require.Equal(t, "thread_not_found", e.Details)
	_, err = repo.GetOwned(ctx, UserRuntimeActor{UserID: stranger}, project, att.ID)
	require.ErrorAs(t, err, &e)
	require.Equal(t, "attachment_not_found", e.Details)
	_, err = repo.GetOwned(ctx, UserRuntimeActor{UserID: owner}, uuid.New(), att.ID)
	require.ErrorAs(t, err, &e, "wrong project")
	got, err := repo.GetOwned(ctx, UserRuntimeActor{UserID: owner}, project, att.ID)
	require.NoError(t, err)
	require.Equal(t, att.ID, got.ID)
	require.Nil(t, got.UploadedAt)
	require.NotNil(t, got.Width)

	require.NoError(t, repo.MarkUploaded(ctx, att.ID))
	got, err = repo.GetOwned(ctx, UserRuntimeActor{UserID: owner}, project, att.ID)
	require.NoError(t, err)
	require.NotNil(t, got.UploadedAt)

	_, err = db.Exec(`UPDATE ext_agent_chat_threads SET deleted_at = now() WHERE id=$1`, thread)
	require.NoError(t, err)
	_, err = repo.GetOwned(ctx, UserRuntimeActor{UserID: owner}, project, att.ID)
	require.ErrorAs(t, err, &e, "a deleted thread hides its attachments")
}
