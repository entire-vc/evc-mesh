package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/internal/storage"
)

// fakeUploadStore stands in for S3, the one external boundary of two-step
// uploads: it serves both the staged PUT target (UploadStore) and the store
// the ordinary upload services write to (service.StorageClient).
type fakeUploadStore struct {
	mu         sync.Mutex
	objects    map[string][]byte
	signed     []http.Header
	presignErr error
	statErr    error
	deleteErr  error
	// brokenRead makes the pinned read fail mid-stream, as when the object
	// is replaced under the ETag.
	brokenRead bool
}

func newFakeUploadStore() *fakeUploadStore { return &fakeUploadStore{objects: map[string][]byte{}} }

func fakeETag(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:8]) }

func (f *fakeUploadStore) put(key string, b []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = append([]byte(nil), b...)
}

func (f *fakeUploadStore) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objects[key]
	return ok
}

func (f *fakeUploadStore) PresignPutURL(_ context.Context, key string, _ time.Duration, h http.Header) (string, error) {
	if f.presignErr != nil {
		return "", f.presignErr
	}
	f.mu.Lock()
	f.signed = append(f.signed, h)
	f.mu.Unlock()
	return "https://s3.test/put/" + key + "?X-Amz-Signature=x", nil
}

func (f *fakeUploadStore) StatObjectInfo(_ context.Context, key string) (storage.ObjectInfo, error) {
	if f.statErr != nil {
		return storage.ObjectInfo{}, f.statErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objects[key]
	if !ok {
		return storage.ObjectInfo{}, storage.ErrNotFound
	}
	return storage.ObjectInfo{Size: int64(len(b)), ETag: fakeETag(b)}, nil
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("precondition failed") }

func (f *fakeUploadStore) DownloadIfMatch(_ context.Context, key, etag string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objects[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	if fakeETag(b) != etag {
		return nil, errors.New("precondition failed")
	}
	if f.brokenRead {
		return io.NopCloser(brokenReader{}), nil
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (f *fakeUploadStore) Delete(_ context.Context, key string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.objects[key]; !ok {
		return storage.ErrNotFound
	}
	delete(f.objects, key)
	return nil
}

func (f *fakeUploadStore) Upload(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.put(key, b)
	return nil
}

func (f *fakeUploadStore) Download(_ context.Context, key string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objects[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (f *fakeUploadStore) GetPresignedURL(_ context.Context, key string, _ time.Duration, _, _ string) (string, error) {
	return "https://s3.test/get/" + key, nil
}

// uploadHTTP is one workspace with a project, a task, a document, a member
// agent and a member user, served by the real handler, services and
// repositories behind the real project access middleware.
type uploadHTTP struct {
	db                                *sqlx.DB
	e                                 *echo.Echo
	h                                 *UploadSessionHandler
	store                             *fakeUploadStore
	ws, project, task, doc            uuid.UUID
	agent, owner, member, otherWSAuth uuid.UUID
}

const (
	actorAgent       = "agent"
	actorAgentOther  = "agent-other-workspace"
	actorMember      = "member"
	actorUnknownUser = "unknown-user"
)

func newUploadHTTP(t *testing.T) *uploadHTTP {
	t.Helper()
	db := reportDB(t)
	ctx := context.Background()
	f := &uploadHTTP{db: db, store: newFakeUploadStore(), project: uuid.New(), task: uuid.New(), doc: uuid.New(),
		agent: uuid.New(), otherWSAuth: uuid.New()}
	f.ws, f.owner = seedRuntimeWorkspace(t, db)
	sfx := f.ws.String()[:8]
	member := &domain.User{ID: uuid.New(), Email: "upl-m-" + sfx + "@example.com", PasswordHash: "x", Name: "Upload Member", Username: "upl-m-" + sfx, IsActive: true}
	require.NoError(t, postgres.NewUserRepo(db).Create(ctx, member))
	f.member = member.ID
	status := uuid.New()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1,$2,$3,'member')`, []any{uuid.New(), f.ws, f.member}},
		{`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix) VALUES ($1,$2,'Upload Agent',$3,'not-a-real-hash','test')`, []any{f.agent, f.ws, "upl-agent-" + sfx}},
		{`INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1,$2,'Uploads',$3)`, []any{f.project, f.ws, "upl-" + sfx}},
		{`INSERT INTO task_statuses (id, project_id, name, slug, category) VALUES ($1,$2,'Todo','todo','todo')`, []any{status, f.project}},
		{`INSERT INTO tasks (id, project_id, status_id, title, task_number, created_by) VALUES ($1,$2,$3,'upload task',1,$4)`, []any{f.task, f.project, status, f.agent}},
		{`INSERT INTO documents (id, project_id, slug, title, storage_key, created_by, created_by_type) VALUES ($1,$2,$3,'Upload doc',$4,$5,'user')`, []any{f.doc, f.project, "upl-doc-" + sfx, "docs/" + sfx, f.owner}},
		{`INSERT INTO project_members (id, project_id, workspace_id, agent_id) VALUES ($1,$2,$3,$4)`, []any{uuid.New(), f.project, f.ws, f.agent}},
		{`INSERT INTO project_members (id, project_id, workspace_id, user_id) VALUES ($1,$2,$3,$4)`, []any{uuid.New(), f.project, f.ws, f.member}},
	} {
		_, err := db.ExecContext(ctx, q.sql, q.args...)
		require.NoError(t, err, q.sql)
	}
	t.Cleanup(func() {
		bg := context.Background()
		for _, q := range []string{
			`DELETE FROM upload_sessions WHERE workspace_id = $1`,
			`DELETE FROM artifacts WHERE task_id IN (SELECT t.id FROM tasks t JOIN projects p ON p.id = t.project_id WHERE p.workspace_id = $1)`,
			`DELETE FROM document_attachments WHERE document_id IN (SELECT d.id FROM documents d JOIN projects p ON p.id = d.project_id WHERE p.workspace_id = $1)`,
			`DELETE FROM documents WHERE project_id IN (SELECT id FROM projects WHERE workspace_id = $1)`,
			`DELETE FROM project_members WHERE workspace_id = $1`,
			`DELETE FROM tasks WHERE project_id IN (SELECT id FROM projects WHERE workspace_id = $1)`,
			`DELETE FROM task_statuses WHERE project_id IN (SELECT id FROM projects WHERE workspace_id = $1)`,
			`DELETE FROM projects WHERE workspace_id = $1`,
			`DELETE FROM agents WHERE workspace_id = $1`,
			`DELETE FROM workspace_members WHERE workspace_id = $1`,
			`DELETE FROM workspaces WHERE id = $1`,
		} {
			_, _ = db.ExecContext(bg, q, f.ws)
		}
		_, _ = db.ExecContext(bg, `DELETE FROM users WHERE id IN ($1,$2)`, f.owner, f.member)
	})

	docs := postgres.NewDocumentRepo(db)
	attachments := postgres.NewDocumentAttachmentRepo(db)
	artifacts := postgres.NewArtifactRepo(db)
	f.h = NewUploadSessionHandler(UploadSessionDeps{
		Sessions: postgres.NewUploadSessionRepo(db), Store: f.store,
		Documents: docs, Attachments: attachments,
		AttachmentSvc: service.NewDocumentAttachmentService(attachments, docs, f.store),
		ArtifactSvc:   service.NewArtifactService(artifacts, f.store, postgres.NewActivityLogRepo(db)),
		TaskBytes:     artifacts, Members: postgres.NewWorkspaceMemberRepo(db),
	})

	f.e = echo.New()
	// ident stands in for the auth middleware: it sets the context keys the
	// real one sets for an agent key or a user session.
	ident := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			switch c.Request().Header.Get("X-Test-Actor") {
			case actorAgent, actorAgentOther:
				c.Set(mw.ContextKeyAuthType, mw.AuthTypeAgent)
				c.Set(mw.ContextKeyAgentID, f.agent)
				ws := f.ws
				if c.Request().Header.Get("X-Test-Actor") == actorAgentOther {
					ws = f.otherWSAuth
				}
				c.Set(mw.ContextKeyAgentAuthWorkspaceID, ws)
			case actorMember:
				c.Set(mw.ContextKeyUserID, f.member)
			case actorUnknownUser:
				c.Set(mw.ContextKeyUserID, uuid.New())
			}
			return next(c)
		}
	}
	// scoped stands in for the workspace guard on the routes that have one.
	scoped := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(mw.ContextKeyWorkspaceID, f.ws)
			if !mw.IsAgent(c) {
				c.Set(mw.ContextKeyWorkspaceRole, domain.RoleMember)
			}
			return next(c)
		}
	}
	g := f.e.Group("/api/v1", ident, mw.RequireTaskProjectMember(db))
	g.POST("/documents/:doc_id/attachments/uploads", f.h.CreateDocumentUpload, scoped)
	g.POST("/tasks/:task_id/artifacts/uploads", f.h.CreateArtifactUpload, scoped)
	g.POST("/uploads/:upload_id/confirm", f.h.Confirm)
	return f
}

func (f *uploadHTTP) call(t *testing.T, actor, path string, body any) (code int, out map[string]any) {
	t.Helper()
	var r io.Reader = http.NoBody
	switch b := body.(type) {
	case nil:
	case string:
		r = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		require.NoError(t, err)
		r = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(http.MethodPost, path, r)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Actor", actor)
	rec := httptest.NewRecorder()
	f.e.ServeHTTP(rec, req)
	out = map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (f *uploadHTTP) docPath() string {
	return "/api/v1/documents/" + f.doc.String() + "/attachments/uploads"
}

func (f *uploadHTTP) taskPath() string {
	return "/api/v1/tasks/" + f.task.String() + "/artifacts/uploads"
}

func uploadDecl(name, mimeType string, b []byte) map[string]any {
	sum := sha256.Sum256(b)
	return map[string]any{"name": name, "mime_type": mimeType, "size_bytes": len(b), "sha256": hex.EncodeToString(sum[:])}
}

// declare creates an upload as actor and returns its id and staging key.
func (f *uploadHTTP) declare(t *testing.T, actor, path string, body map[string]any) (id, key string) {
	t.Helper()
	code, out := f.call(t, actor, path, body)
	require.Equal(t, http.StatusCreated, code, "%v", out)
	id, _ = out["upload_id"].(string)
	require.NotEmpty(t, id)
	return id, "uploads/" + f.ws.String() + "/" + id
}

func (f *uploadHTTP) confirm(t *testing.T, actor, id string) (code int, out map[string]any) {
	t.Helper()
	return f.call(t, actor, "/api/v1/uploads/"+id+"/confirm", nil)
}

func (f *uploadHTTP) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, f.db.Get(&n, q, args...))
	return n
}

func (f *uploadHTTP) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	_, err := f.db.Exec(q, args...)
	require.NoError(t, err)
}

// pngFile is a file http.DetectContentType reads as image/png.
func pngFile(size int) []byte {
	b := make([]byte, size)
	copy(b, "\x89PNG\r\n\x1a\n")
	for i := 8; i < size; i++ {
		b[i] = byte(i)
	}
	return b
}

func TestUploadSessionDB_DocumentRoundTrip(t *testing.T) {
	f := newUploadHTTP(t)
	png := pngFile(600)
	code, out := f.call(t, actorAgent, f.docPath(), uploadDecl("diagram.png", "image/png", png))
	require.Equal(t, http.StatusCreated, code, "%v", out)
	assert.Equal(t, http.MethodPut, out["upload_method"])
	assert.Equal(t, map[string]any{}, out["upload_headers"])
	assert.EqualValues(t, service.MaxDocumentAttachmentBytes, out["max_bytes"])
	id := out["upload_id"].(string)
	key := "uploads/" + f.ws.String() + "/" + id
	assert.Contains(t, out["upload_url"], key)
	require.Len(t, f.store.signed, 1)
	assert.Equal(t, "600", f.store.signed[0].Get("Content-Length"), "only the length is signed")

	code, out = f.confirm(t, actorAgent, id)
	require.Equal(t, http.StatusConflict, code, "confirm before the PUT: %v", out)
	assert.Equal(t, "upload_not_received", out["error_code"])

	f.store.put(key, png)
	code, out = f.confirm(t, actorAgent, id)
	require.Equal(t, http.StatusCreated, code, "%v", out)
	att := out["attachment_id"].(string)
	assert.Equal(t, f.doc.String(), out["document_id"])
	assert.Equal(t, true, out["renders_inline"])
	assert.Equal(t, "/api/v1/document-attachments/"+att+"/download?disposition=inline", out["download_path"])
	assert.Equal(t, "![diagram.png](/api/v1/document-attachments/"+att+"/download?disposition=inline)", out["markdown"])
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM document_attachments WHERE id = $1 AND document_id = $2 AND size_bytes = 600`, att, f.doc))
	assert.False(t, f.store.has(key), "the staged object is removed once confirmed")
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM upload_sessions WHERE id = $1 AND result_id = $2 AND confirmed_at IS NOT NULL`, id, att))

	code, out = f.confirm(t, actorAgent, id)
	require.Equal(t, http.StatusOK, code, "replay: %v", out)
	assert.Equal(t, att, out["attachment_id"])
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM document_attachments WHERE document_id = $1`, f.doc))

	// A deleted attachment is gone for the replay too.
	f.exec(t, `UPDATE document_attachments SET deleted_at = now() WHERE id = $1`, att)
	code, out = f.confirm(t, actorAgent, id)
	assert.Equal(t, http.StatusNotFound, code, "%v", out)
}

func TestUploadSessionDB_ArtifactRoundTrip(t *testing.T) {
	f := newUploadHTTP(t)
	png := pngFile(700)
	decl := uploadDecl("shot.png", "image/png", png)
	decl["artifact_type"] = "report"
	code, out := f.call(t, actorAgent, f.taskPath(), decl)
	require.Equal(t, http.StatusCreated, code, "%v", out)
	assert.EqualValues(t, service.DefaultArtifactMaxBytes, out["max_bytes"])
	first := out["upload_id"].(string)
	f.store.put("uploads/"+f.ws.String()+"/"+first, png)
	code, out = f.confirm(t, actorAgent, first)
	require.Equal(t, http.StatusCreated, code, "%v", out)
	art := out["artifact_id"].(string)
	assert.Equal(t, f.task.String(), out["task_id"])
	assert.Equal(t, "![shot.png](/api/v1/artifacts/"+art+"/download?disposition=inline)", out["markdown"])
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM artifacts WHERE id = $1 AND task_id = $2 AND artifact_type = 'report' AND uploaded_by = $3`, art, f.task, f.agent))

	code, out = f.confirm(t, actorAgent, first)
	require.Equal(t, http.StatusOK, code, "%v", out)
	assert.Equal(t, art, out["artifact_id"])
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM artifacts WHERE task_id = $1`, f.task))

	// A type that is not sniffed passes as declared, and links instead of
	// embedding.
	text := []byte("plain [notes]\n")
	id, key := f.declare(t, actorAgent, f.taskPath(), uploadDecl("notes [v1].txt", "text/plain", text))
	f.store.put(key, text)
	code, out = f.confirm(t, actorAgent, id)
	require.Equal(t, http.StatusCreated, code, "%v", out)
	assert.Equal(t, false, out["renders_inline"])
	assert.True(t, strings.HasPrefix(out["markdown"].(string), `[notes \[v1\].txt](/api/v1/artifacts/`), out["markdown"])

	// A deleted artifact is gone for the replay too.
	f.exec(t, `DELETE FROM artifacts WHERE id = $1`, art)
	code, out = f.confirm(t, actorAgent, first)
	assert.Equal(t, http.StatusNotFound, code, "%v", out)
}

func TestUploadSessionDB_Refusals(t *testing.T) {
	f := newUploadHTTP(t)
	png := pngFile(500)

	t.Run("Declaration", func(t *testing.T) {
		bad := uploadDecl("a.png", "image/png", png)
		bad["sha256"] = "xyz"
		code, out := f.call(t, actorAgent, f.docPath(), bad)
		assert.Equal(t, http.StatusUnprocessableEntity, code, "%v", out)
		assert.Equal(t, "validation_failed", out["error_code"])

		badType := uploadDecl("a.png", "image/png", png)
		badType["artifact_type"] = "spreadsheet"
		code, out = f.call(t, actorAgent, f.taskPath(), badType)
		assert.Equal(t, http.StatusUnprocessableEntity, code, "%v", out)

		code, _ = f.call(t, actorAgent, f.docPath(), "{not json")
		assert.Equal(t, http.StatusBadRequest, code)
		code, _ = f.call(t, actorAgent, f.taskPath(), "{not json")
		assert.Equal(t, http.StatusBadRequest, code)

		big := uploadDecl("big.png", "image/png", png)
		big["size_bytes"] = service.MaxDocumentAttachmentBytes + 1
		code, out = f.call(t, actorAgent, f.docPath(), big)
		assert.Equal(t, http.StatusUnprocessableEntity, code, "%v", out)
		assert.Equal(t, "file_too_large", out["error_code"])

		code, out = f.call(t, actorAgent, "/api/v1/documents/"+uuid.NewString()+"/attachments/uploads", uploadDecl("a.png", "image/png", png))
		assert.Equal(t, http.StatusNotFound, code, "%v", out)

		code, out = f.call(t, actorAgent, "/api/v1/tasks/"+uuid.NewString()+"/artifacts/uploads", uploadDecl("a.png", "image/png", png))
		assert.Equal(t, http.StatusNotFound, code, "%v", out)
	})

	t.Run("ArtifactLimits", func(t *testing.T) {
		maxArtifact, maxTask := f.h.maxArtifact, f.h.maxTask
		t.Cleanup(func() { f.h.maxArtifact, f.h.maxTask = maxArtifact, maxTask })
		f.h.maxArtifact = int64(len(png) - 1)
		code, out := f.call(t, actorAgent, f.taskPath(), uploadDecl("a.png", "image/png", png))
		assert.Equal(t, http.StatusUnprocessableEntity, code, "%v", out)
		assert.Equal(t, "file_too_large", out["error_code"])
		assert.EqualValues(t, len(png)-1, out["context"].(map[string]any)["limit_bytes"])

		f.h.maxArtifact, f.h.maxTask = maxArtifact, int64(len(png)-1)
		code, out = f.call(t, actorAgent, f.taskPath(), uploadDecl("a.png", "image/png", png))
		assert.Equal(t, http.StatusUnprocessableEntity, code, "%v", out)
		assert.Equal(t, "file_too_large", out["error_code"])

		// The declaration fits, but the task fills up before the confirm: the
		// upload's own reservation refuses it.
		f.h.maxTask = int64(len(png))
		f.h.artifactSvc = service.NewArtifactService(postgres.NewArtifactRepo(f.db), f.store, postgres.NewActivityLogRepo(f.db),
			service.WithArtifactLimits(maxArtifact, int64(len(png))))
		t.Cleanup(func() {
			f.h.artifactSvc = service.NewArtifactService(postgres.NewArtifactRepo(f.db), f.store, postgres.NewActivityLogRepo(f.db))
		})
		first, firstKey := f.declare(t, actorAgent, f.taskPath(), uploadDecl("one.png", "image/png", png))
		second, secondKey := f.declare(t, actorAgent, f.taskPath(), uploadDecl("two.png", "image/png", png))
		f.store.put(firstKey, png)
		f.store.put(secondKey, png)
		code, out = f.confirm(t, actorAgent, first)
		require.Equal(t, http.StatusCreated, code, "%v", out)
		code, out = f.confirm(t, actorAgent, second)
		assert.Equal(t, http.StatusUnprocessableEntity, code, "%v", out)
		assert.Equal(t, "file_too_large", out["error_code"])
	})

	t.Run("StagedBytes", func(t *testing.T) {
		id, key := f.declare(t, actorAgent, f.docPath(), uploadDecl("s.png", "image/png", png))
		f.store.put(key, png[:400])
		code, out := f.confirm(t, actorAgent, id)
		assert.Equal(t, http.StatusUnprocessableEntity, code, "%v", out)
		assert.Equal(t, "size_mismatch", out["error_code"])
		assert.False(t, f.store.has(key), "a wrong-size object is removed")

		corrupt := append([]byte(nil), png...)
		corrupt[100] ^= 0xff
		f.store.put(key, corrupt)
		code, out = f.confirm(t, actorAgent, id)
		assert.Equal(t, http.StatusUnprocessableEntity, code, "%v", out)
		assert.Equal(t, "checksum_mismatch", out["error_code"])
		assert.False(t, f.store.has(key), "a corrupted object is removed")

		// The session stays open: the right bytes confirm.
		f.store.put(key, png)
		code, out = f.confirm(t, actorAgent, id)
		assert.Equal(t, http.StatusCreated, code, "%v", out)

		text := []byte("this is not a png at all, only text")
		id, key = f.declare(t, actorAgent, f.docPath(), uploadDecl("fake.png", "image/png", text))
		f.store.put(key, text)
		code, out = f.confirm(t, actorAgent, id)
		assert.Equal(t, http.StatusUnprocessableEntity, code, "%v", out)
		assert.Equal(t, "mime_mismatch", out["error_code"])
		assert.False(t, f.store.has(key))
	})

	t.Run("SessionState", func(t *testing.T) {
		id, key := f.declare(t, actorAgent, f.docPath(), uploadDecl("e.png", "image/png", png))
		f.store.put(key, png)
		f.exec(t, `UPDATE upload_sessions SET expires_at = now() - interval '1 second' WHERE id = $1`, id)
		code, out := f.confirm(t, actorAgent, id)
		assert.Equal(t, http.StatusGone, code, "%v", out)
		assert.Equal(t, "upload_expired", out["error_code"])
		assert.False(t, f.store.has(key), "an expired upload's object is removed")

		id, key = f.declare(t, actorAgent, f.docPath(), uploadDecl("busy.png", "image/png", png))
		f.store.put(key, png)
		f.exec(t, `UPDATE upload_sessions SET claimed_until = now() + interval '10 minutes' WHERE id = $1`, id)
		code, out = f.confirm(t, actorAgent, id)
		assert.Equal(t, http.StatusConflict, code, "a confirm under another's lease: %v", out)
		assert.True(t, f.store.has(key), "a refused concurrent confirm leaves the object")

		f.exec(t, `UPDATE upload_sessions SET expires_at = now() - interval '1 second' WHERE id = $1`, id)
		code, _ = f.confirm(t, actorAgent, id)
		assert.Equal(t, http.StatusGone, code, "expired under a lease")

		for _, bad := range []string{uuid.NewString(), "not-a-uuid", uuid.Nil.String()} {
			code, _ = f.confirm(t, actorAgent, bad)
			assert.Equal(t, http.StatusNotFound, code, bad)
		}
		code, _ = f.confirm(t, "", uuid.NewString())
		assert.Equal(t, http.StatusNotFound, code, "no credential")
	})

	t.Run("ObjectStorageFailures", func(t *testing.T) {
		f.store.presignErr = errors.New("down")
		code, _ := f.call(t, actorAgent, f.docPath(), uploadDecl("p.png", "image/png", png))
		f.store.presignErr = nil
		assert.Equal(t, http.StatusServiceUnavailable, code)

		id, key := f.declare(t, actorAgent, f.docPath(), uploadDecl("p.png", "image/png", png))
		f.store.put(key, png)
		f.store.statErr = errors.New("down")
		code, _ = f.confirm(t, actorAgent, id)
		f.store.statErr = nil
		assert.Equal(t, http.StatusServiceUnavailable, code)

		f.store.brokenRead = true
		code, out := f.confirm(t, actorAgent, id)
		f.store.brokenRead = false
		assert.Equal(t, http.StatusConflict, code, "%v", out)
		assert.True(t, f.store.has(key), "a read that broke leaves the object for the next confirm")

		f.store.deleteErr = errors.New("down")
		code, out = f.confirm(t, actorAgent, id)
		f.store.deleteErr = nil
		assert.Equal(t, http.StatusCreated, code, "a failed cleanup does not fail the confirm: %v", out)
	})

	t.Run("NoObjectStorage", func(t *testing.T) {
		h := NewUploadSessionHandler(UploadSessionDeps{Sessions: postgres.NewUploadSessionRepo(f.db)})
		for _, fn := range []echo.HandlerFunc{h.Confirm, func(c echo.Context) error {
			return h.create(c, f.ws, postgres.UploadKindDocumentAttachment, f.doc, createUploadRequest{}, 1)
		}} {
			rec := httptest.NewRecorder()
			c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/", http.NoBody), rec)
			require.NoError(t, fn(c))
			assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		}
	})
}

// TestUploadSessionDB_ConfirmChecksAccessNow: the confirm and its replay
// apply the declaring actor's access as it is now, not as it was when the
// upload was declared.
func TestUploadSessionDB_ConfirmChecksAccessNow(t *testing.T) {
	f := newUploadHTTP(t)
	png := pngFile(500)
	put := func(actor, path, name string) string {
		id, key := f.declare(t, actor, path, uploadDecl(name, "image/png", png))
		f.store.put(key, png)
		return id
	}
	doneArtifact := put(actorAgent, f.taskPath(), "done.png")
	code, out := f.confirm(t, actorAgent, doneArtifact)
	require.Equal(t, http.StatusCreated, code, "%v", out)
	pendingArtifact := put(actorAgent, f.taskPath(), "pending.png")
	pendingDoc := put(actorAgent, f.docPath(), "pending-doc.png")

	code, _ = f.confirm(t, actorAgentOther, pendingArtifact)
	assert.Equal(t, http.StatusNotFound, code, "the agent's key for another workspace does not reach this upload")

	f.exec(t, `DELETE FROM project_members WHERE project_id = $1 AND agent_id = $2`, f.project, f.agent)
	code, _ = f.call(t, actorAgent, f.taskPath(), uploadDecl("after.png", "image/png", png))
	require.Equal(t, http.StatusForbidden, code, "positive control: declaring is refused after removal")

	code, out = f.confirm(t, actorAgent, pendingArtifact)
	assert.Equal(t, http.StatusForbidden, code, "%v", out)
	code, out = f.confirm(t, actorAgent, pendingDoc)
	assert.Equal(t, http.StatusForbidden, code, "%v", out)
	code, out = f.confirm(t, actorAgent, doneArtifact)
	assert.Equal(t, http.StatusForbidden, code, "replay after removal: %v", out)
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM artifacts WHERE task_id = $1`, f.task), "only the artifact confirmed before removal")
	assert.Equal(t, 0, f.count(t, `SELECT count(*) FROM document_attachments WHERE document_id = $1`, f.doc))

	// A user: the workspace role and the project membership are both read now.
	memberDoc := put(actorMember, f.docPath(), "member.png")
	code, out = f.confirm(t, actorMember, memberDoc)
	require.Equal(t, http.StatusCreated, code, "%v", out)
	viewerDoc := put(actorMember, f.docPath(), "viewer.png")
	f.exec(t, `UPDATE workspace_members SET role = 'viewer' WHERE workspace_id = $1 AND user_id = $2`, f.ws, f.member)
	code, out = f.confirm(t, actorMember, viewerDoc)
	assert.Equal(t, http.StatusForbidden, code, "a viewer cannot confirm: %v", out)
	f.exec(t, `UPDATE workspace_members SET role = 'member' WHERE workspace_id = $1 AND user_id = $2`, f.ws, f.member)
	f.exec(t, `DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`, f.project, f.member)
	code, out = f.confirm(t, actorMember, viewerDoc)
	assert.Equal(t, http.StatusForbidden, code, "out of the project: %v", out)
	f.exec(t, `DELETE FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`, f.ws, f.member)
	code, _ = f.confirm(t, actorMember, viewerDoc)
	assert.Equal(t, http.StatusNotFound, code, "out of the workspace")
	code, _ = f.confirm(t, actorUnknownUser, viewerDoc)
	assert.Equal(t, http.StatusNotFound, code, "another user")

	// The document itself gone.
	f.exec(t, `INSERT INTO project_members (id, project_id, workspace_id, agent_id) VALUES ($1,$2,$3,$4)`, uuid.New(), f.project, f.ws, f.agent)
	f.exec(t, `UPDATE documents SET deleted_at = now() WHERE id = $1`, f.doc)
	code, out = f.confirm(t, actorAgent, pendingDoc)
	assert.Equal(t, http.StatusNotFound, code, "%v", out)
}

// TestUploadSessionDB_ResultIsWrittenOnce: the result's id is fixed by the
// first confirm, so a confirm after an interrupted one answers with the row
// that one wrote, and one that failed before writing writes it under the same
// id; a failed record of the result does not fail the confirm.
func TestUploadSessionDB_ResultIsWrittenOnce(t *testing.T) {
	f := newUploadHTTP(t)
	png := pngFile(500)

	// Written, not recorded: the earlier confirm wrote the row and stopped.
	id, key := f.declare(t, actorAgent, f.docPath(), uploadDecl("written.png", "image/png", png))
	f.store.put(key, png)
	written := uuid.New()
	f.exec(t, `UPDATE upload_sessions SET pending_result_id = $2 WHERE id = $1`, id, written)
	_, err := f.h.attachSvc.Upload(context.Background(), service.UploadDocumentAttachmentInput{
		ID: written, DocumentID: f.doc, WorkspaceID: f.ws, Name: "written.png", MimeType: "image/png",
		Size: int64(len(png)), Reader: bytes.NewReader(png), UploadedBy: f.agent, UploadedByType: domain.ActorTypeAgent,
	})
	require.NoError(t, err)
	code, out := f.confirm(t, actorAgent, id)
	require.Equal(t, http.StatusOK, code, "%v", out)
	assert.Equal(t, written.String(), out["attachment_id"])
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM document_attachments WHERE document_id = $1`, f.doc))
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM upload_sessions WHERE id = $1 AND result_id = $2`, id, written))
	assert.False(t, f.store.has(key))

	// Not written: the earlier confirm took the lease and failed before the
	// row; this one writes it under the id that one fixed.
	id, key = f.declare(t, actorAgent, f.taskPath(), uploadDecl("unwritten.png", "image/png", png))
	f.store.put(key, png)
	fixed := uuid.New()
	f.exec(t, `UPDATE upload_sessions SET pending_result_id = $2 WHERE id = $1`, id, fixed)
	code, out = f.confirm(t, actorAgent, id)
	require.Equal(t, http.StatusCreated, code, "%v", out)
	assert.Equal(t, fixed.String(), out["artifact_id"])

	// The record fails: the confirm still answers with the row, the replay
	// answers with the same one, and the record lands when the database
	// takes it.
	name := "receipt-" + f.ws.String()[:8] + ".png"
	trigger := "upl_hdb_receipt_" + f.ws.String()[:8]
	// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only DDL cannot bind identifiers; the trigger name is a test constant plus the fixture's generated workspace UUID, no external input
	drop := fmt.Sprintf(`DROP TRIGGER IF EXISTS %[1]s ON upload_sessions; DROP FUNCTION IF EXISTS %[1]s()`, trigger)
	t.Cleanup(func() { _, _ = f.db.Exec(drop) })
	id, key = f.declare(t, actorAgent, f.docPath(), uploadDecl(name, "image/png", png))
	f.store.put(key, png)
	f.exec(t, fmt.Sprintf(`CREATE FUNCTION %[1]s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.name = '%[2]s' AND NEW.confirmed_at IS NOT NULL THEN RAISE EXCEPTION 'injected receipt failure'; END IF;
		RETURN NEW; END $$;
		CREATE TRIGGER %[1]s BEFORE UPDATE ON upload_sessions FOR EACH ROW EXECUTE FUNCTION %[1]s()`, trigger, name))
	code, out = f.confirm(t, actorAgent, id)
	require.Equal(t, http.StatusCreated, code, "%v", out)
	first := out["attachment_id"]
	code, out = f.confirm(t, actorAgent, id)
	require.Equal(t, http.StatusOK, code, "%v", out)
	assert.Equal(t, first, out["attachment_id"])
	assert.Equal(t, 0, f.count(t, `SELECT count(*) FROM upload_sessions WHERE id = $1 AND confirmed_at IS NOT NULL`, id))
	f.exec(t, drop)
	f.store.put(key, png)
	code, out = f.confirm(t, actorAgent, id)
	require.Equal(t, http.StatusOK, code, "%v", out)
	assert.Equal(t, first, out["attachment_id"])
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM document_attachments WHERE name = $1`, name), "one row for one upload")
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM upload_sessions WHERE id = $1 AND confirmed_at IS NOT NULL`, id))
	assert.False(t, f.store.has(key))
}

// uploadSweepTestLock is the advisory lock key every sweep test holds, the
// same as in internal/repository/postgres/upload_session_repo_db_test.go: a
// sweep removes every expired session in the shared test database, and
// packages test in parallel, so two sweeps at once would take each other's rows.
const uploadSweepTestLock int64 = 0x75706c5377656570

func lockUploadSweepForTest(t *testing.T, db *sqlx.DB) {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Connx(ctx)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, uploadSweepTestLock)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, uploadSweepTestLock)
		_ = conn.Close()
	})
}

// TestSweepUploadsDB: the sweeper removes an abandoned upload with its staged
// object, counts an object already gone as removed, and keeps a session whose
// object could not be deleted.
func TestSweepUploadsDB(t *testing.T) {
	f := newUploadHTTP(t)
	lockUploadSweepForTest(t, f.db)
	png := pngFile(100)
	staged, stagedKey := f.declare(t, actorAgent, f.docPath(), uploadDecl("a.png", "image/png", png))
	f.store.put(stagedKey, png)
	gone, _ := f.declare(t, actorAgent, f.docPath(), uploadDecl("b.png", "image/png", png))
	f.exec(t, `UPDATE upload_sessions SET expires_at = now() - interval '1 hour' WHERE id IN ($1,$2)`, staged, gone)
	repo := postgres.NewUploadSessionRepo(f.db)
	mine := `SELECT count(*) FROM upload_sessions WHERE workspace_id = $1`

	f.store.deleteErr = errors.New("down")
	_, err := SweepUploads(context.Background(), repo, f.store)
	require.NoError(t, err)
	f.store.deleteErr = nil
	assert.Equal(t, 2, f.count(t, mine, f.ws), "nothing removed while storage refuses")

	for i := 0; i < 50; i++ {
		n, err := SweepUploads(context.Background(), repo, f.store)
		require.NoError(t, err)
		if n == 0 {
			break
		}
	}
	assert.Equal(t, 0, f.count(t, mine, f.ws))
	assert.False(t, f.store.has(stagedKey))
}
