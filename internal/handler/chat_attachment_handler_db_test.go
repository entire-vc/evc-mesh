package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/storage"
)

// fakeChatStore stands in for S3, the one true external boundary here.
type fakeChatStore struct {
	objects map[string]int64
	signed  []http.Header
}

func (f *fakeChatStore) PresignPutURL(_ context.Context, key string, _ time.Duration, h http.Header) (string, error) {
	f.signed = append(f.signed, h)
	return "https://s3.test/put/" + key, nil
}

func (f *fakeChatStore) GetPresignedURL(_ context.Context, key string, _ time.Duration, _, _ string) (string, error) {
	return "https://s3.test/get/" + key, nil
}

func (f *fakeChatStore) StatObject(_ context.Context, key string) (int64, error) {
	if size, ok := f.objects[key]; ok {
		return size, nil
	}
	return 0, storage.ErrNotFound
}

func seedChatThread(t *testing.T, db *sqlx.DB, ws, project, agent, user uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.ExecContext(context.Background(), `INSERT INTO ext_agent_chat_threads (id, workspace_id, project_id, agent_id, user_id) VALUES ($1,$2,$3,$4,$5)`, id, ws, project, agent, user)
	require.NoError(t, err)
	return id
}

func seedChatUser(t *testing.T, db *sqlx.DB) uuid.UUID {
	t.Helper()
	_, user := seedRuntimeWorkspace(t, db)
	return user
}

func chatCall(t *testing.T, fn echo.HandlerFunc, method, body string, user uuid.UUID, params map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(method, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(mw.ContextKeyUserID, user)
	var names, values []string
	for k, v := range params {
		names, values = append(names, k), append(values, v)
	}
	c.SetParamNames(names...)
	c.SetParamValues(values...)
	if err := fn(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	return rec
}

func presignBody(thread uuid.UUID, size int64) string {
	b, _ := json.Marshal(map[string]any{"thread_id": thread, "file_name": "shot.png", "mime": "image/png", "size": size, "width": 640, "height": 480})
	return string(b)
}

func chatDetails(rec *httptest.ResponseRecorder) string {
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	d, _ := out["details"].(string)
	return d
}

type chatAttachmentEnv struct {
	db                         *sqlx.DB
	h                          *ChatAttachmentHandler
	store                      *fakeChatStore
	ws, project, owner, thread uuid.UUID
}

func chatAttachmentFixture(t *testing.T) chatAttachmentEnv {
	t.Helper()
	db := runtimeHTTPDB(t)
	ws, owner := seedRuntimeWorkspace(t, db)
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), `DELETE FROM workspaces WHERE id=$1`, ws) })
	project, _ := seedChatProject(t, db, ws)
	agent := seedOperatorAgent(t, db, ws, &owner)
	store := &fakeChatStore{objects: map[string]int64{}}
	return chatAttachmentEnv{db: db, h: NewChatAttachmentHandler(postgres.NewChatAttachmentRepo(db), store), store: store,
		ws: ws, project: project, owner: owner, thread: seedChatThread(t, db, ws, project, agent, owner)}
}

func TestChatAttachment_SizeLimitFromWorkspaceSettings(t *testing.T) {
	f := chatAttachmentFixture(t)
	db, h, store, ws, project, owner, thread := f.db, f.h, f.store, f.ws, f.project, f.owner, f.thread
	params := map[string]string{"proj_id": project.String()}

	// Default limit is 25 MiB: exactly at the limit passes, one byte over is refused.
	rec := chatCall(t, h.Presign, http.MethodPost, presignBody(thread, 25<<20), owner, params)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var ok map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ok))
	require.Equal(t, "PUT", ok["upload_method"])
	require.Contains(t, ok["upload_url"], "https://s3.test/put/chat/"+ws.String()+"/")
	require.Equal(t, "image/png", store.signed[0].Get("Content-Type"))
	require.Equal(t, "26214400", store.signed[0].Get("Content-Length"))

	rec = chatCall(t, h.Presign, http.MethodPost, presignBody(thread, 25<<20+1), owner, params)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
	require.Equal(t, "attachment_too_large", chatDetails(rec))

	// A workspace setting lowers the limit; red control: the same 2 MiB file passed before.
	rec = chatCall(t, h.Presign, http.MethodPost, presignBody(thread, 2<<20), owner, params)
	require.Equal(t, http.StatusCreated, rec.Code)
	_, err := db.ExecContext(context.Background(), `INSERT INTO integration_configs (workspace_id, provider, config, is_active) VALUES ($1,'agent-chat','{"max_attachment_mb": 1}',true)`, ws)
	require.NoError(t, err)
	rec = chatCall(t, h.Presign, http.MethodPost, presignBody(thread, 2<<20), owner, params)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
	rec = chatCall(t, h.Presign, http.MethodPost, presignBody(thread, 1<<20), owner, params)
	require.Equal(t, http.StatusCreated, rec.Code)

	// The setting cannot raise the ceiling above the Docs limit.
	_, err = db.ExecContext(context.Background(), `UPDATE integration_configs SET config='{"max_attachment_mb": 100}' WHERE workspace_id=$1`, ws)
	require.NoError(t, err)
	rec = chatCall(t, h.Presign, http.MethodPost, presignBody(thread, 26<<20), owner, params)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)

	// Refused requests left no rows behind: only the three accepted ones exist.
	var n int
	require.NoError(t, db.GetContext(context.Background(), &n, `SELECT count(*) FROM ext_agent_chat_attachments WHERE workspace_id=$1`, ws))
	require.Equal(t, 3, n)
}

func TestChatAttachment_StoresImageDimensions(t *testing.T) {
	f := chatAttachmentFixture(t)
	db, h, ws, project, owner, thread := f.db, f.h, f.ws, f.project, f.owner, f.thread
	rec := chatCall(t, h.Presign, http.MethodPost, presignBody(thread, 1024), owner, map[string]string{"proj_id": project.String()})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var w, hgt int
	require.NoError(t, db.QueryRow(`SELECT width, height FROM ext_agent_chat_attachments WHERE workspace_id=$1`, ws).Scan(&w, &hgt))
	require.Equal(t, [2]int{640, 480}, [2]int{w, hgt})

	// Dimensions on a non-image are refused.
	body := strings.Replace(presignBody(thread, 1024), "image/png", "application/pdf", 1)
	rec = chatCall(t, h.Presign, http.MethodPost, body, owner, map[string]string{"proj_id": project.String()})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
}

func TestChatAttachment_PrivateToThreadOwner(t *testing.T) {
	f := chatAttachmentFixture(t)
	db, h, store, project, owner, thread := f.db, f.h, f.store, f.project, f.owner, f.thread
	stranger := seedChatUser(t, db)
	params := map[string]string{"proj_id": project.String()}

	// Upload into someone else's thread: thread_not_found, same as a missing thread.
	rec := chatCall(t, h.Presign, http.MethodPost, presignBody(thread, 10), stranger, params)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "thread_not_found", chatDetails(rec))
	missing := chatCall(t, h.Presign, http.MethodPost, presignBody(uuid.New(), 10), owner, params)
	require.Equal(t, http.StatusNotFound, missing.Code)
	require.Equal(t, chatDetails(rec), chatDetails(missing))

	// The owner uploads; a stranger cannot download, the owner can (red control).
	rec = chatCall(t, h.Presign, http.MethodPost, presignBody(thread, 10), owner, params)
	require.Equal(t, http.StatusCreated, rec.Code)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	id := out["attachment_id"].(string)
	var key string
	require.NoError(t, db.GetContext(context.Background(), &key, `SELECT s3_key FROM ext_agent_chat_attachments WHERE id=$1`, id))
	dl := map[string]string{"proj_id": project.String(), "attachment_id": id}

	// Not uploaded yet: nothing to serve, even to the owner.
	require.Equal(t, http.StatusNotFound, chatCall(t, h.Download, http.MethodGet, "", owner, dl).Code)
	store.objects[key] = 10

	rec = chatCall(t, h.Download, http.MethodGet, "", stranger, dl)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "attachment_not_found", chatDetails(rec))
	require.Empty(t, rec.Header().Get("Location"))

	rec = chatCall(t, h.Download, http.MethodGet, "", owner, dl)
	require.Equal(t, http.StatusFound, rec.Code)
	require.Equal(t, "https://s3.test/get/"+key, rec.Header().Get("Location"))
	var up *time.Time
	require.NoError(t, db.GetContext(context.Background(), &up, `SELECT uploaded_at FROM ext_agent_chat_attachments WHERE id=$1`, id))
	require.NotNil(t, up)

	// The object removed after the first download: the next one is a 404, not a dead redirect.
	delete(store.objects, key)
	rec = chatCall(t, h.Download, http.MethodGet, "", owner, dl)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Empty(t, rec.Header().Get("Location"))
	store.objects[key] = 10

	// A soft-deleted thread hides its attachments from the owner too.
	_, err := db.ExecContext(context.Background(), `UPDATE ext_agent_chat_threads SET deleted_at=now() WHERE id=$1`, thread)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, chatCall(t, h.Download, http.MethodGet, "", owner, dl).Code)
}
