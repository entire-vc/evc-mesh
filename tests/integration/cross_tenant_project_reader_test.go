//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Project reader (docs/project-reader.md), the AC2 matrix of the source card
// against the running API: a reader bound to one project reads that project's
// comments and nothing else — no foreign task, comment, document or memory, no
// workspace memory, no write. The same foreign ids answer 200 to an ordinary
// member agent of the workspace (positive control), and a plain viewer agent
// with the same project membership still reads the foreign memory by id —
// the leak this profile exists to close (red control, unchanged on purpose:
// the plain viewer is not narrowed by this change).

// keyDo sends one request with only X-Agent-Key (or nothing): a fresh client,
// no cookie jar, no bearer token, so the agent key is the only identity.
func keyDo(t *testing.T, env *TestEnv, method, path, key string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, env.BaseURL+path, r)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("X-Agent-Key", key)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func TestCrossTenant_ProjectReaderScope(t *testing.T) {
	owner := NewTestEnv(t)
	t.Cleanup(func() { owner.Cleanup(t) })
	owner.Register(t, uniqueEmail("reader-owner"), "TestPass123", "Reader Owner")
	wsID := firstWorkspaceID(t, owner)
	tn := &tenant{env: owner, wsID: wsID}

	ch := tn.createID(t, http.MethodPost, "/api/v1/workspaces/"+wsID+"/projects", map[string]any{"name": "reader home project"})
	foreign := tn.createID(t, http.MethodPost, "/api/v1/workspaces/"+wsID+"/projects", map[string]any{"name": "reader foreign project"})
	owner.OnCleanup(func() {
		ctx := context.Background()
		for _, p := range []string{ch, foreign} {
			_, _ = owner.DB.ExecContext(ctx, "DELETE FROM memories WHERE project_id = $1", p)
			_, _ = owner.DB.ExecContext(ctx, "DELETE FROM comments WHERE task_id IN (SELECT id FROM tasks WHERE project_id = $1)", p)
			_, _ = owner.DB.ExecContext(ctx, "DELETE FROM documents WHERE project_id = $1", p)
			_, _ = owner.DB.ExecContext(ctx, "DELETE FROM tasks WHERE project_id = $1", p)
			_, _ = owner.DB.ExecContext(ctx, "DELETE FROM project_members WHERE project_id = $1", p)
		}
	})
	chTask := tn.createID(t, http.MethodPost, "/api/v1/projects/"+ch+"/tasks", map[string]any{"title": "consent source"})
	fTask := tn.createID(t, http.MethodPost, "/api/v1/projects/"+foreign+"/tasks", map[string]any{"title": "foreign confidential"})
	owner.AddComment(t, chTask, "canonical consent comment")
	owner.AddComment(t, fTask, "foreign confidential comment")
	// A document needs object storage; without it the document rows of the
	// matrix are skipped, not the whole test.
	fDoc := ""
	if resp := owner.Post(t, "/api/v1/projects/"+foreign+"/documents", map[string]any{"title": "foreign doc", "body": documentBody}); resp.StatusCode == http.StatusCreated {
		var d map[string]any
		owner.DecodeJSON(t, resp, &d)
		fDoc, _ = d["id"].(string)
	} else {
		t.Logf("document fixture unavailable (%d %s): document rows skipped", resp.StatusCode, owner.ReadBody(t, resp))
	}

	// Positive control: an ordinary member agent of the workspace (mirrors
	// the positive control role in a live run). It also writes the memories.
	memberID, memberKey := owner.CreateAgent(t, wsID, fmt.Sprintf("reader-control-%d", time.Now().UnixNano()))
	// Agents reach a project only through its membership; the control is a
	// member of the foreign project, so its 200s prove the ids are readable.
	resp := owner.Post(t, "/api/v1/projects/"+foreign+"/members/agents", map[string]any{"agent_id": memberID, "role": "member"})
	require.Less(t, resp.StatusCode, 300, "control membership: %s", owner.ReadBody(t, resp))
	remember := func(body map[string]any) string {
		code, raw := keyDo(t, owner, http.MethodPost, "/api/v1/memories", memberKey, body)
		require.Equal(t, http.StatusOK, code, "remember: %s", raw)
		var out struct {
			Memory struct {
				ID string `json:"id"`
			} `json:"memory"`
		}
		require.NoError(t, json.Unmarshal(raw, &out))
		require.NotEmpty(t, out.Memory.ID, "remember returned no id: %s", raw)
		return out.Memory.ID
	}
	fMem := remember(map[string]any{"workspace_id": wsID, "project_id": foreign, "scope": "project",
		"key": fmt.Sprintf("reader-foreign-%d", time.Now().UnixNano()), "content": "foreign project memory"})
	wsMemKey := fmt.Sprintf("reader-ws-%d", time.Now().UnixNano())
	wsMem := remember(map[string]any{"workspace_id": wsID, "scope": "workspace", "key": wsMemKey, "content": "workspace memory"})
	owner.OnCleanup(func() {
		_, _ = owner.DB.ExecContext(context.Background(), "DELETE FROM memories WHERE id = $1 OR id = $2", fMem, wsMem)
	})

	foreignReads := map[string]string{
		"foreign_task":       "/api/v1/tasks/" + fTask,
		"foreign_comments":   "/api/v1/tasks/" + fTask + "/comments?page=1",
		"foreign_document":   "/api/v1/documents/" + fDoc,
		"foreign_documents":  "/api/v1/projects/" + foreign + "/documents?limit=1",
		"foreign_memory":     "/api/v1/memories/" + fMem,
		"foreign_memories":   "/api/v1/memories?project_id=" + foreign + "&scope=project&limit=1",
		"workspace_memories": "/api/v1/memories?scope=workspace&limit=1",
		"memories_no_scope":  "/api/v1/memories?limit=5",
		"workspace_memory":   "/api/v1/memories/" + wsMem,
		"memory_by_key":      "/api/v1/memories/by-key?key=" + url.QueryEscape(wsMemKey) + "&scope=workspace",
		"memory_search":      "/api/v1/memories/search?q=memory",
		"memory_export":      "/api/v1/memories/export",
		"own_knowledge":      "/api/v1/projects/" + ch + "/knowledge",
		"canonical_updates":  "/api/v1/canonical_updates",
	}
	if fDoc == "" {
		delete(foreignReads, "foreign_document")
	}
	for _, name := range []string{"foreign_task", "foreign_comments", "foreign_documents", "foreign_memory", "foreign_memories", "workspace_memory", "foreign_document"} {
		if _, ok := foreignReads[name]; !ok {
			continue
		}
		code, raw := keyDo(t, owner, http.MethodGet, foreignReads[name], memberKey, nil)
		require.Equal(t, http.StatusOK, code, "positive control %s: member agent got %d %s", name, code, raw)
	}

	// Issuance: an agent key cannot issue a reader; the human admin can.
	code, raw := keyDo(t, owner, http.MethodPost, "/api/v1/workspaces/"+wsID+"/project-readers", memberKey,
		map[string]any{"name": "agent-issued-reader", "project_id": ch})
	require.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, code, "an agent issued a project reader: %s", raw)

	resp = owner.Post(t, "/api/v1/workspaces/"+wsID+"/project-readers",
		map[string]any{"name": fmt.Sprintf("consent-reader-%d", time.Now().UnixNano()), "project_id": foreign + "x"})
	_ = owner.ReadBody(t, resp)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "malformed project_id must be refused")

	resp = owner.Post(t, "/api/v1/workspaces/"+wsID+"/project-readers",
		map[string]any{"name": fmt.Sprintf("consent-reader-%d", time.Now().UnixNano()), "project_id": ch})
	issuedRaw := owner.ReadBody(t, resp)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "issue reader: %s", issuedRaw)
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	var issued struct {
		Agent struct {
			ID              string          `json:"id"`
			ReaderProjectID string          `json:"reader_project_id"`
			AcceptsFrom     json.RawMessage `json:"accepts_from"`
		} `json:"agent"`
		APIKey string `json:"api_key"`
	}
	require.NoError(t, json.Unmarshal(issuedRaw, &issued))
	readerID, readerKey := issued.Agent.ID, issued.APIKey
	require.NotEmpty(t, readerKey)
	require.Equal(t, ch, issued.Agent.ReaderProjectID)
	owner.OnCleanup(func() {
		ctx := context.Background()
		_, _ = owner.DB.ExecContext(ctx, "DELETE FROM agent_workspace_grants WHERE agent_id = $1", readerID)
		_, _ = owner.DB.ExecContext(ctx, "DELETE FROM project_members WHERE agent_id = $1", readerID)
		_, _ = owner.DB.ExecContext(ctx, "DELETE FROM agents WHERE id = $1", readerID)
	})
	// Re-read what was stored: one viewer grant, one viewer membership of ch only.
	var grantRole string
	require.NoError(t, owner.DB.Get(&grantRole, `SELECT role FROM agent_workspace_grants WHERE agent_id = $1`, readerID))
	assert.Equal(t, "viewer", grantRole)
	var memberships []string
	require.NoError(t, owner.DB.Select(&memberships, `SELECT project_id::text || ':' || role FROM project_members WHERE agent_id = $1`, readerID))
	assert.Equal(t, []string{ch + ":viewer"}, memberships)

	// AC2: the consumer path.
	code, raw = keyDo(t, owner, http.MethodGet, "/api/v1/tasks/"+chTask+"/comments?page=1", readerKey, nil)
	require.Equal(t, http.StatusOK, code, "reader comments: %s", raw)
	assert.Contains(t, string(raw), "canonical consent comment")
	code, _ = keyDo(t, owner, http.MethodGet, "/api/v1/tasks/"+chTask+"/comments?page=1", "", nil)
	assert.Equal(t, http.StatusUnauthorized, code, "comments without a key")
	code, raw = keyDo(t, owner, http.MethodPost, "/api/v1/tasks/"+chTask+"/comments", readerKey, map[string]any{"body": "reader write"})
	assert.Equal(t, http.StatusForbidden, code, "reader POST comment: %s", raw)
	code, raw = keyDo(t, owner, http.MethodGet, "/api/v1/agents/me", readerKey, nil)
	require.Equal(t, http.StatusOK, code, "reader /agents/me: %s", raw)
	assert.Contains(t, string(raw), `"reader_project_id":"`+ch+`"`)

	for name, path := range foreignReads {
		code, raw := keyDo(t, owner, http.MethodGet, path, readerKey, nil)
		assert.Contains(t, []int{http.StatusForbidden, http.StatusNotFound}, code, "reader %s: got %d %s", name, code, raw)
		assert.NotContains(t, string(raw), "foreign", "reader %s leaked content", name)
		assert.NotContains(t, string(raw), "workspace memory", "reader %s leaked content", name)
	}

	// Red control: a plain viewer agent with the same membership still reads the
	// foreign memory by id (the source-card leak; deliberately not changed here).
	viewerID, viewerKey := owner.CreateAgent(t, wsID, fmt.Sprintf("plain-viewer-%d", time.Now().UnixNano()))
	resp = owner.Get(t, "/api/v1/workspaces/"+wsID+"/agent-grants")
	var grants struct {
		AgentGrants []struct {
			ID      string `json:"id"`
			AgentID string `json:"agent_id"`
		} `json:"agent_grants"`
	}
	owner.DecodeJSON(t, resp, &grants)
	grantID := ""
	for _, g := range grants.AgentGrants {
		if g.AgentID == viewerID {
			grantID = g.ID
		}
	}
	require.NotEmpty(t, grantID, "home grant of the plain viewer not listed")
	resp = owner.Patch(t, "/api/v1/workspaces/"+wsID+"/agent-grants/"+grantID, map[string]any{"role": "viewer"})
	_ = owner.ReadBody(t, resp)
	require.Less(t, resp.StatusCode, 300)
	resp = owner.Post(t, "/api/v1/projects/"+ch+"/members/agents", map[string]any{"agent_id": viewerID, "role": "viewer"})
	_ = owner.ReadBody(t, resp)
	require.Less(t, resp.StatusCode, 300)
	code, raw = keyDo(t, owner, http.MethodGet, foreignReads["foreign_memory"], viewerKey, nil)
	assert.Equal(t, http.StatusOK, code, "red control: a plain viewer is expected to still read the foreign memory by id (%s)", raw)

	// Unassignable: assigning a task to the reader is refused.
	resp = owner.Patch(t, "/api/v1/tasks/"+chTask, map[string]any{"assignee_id": readerID, "assignee_type": "agent"})
	body := owner.ReadBody(t, resp)
	assert.GreaterOrEqual(t, resp.StatusCode, 400, "task assigned to a project reader: %s", body)

	// No event stream for a reader key.
	conn, wsResp, err := dialWS(t, wsURL(owner.BaseURL, "agent_key="+url.QueryEscape(readerKey)))
	if conn != nil {
		_ = conn.CloseNow()
	}
	require.Error(t, err, "reader key opened a WebSocket")
	if wsResp != nil {
		assert.Equal(t, http.StatusForbidden, wsResp.StatusCode)
	}

	// Revocation: the ordinary DELETE /agents/:id; the key stops working.
	resp = owner.Delete(t, "/api/v1/agents/"+readerID)
	_ = owner.ReadBody(t, resp)
	require.Less(t, resp.StatusCode, 300, "delete reader")
	code, _ = keyDo(t, owner, http.MethodGet, "/api/v1/tasks/"+chTask+"/comments?page=1", readerKey, nil)
	assert.Equal(t, http.StatusUnauthorized, code, "revoked reader key")
}

// Issuance into a project of another workspace writes nothing: the project is
// checked inside the issuing transaction.
func TestCrossTenant_ProjectReaderForeignWorkspaceProjectRefused(t *testing.T) {
	victim := newTenant(t, "reader-victim")
	intruder := NewTestEnv(t)
	t.Cleanup(func() { intruder.Cleanup(t) })
	intruder.Register(t, uniqueEmail("reader-intruder"), "TestPass123", "Reader Intruder")
	wsID := firstWorkspaceID(t, intruder)
	name := fmt.Sprintf("cross-reader-%d", time.Now().UnixNano())
	resp := intruder.Post(t, "/api/v1/workspaces/"+wsID+"/project-readers", map[string]any{"name": name, "project_id": victim.projectID})
	raw := intruder.ReadBody(t, resp)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "reader into a foreign workspace project: %s", raw)
	var n int
	require.NoError(t, intruder.DB.Get(&n, `SELECT count(*) FROM agents WHERE workspace_id = $1 AND name = $2`, wsID, name))
	assert.Zero(t, n, "a refused issuance left an agent row")
}
