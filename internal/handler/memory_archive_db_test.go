package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// memoryArchiveDBEnv runs the archive, by-key, search and final-recall handlers
// over a real Postgres with the production project-access middleware. Only the
// caller's authentication is stood in (asAgent); everything after it is real.
type memoryArchiveDBEnv struct {
	db      *sqlx.DB
	ws      uuid.UUID
	project uuid.UUID
	h       *MemoryHandler
	repo    *postgres.MemoryRepo
}

func newMemoryArchiveDBEnv(t *testing.T) *memoryArchiveDBEnv {
	t.Helper()
	db := reservedTagTestDB(t)
	ws := newReservedTagTestWorkspace(t, db, false)
	repo := postgres.NewMemoryRepo(db)
	ms := service.NewMemoryService(repo, postgres.NewMemoryEdgesRepo(db), finalRecallEmbedder{})
	h := NewMemoryHandler(ms, postgres.NewWorkspaceMemberRepo(db))
	agents := postgres.NewAgentRepo(db)
	h.SetAgentRoleLookup(func(ctx context.Context, id uuid.UUID) (string, error) {
		a, err := agents.GetByID(ctx, id)
		if err != nil {
			return "", err
		}
		return a.Role, nil
	})
	h.SetProjectRoleLookup(ProjectMemberRoleFromDB(db))
	project := uuid.New()
	_, err := db.Exec(`INSERT INTO projects (id,workspace_id,name,slug) VALUES ($1,$2,'archive',$3)`, project, ws, "archive-"+project.String())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM memory_revisions WHERE workspace_id = $1`, ws)
		_, _ = db.Exec(`DELETE FROM memories WHERE workspace_id = $1`, ws)
		_, _ = db.Exec(`DELETE FROM project_members WHERE workspace_id = $1`, ws)
		_, _ = db.Exec(`DELETE FROM projects WHERE workspace_id = $1`, ws)
		_, _ = db.Exec(`DELETE FROM agents WHERE workspace_id = $1`, ws)
	})
	return &memoryArchiveDBEnv{db: db, ws: ws, project: project, h: h, repo: repo}
}

// agent inserts an agent with a directory role; member adds it to the project.
func (env *memoryArchiveDBEnv) agent(t *testing.T, role string, member bool) uuid.UUID {
	t.Helper()
	id := newReservedTagTestAgent(t, env.db, env.ws)
	_, err := env.db.Exec(`UPDATE agents SET role=$2 WHERE id=$1`, id, role)
	require.NoError(t, err)
	if member {
		_, err = env.db.Exec(`INSERT INTO project_members (id,project_id,workspace_id,agent_id) VALUES ($1,$2,$3,$4)`, uuid.New(), env.project, env.ws, id)
		require.NoError(t, err)
	}
	return id
}

func (env *memoryArchiveDBEnv) do(t *testing.T, as uuid.UUID, method, path string, body any) (code int, out map[string]any) {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		require.NoError(t, err)
	}
	return env.doWith(t, func(c echo.Context) { asAgent(c, as, env.ws) }, method, path, raw)
}

// doWith serves one request through the production route chain with the given
// authentication and a raw body.
func (env *memoryArchiveDBEnv) doWith(t *testing.T, auth func(echo.Context), method, path string, body []byte) (code int, out map[string]any) {
	t.Helper()
	e := echo.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error { auth(c); return next(c) }
	})
	e.Use(mw.RequireTaskProjectMember(env.db))
	e.POST("/api/v1/memories", env.h.Remember)
	e.POST("/api/v1/memories/:id/archive", env.h.Archive)
	e.POST("/api/v1/memories/:id/unarchive", env.h.Unarchive)
	e.GET("/api/v1/memories/by-key", env.h.ByKey)
	e.GET("/api/v1/memories/search", env.h.Search)
	e.GET("/api/v1/memories/recall-final/v1", env.h.FinalRecall)
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return rec.Code, out
}

func (env *memoryArchiveDBEnv) remember(t *testing.T, as uuid.UUID, key string, scope domain.MemoryScope, expiresAt string) (code int, out map[string]any) {
	t.Helper()
	body := map[string]any{"key": key, "content": "verdigris lantern note for " + key, "scope": scope, "workspace_id": env.ws}
	if scope == domain.ScopeProject {
		body["project_id"] = env.project
	}
	if expiresAt != "" {
		body["expires_at"] = expiresAt
	}
	return env.do(t, as, http.MethodPost, "/api/v1/memories", body)
}

func (env *memoryArchiveDBEnv) setArchived(t *testing.T, as, id uuid.UUID, archived bool, reason string) (code int, out map[string]any) {
	t.Helper()
	verb := "archive"
	if !archived {
		verb = "unarchive"
	}
	return env.do(t, as, http.MethodPost, "/api/v1/memories/"+id.String()+"/"+verb, map[string]any{"reason": reason})
}

// archiveKeysOf reads the keys from any of the three read responses: by-key and the
// legacy search answer with a list, final recall with {items}.
func archiveKeysOf(t *testing.T, body map[string]any) []string {
	t.Helper()
	var list []any
	switch {
	case body["items"] != nil:
		list, _ = body["items"].([]any)
	case body["data"] != nil:
		list, _ = body["data"].([]any)
	case body["memories"] != nil:
		list, _ = body["memories"].([]any)
	case body["results"] != nil:
		list, _ = body["results"].([]any)
	}
	keys := []string{}
	for _, raw := range list {
		row, _ := raw.(map[string]any)
		if m, ok := row["memory"].(map[string]any); ok {
			row = m
		}
		if k, ok := row["key"].(string); ok {
			keys = append(keys, k)
		}
	}
	return keys
}

// readPaths reads key through every path get_memory and recall use, with the
// given extra query flags, and returns which paths saw it.
func (env *memoryArchiveDBEnv) readPaths(t *testing.T, as uuid.UUID, key string, flags url.Values) map[string]bool {
	t.Helper()
	seen := map[string]bool{}
	q := url.Values{"key": {key}, "workspace_id": {env.ws.String()}}
	for k, v := range flags {
		q[k] = v
	}
	code, body := env.do(t, as, http.MethodGet, "/api/v1/memories/by-key?"+q.Encode(), nil)
	require.Equal(t, http.StatusOK, code, body)
	seen["by-key"] = archiveContains(archiveKeysOf(t, body), key)

	s := url.Values{"q": {"verdigris lantern"}, "workspace_id": {env.ws.String()}, "limit": {"50"}}
	for k, v := range flags {
		s[k] = v
	}
	code, body = env.do(t, as, http.MethodGet, "/api/v1/memories/search?"+s.Encode(), nil)
	require.Equal(t, http.StatusOK, code, body)
	seen["search"] = archiveContains(archiveKeysOf(t, body), key)

	code, body = env.do(t, as, http.MethodGet, "/api/v1/memories/recall-final/v1?"+s.Encode(), nil)
	require.Equal(t, http.StatusOK, code, body)
	seen["final"] = archiveContains(archiveKeysOf(t, body), key)
	return seen
}

func archiveContains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func memoryIDOf(t *testing.T, body map[string]any) uuid.UUID {
	t.Helper()
	raw, ok := body["id"].(string)
	if !ok {
		if m, isMap := body["memory"].(map[string]any); isMap {
			raw, ok = m["id"].(string)
		}
	}
	require.True(t, ok, "no memory id in %v", body)
	id, err := uuid.Parse(raw)
	require.NoError(t, err)
	return id
}

// TestMemoryArchiveDB_ProjectEntryOfAnotherAgent is the card's scenario: a
// project member who is not the author archives a stale entry; plain recall and
// get_memory stop returning it, include_archived=true still does; the reason is
// on a revision; a repeat remember does not resurrect it; unarchive brings it back.
func TestMemoryArchiveDB_ProjectEntryOfAnotherAgent(t *testing.T) {
	env := newMemoryArchiveDBEnv(t)
	author := env.agent(t, "developer", true)
	curator := env.agent(t, "developer", true)
	const key = "stale-directive-f19259ad"

	code, body := env.remember(t, author, key, domain.ScopeProject, "")
	require.Equal(t, http.StatusOK, code, body)
	id := memoryIDOf(t, body)

	require.Equal(t, map[string]bool{"by-key": true, "search": true, "final": true}, env.readPaths(t, curator, key, nil), "live entry is visible everywhere")

	code, body = env.setArchived(t, curator, id, true, "MR merged long ago, directive no longer applies")
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, true, body["archived"])
	require.Equal(t, true, body["changed"])
	require.NotNil(t, body["revision_id"])

	var rev struct {
		Action  string    `db:"action"`
		Reason  string    `db:"reason"`
		AgentID uuid.UUID `db:"actor_agent_id"`
		Version int       `db:"version"`
	}
	require.NoError(t, env.db.Get(&rev, `SELECT action, reason, actor_agent_id, version FROM memory_revisions WHERE memory_id=$1 ORDER BY version DESC LIMIT 1`, id))
	require.Equal(t, "archived", rev.Action)
	require.Equal(t, "MR merged long ago, directive no longer applies", rev.Reason)
	require.Equal(t, curator, rev.AgentID, "the revision names who archived, not the author")
	require.EqualValues(t, body["revision_version"], rev.Version)

	require.Equal(t, map[string]bool{"by-key": false, "search": false, "final": false}, env.readPaths(t, curator, key, nil), "archived entry leaves plain recall and get_memory")
	require.Equal(t, map[string]bool{"by-key": true, "search": true, "final": true}, env.readPaths(t, curator, key, url.Values{"include_archived": {"true"}}), "include_archived=true returns it on every path")

	// Idempotent: archiving an archived entry changes nothing and writes no revision.
	code, body = env.setArchived(t, curator, id, true, "again")
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, false, body["changed"])
	var revisions int
	require.NoError(t, env.db.Get(&revisions, `SELECT count(*) FROM memory_revisions WHERE memory_id=$1 AND action IN ('archived','unarchived')`, id))
	require.Equal(t, 1, revisions)

	// A repeat remember on the key updates the archived row and keeps it archived.
	code, body = env.remember(t, author, key, domain.ScopeProject, "")
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, id, memoryIDOf(t, body), "remember must update the archived row, not create a live duplicate")
	require.Equal(t, true, body["archived"])
	require.Contains(t, body["hint"], "archive_memory")
	var rows int
	require.NoError(t, env.db.Get(&rows, `SELECT count(*) FROM memories WHERE workspace_id=$1 AND key=$2`, env.ws, key))
	require.Equal(t, 1, rows)
	require.Equal(t, map[string]bool{"by-key": false, "search": false, "final": false}, env.readPaths(t, curator, key, nil))

	code, body = env.setArchived(t, curator, id, false, "still needed after all")
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, false, body["archived"])
	require.Equal(t, true, body["changed"])
	require.NoError(t, env.db.Get(&rev, `SELECT action, reason, actor_agent_id, version FROM memory_revisions WHERE memory_id=$1 ORDER BY version DESC LIMIT 1`, id))
	require.Equal(t, "unarchived", rev.Action)
	require.Equal(t, "still needed after all", rev.Reason)
	require.Equal(t, map[string]bool{"by-key": true, "search": true, "final": true}, env.readPaths(t, curator, key, nil), "unarchived entry is back in recall")
}

func TestMemoryArchiveDB_Permissions(t *testing.T) {
	env := newMemoryArchiveDBEnv(t)
	author := env.agent(t, "developer", true)
	member := env.agent(t, "developer", true)
	outsider := env.agent(t, "developer", false)
	lead := env.agent(t, "lead, product", false)

	_, body := env.remember(t, author, "perm-project", domain.ScopeProject, "")
	projectMem := memoryIDOf(t, body)
	_, body = env.remember(t, author, "perm-agent", domain.ScopeAgent, "")
	agentMem := memoryIDOf(t, body)
	_, body = env.remember(t, author, "perm-workspace", domain.ScopeWorkspace, "")
	workspaceMem := memoryIDOf(t, body)

	cases := []struct {
		name   string
		as     uuid.UUID
		id     uuid.UUID
		reason string
		want   int
	}{
		{"reason is required", member, projectMem, "  ", http.StatusBadRequest},
		{"reason is bounded", member, projectMem, strings.Repeat("я", domain.MemoryArchiveReasonMaxLen+1), http.StatusBadRequest},
		{"project: non-member is refused", outsider, projectMem, "stale", http.StatusForbidden},
		{"agent scope: another agent is refused", member, agentMem, "stale", http.StatusForbidden},
		{"agent scope: a lead is refused too", lead, agentMem, "stale", http.StatusForbidden},
		{"workspace: a developer is refused", member, workspaceMem, "stale", http.StatusForbidden},
		{"unknown memory is 404", member, uuid.New(), "stale", http.StatusNotFound},
		{"project: a member who is not the author may", member, projectMem, "stale", http.StatusOK},
		{"agent scope: the owner may", author, agentMem, "stale", http.StatusOK},
		{"workspace: a lead may", lead, workspaceMem, "stale", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, resp := env.setArchived(t, tc.as, tc.id, true, tc.reason)
			require.Equal(t, tc.want, got, resp)
		})
	}

	// Another workspace's agent cannot even learn the memory exists.
	foreign := newMemoryArchiveDBEnv(t)
	stranger := foreign.agent(t, "lead", false)
	code, body := foreign.setArchived(t, stranger, workspaceMem, false, "mine now")
	require.Equal(t, http.StatusNotFound, code, body)
}

// TestMemoryExpiredDB_ReadableUntilCleanup: include_expired must return an entry
// whose expires_at has passed, on get_memory's path as on recall's, until the
// background cleanup removes it after the grace period. Before this change the
// legacy search dropped the flag and CleanExpired deleted at expiry.
func TestMemoryExpiredDB_ReadableUntilCleanup(t *testing.T) {
	env := newMemoryArchiveDBEnv(t)
	author := env.agent(t, "developer", true)
	const key = "expired-two-minutes-ago"

	code, body := env.remember(t, author, key, domain.ScopeProject, "")
	require.Equal(t, http.StatusOK, code, body)
	id := memoryIDOf(t, body)
	_, err := env.db.Exec(`UPDATE memories SET expires_at = now() - interval '2 minutes' WHERE id=$1`, id)
	require.NoError(t, err)

	require.Equal(t, map[string]bool{"by-key": false, "search": false, "final": false}, env.readPaths(t, author, key, nil), "an expired entry is hidden by default")
	require.Equal(t, map[string]bool{"by-key": true, "search": true, "final": true}, env.readPaths(t, author, key, url.Values{"include_expired": {"true"}}), "include_expired=true reads it until cleanup")

	// Cleanup keeps it inside the grace period and deletes it after.
	_, body = env.remember(t, author, "expired-long-ago", domain.ScopeProject, "")
	old := memoryIDOf(t, body)
	_, err = env.db.Exec(`UPDATE memories SET expires_at = now() - make_interval(hours => $2) WHERE id=$1`, old, int((domain.ExpiredMemoryGracePeriod + 24*time.Hour).Hours()))
	require.NoError(t, err)

	_, err = env.repo.CleanExpired(context.Background())
	require.NoError(t, err)
	var left []uuid.UUID
	require.NoError(t, env.db.Select(&left, `SELECT id FROM memories WHERE id = ANY(ARRAY[$1,$2]::uuid[])`, id, old))
	require.Equal(t, []uuid.UUID{id}, left, "expired 2 min ago survives cleanup; expired past the grace period is deleted")
}

// user inserts a user with this workspace role; "" leaves it outside the workspace.
func (env *memoryArchiveDBEnv) user(t *testing.T, role string) func(echo.Context) {
	t.Helper()
	_, auth := env.userWithID(t, role)
	return auth
}

func (env *memoryArchiveDBEnv) userWithID(t *testing.T, role string) (id uuid.UUID, auth func(echo.Context)) {
	t.Helper()
	id = uuid.New()
	short := strings.ReplaceAll(id.String(), "-", "")[:12]
	_, err := env.db.Exec(`INSERT INTO users (id, email, password_hash, display_name, username) VALUES ($1, $2, 'x', 'Archive Test User', $3)`,
		id, "archive-"+short+"@example.invalid", "archive-"+short)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = env.db.Exec(`DELETE FROM project_members WHERE user_id = $1`, id)
		_, _ = env.db.Exec(`DELETE FROM workspace_members WHERE user_id = $1`, id)
		_, _ = env.db.Exec(`DELETE FROM users WHERE id = $1`, id)
	})
	if role != "" {
		_, err = env.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1, $2, $3, $4)`, uuid.New(), env.ws, id, role)
		require.NoError(t, err)
	}
	return id, func(c echo.Context) { asUser(c, id) }
}

// A user token carries no workspace: archive resolves it from the memory, and
// the user's workspace role decides, like a write.
func TestMemoryArchiveDB_UserCallers(t *testing.T) {
	env := newMemoryArchiveDBEnv(t)
	author := env.agent(t, "developer", true)
	key := "archive-user-" + uuid.New().String()[:8]
	code, body := env.remember(t, author, key, domain.ScopeProject, "")
	require.Equal(t, http.StatusOK, code, body)
	id := memoryIDOf(t, body)
	path := "/api/v1/memories/" + id.String()
	reason := []byte(`{"reason":"merged long ago"}`)

	code, body = env.doWith(t, env.user(t, ""), http.MethodPost, path+"/archive", reason)
	require.Equal(t, http.StatusNotFound, code, "outside the workspace: %v", body)

	code, body = env.doWith(t, env.user(t, "viewer"), http.MethodPost, path+"/archive", reason)
	require.Equal(t, http.StatusForbidden, code, "a viewer cannot write: %v", body)

	admin := env.user(t, "admin")
	code, body = env.doWith(t, admin, http.MethodPost, path+"/archive", reason)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, true, body["archived"])
	code, body = env.doWith(t, admin, http.MethodPost, path+"/unarchive", reason)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, false, body["archived"])

	code, body = env.doWith(t, func(c echo.Context) { c.Set("auth_type", "user") }, http.MethodPost, path+"/archive", reason)
	require.Equal(t, http.StatusNotFound, code, "no user id: %v", body)
}

func TestMemoryArchiveDB_RejectsMalformedRequests(t *testing.T) {
	env := newMemoryArchiveDBEnv(t)
	as := env.agent(t, "developer", true)
	auth := func(c echo.Context) { asAgent(c, as, env.ws) }
	ws := "workspace_id=" + env.ws.String()

	cases := []struct {
		name, method, path string
		body               []byte
		want               int
	}{
		{"archive: bad id", http.MethodPost, "/api/v1/memories/not-a-uuid/archive", []byte(`{"reason":"x"}`), http.StatusBadRequest},
		{"archive: bad body", http.MethodPost, "/api/v1/memories/" + uuid.New().String() + "/archive", []byte(`{"reason":`), http.StatusBadRequest},
		{"archive: unknown id", http.MethodPost, "/api/v1/memories/" + uuid.New().String() + "/unarchive", []byte(`{"reason":"x"}`), http.StatusNotFound},
		{"by-key: no key", http.MethodGet, "/api/v1/memories/by-key?" + ws, nil, http.StatusBadRequest},
		{"by-key: bad scope", http.MethodGet, "/api/v1/memories/by-key?key=k&scope=team&" + ws, nil, http.StatusBadRequest},
		{"by-key: bad project", http.MethodGet, "/api/v1/memories/by-key?key=k&project_id=nope&" + ws, nil, http.StatusBadRequest},
		{"by-key: bad include_archived", http.MethodGet, "/api/v1/memories/by-key?key=k&include_archived=maybe&" + ws, nil, http.StatusBadRequest},
		{"by-key: bad include_expired", http.MethodGet, "/api/v1/memories/by-key?key=k&include_expired=maybe&" + ws, nil, http.StatusBadRequest},
		{"by-key: limit too big", http.MethodGet, "/api/v1/memories/by-key?key=k&limit=101&" + ws, nil, http.StatusBadRequest},
		{"by-key: foreign workspace", http.MethodGet, "/api/v1/memories/by-key?key=k&workspace_id=" + uuid.New().String(), nil, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := env.doWith(t, auth, tc.method, tc.path, tc.body)
			require.Equal(t, tc.want, code, body)
		})
	}

	// The valid filters reach the read: scope, project and limit narrow it.
	key := "by-key-filters-" + uuid.New().String()[:8]
	code, body := env.remember(t, as, key, domain.ScopeProject, "")
	require.Equal(t, http.StatusOK, code, body)
	q := url.Values{"key": {key}, "workspace_id": {env.ws.String()}, "scope": {"project"}, "project_id": {env.project.String()},
		"limit": {"5"}, "include_archived": {"false"}, "include_expired": {"false"}}
	code, body = env.doWith(t, auth, http.MethodGet, "/api/v1/memories/by-key?"+q.Encode(), nil)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, []string{key}, archiveKeysOf(t, body))
	q.Set("scope", "workspace")
	code, body = env.doWith(t, auth, http.MethodGet, "/api/v1/memories/by-key?"+q.Encode(), nil)
	require.Equal(t, http.StatusOK, code, body)
	require.Empty(t, archiveKeysOf(t, body))
}

// joinProject adds a user or agent to the test project with a project role.
func (env *memoryArchiveDBEnv) joinProject(t *testing.T, userID, agentID *uuid.UUID, role string) {
	t.Helper()
	_, err := env.db.Exec(`INSERT INTO project_members (id, project_id, workspace_id, user_id, agent_id, role) VALUES ($1, $2, $3, $4, $5, $6)`,
		uuid.New(), env.project, env.ws, userID, agentID, role)
	require.NoError(t, err)
}

// Project access is membership; archiving is a write, so a project viewer (agent
// or user) is refused, while a project member user archives and is recorded as
// the revision's actor.
func TestMemoryArchiveDB_ProjectViewerIsRefused(t *testing.T) {
	env := newMemoryArchiveDBEnv(t)
	author := env.agent(t, "developer", true)
	code, body := env.remember(t, author, "archive-viewer-"+uuid.New().String()[:8], domain.ScopeProject, "")
	require.Equal(t, http.StatusOK, code, body)
	id := memoryIDOf(t, body)
	path := "/api/v1/memories/" + id.String() + "/archive"
	reason := []byte(`{"reason":"merged long ago"}`)

	viewerAgent := env.agent(t, "developer", false)
	env.joinProject(t, nil, &viewerAgent, "viewer")
	code, body = env.do(t, viewerAgent, http.MethodPost, path, map[string]any{"reason": "merged long ago"})
	require.Equal(t, http.StatusForbidden, code, "project viewer agent: %v", body)

	viewerID, viewerUser := env.userWithID(t, "member")
	env.joinProject(t, &viewerID, nil, "viewer")
	code, body = env.doWith(t, viewerUser, http.MethodPost, path, reason)
	require.Equal(t, http.StatusForbidden, code, "project viewer user: %v", body)

	memberID, memberUser := env.userWithID(t, "member")
	env.joinProject(t, &memberID, nil, "member")
	code, body = env.doWith(t, memberUser, http.MethodPost, path, reason)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, true, body["archived"])

	var actorUser, actorAgent uuid.NullUUID
	require.NoError(t, env.db.QueryRow(`SELECT actor_user_id, actor_agent_id FROM memory_revisions WHERE id = $1`, body["revision_id"]).Scan(&actorUser, &actorAgent))
	require.Equal(t, memberID, actorUser.UUID, "the user is the recorded actor")
	require.False(t, actorAgent.Valid)

	// No role lookup wired: refuse rather than fall back to membership.
	env.h.SetProjectRoleLookup(nil)
	code, body = env.doWith(t, memberUser, http.MethodPost, "/api/v1/memories/"+id.String()+"/unarchive", reason)
	require.Equal(t, http.StatusForbidden, code, body)
}
