package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// Bulk and service reads must not stamp memories.last_accessed_at. A paged
// GET /memories walk can otherwise mark the entire workspace as recently read,
// hiding stale entries from review triage. Only explicit recall/get_memory
// reads count as access.

func (env *finalRecallDBEnv) serve(t *testing.T, path string, params url.Values) (status int, body []byte) {
	t.Helper()
	e := echo.New()
	// mock: external boundary — authenticated agent context; requireWorkspaceID
	// still enforces the real tenant check. No internal handler/service is mocked.
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error { asAgent(c, env.agent, env.ws); return next(c) }
	})
	e.GET("/api/v1/memories", env.h.List)
	e.GET("/api/v1/memories/export", env.h.ExportMemories)
	e.GET("/api/v1/memories/search", env.h.Search)
	e.GET("/api/v1/memories/by-key", env.h.ByKey)
	e.GET("/api/v1/memories/:id", env.h.GetByID)
	e.GET("/api/v1/memories/:id/related", env.h.FindRelated)
	e.GET("/api/v1/projects/:proj_id/knowledge", env.h.GetProjectKnowledge)
	target := path
	if len(params) > 0 {
		target += "?" + params.Encode()
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, http.NoBody))
	return rec.Code, rec.Body.Bytes()
}

func (env *finalRecallDBEnv) touchedIDs(t *testing.T) []uuid.UUID {
	t.Helper()
	ids := []uuid.UUID{}
	require.NoError(t, env.db.Select(&ids, `SELECT id FROM memories WHERE workspace_id=$1 AND last_accessed_at IS NOT NULL`, env.ws))
	return ids
}

// sweepAllMemories exercises the bulk path with multiple pages, including
// archived+expired entries when requested. It returns the rows actually seen.
func (env *finalRecallDBEnv) sweepAllMemories(t *testing.T, extra url.Values) int {
	t.Helper()
	seen := 0
	for offset := 0; ; {
		params := url.Values{"limit": {"5"}, "offset": {strconv.Itoa(offset)}}
		for k, v := range extra {
			params[k] = v
		}
		code, body := env.serve(t, "/api/v1/memories", params)
		require.Equal(t, http.StatusOK, code, string(body))
		var page struct {
			Items []json.RawMessage `json:"items"`
			Total int               `json:"total"`
		}
		require.NoError(t, json.Unmarshal(body, &page))
		seen += len(page.Items)
		offset += len(page.Items)
		if len(page.Items) == 0 || offset >= page.Total {
			return seen
		}
	}
}

func TestBulkReadsDB_DoNotTouchLastAccessedAt(t *testing.T) {
	for _, read := range []string{"paged list", "export", "project knowledge", "related"} {
		t.Run(read, func(t *testing.T) {
			env := newFinalRecallDBEnv(t)
			_, err := env.db.Exec(`UPDATE memories SET archived=true WHERE id=$1`, env.ids[len(env.ids)-1])
			require.NoError(t, err)
			_, err = env.db.Exec(`UPDATE memories SET expires_at=now()-interval '1 day' WHERE id=$1`, env.ids[len(env.ids)-2])
			require.NoError(t, err)
			// A service read must preserve both NULL and an existing, stale stamp.
			_, err = env.db.Exec(`UPDATE memories SET last_accessed_at=now()-interval '40 days' WHERE id=$1`, env.ids[0])
			require.NoError(t, err)
			var before string
			require.NoError(t, env.db.Get(&before, `SELECT last_accessed_at::text FROM memories WHERE id=$1`, env.ids[0]))

			switch read {
			case "paged list":
				require.Equal(t, len(env.ids)-2, env.sweepAllMemories(t, nil))
				require.Equal(t, len(env.ids), env.sweepAllMemories(t, url.Values{"include_archived": {"true"}, "include_expired": {"true"}}))
			case "export":
				code, body := env.serve(t, "/api/v1/memories/export", nil)
				require.Equal(t, http.StatusOK, code, string(body))
				require.Contains(t, string(body), "cobalt-base-00")
			case "project knowledge":
				code, body := env.serve(t, "/api/v1/projects/"+uuid.NewString()+"/knowledge", url.Values{"limit": {"500"}})
				require.Equal(t, http.StatusOK, code, string(body))
				var knowledge struct {
					WorkspaceTotal int `json:"workspace_total"`
				}
				require.NoError(t, json.Unmarshal(body, &knowledge))
				require.Equal(t, len(env.ids)-2, knowledge.WorkspaceTotal)
			case "related":
				code, body := env.serve(t, "/api/v1/memories/"+env.ids[0].String()+"/related", nil)
				require.Equal(t, http.StatusOK, code, string(body))
				var related struct {
					Items []json.RawMessage `json:"items"`
				}
				require.NoError(t, json.Unmarshal(body, &related))
				require.NotEmpty(t, related.Items)
			}
			var after string
			require.NoError(t, env.db.Get(&after, `SELECT last_accessed_at::text FROM memories WHERE id=$1`, env.ids[0]))
			require.Equal(t, before, after, "service read must preserve the stale stamp")
			require.ElementsMatch(t, env.ids[:1], env.touchedIDs(t), "service read must preserve NULL stamps")
		})
	}
}

func TestAgentReadsDB_TouchLastAccessedAt(t *testing.T) {
	t.Run("recall touches exactly the returned rows", func(t *testing.T) {
		env := newFinalRecallDBEnv(t)
		code, rows, _ := env.request(t, "cobalt checkout", 3, 0, false, true)
		require.Equal(t, http.StatusOK, code)
		require.Len(t, rows, 3)
		want := []uuid.UUID{}
		for _, r := range rows {
			want = append(want, r.ID)
		}
		require.ElementsMatch(t, want, env.touchedIDs(t))
	})

	t.Run("get_memory by key touches the entry at the key", func(t *testing.T) {
		env := newFinalRecallDBEnv(t)
		code, body := env.serve(t, "/api/v1/memories/by-key", url.Values{"key": {"cobalt-base-03"}})
		require.Equal(t, http.StatusOK, code, string(body))
		require.ElementsMatch(t, []uuid.UUID{env.ids[3]}, env.touchedIDs(t))
	})

	t.Run("get_memory by key on a missing key touches nothing", func(t *testing.T) {
		env := newFinalRecallDBEnv(t)
		code, body := env.serve(t, "/api/v1/memories/by-key", url.Values{"key": {"no-such-key"}})
		require.Equal(t, http.StatusOK, code, string(body))
		require.Empty(t, env.touchedIDs(t))
	})

	for _, state := range []string{"archived", "expired"} {
		t.Run("get_memory excludes "+state+" unless requested", func(t *testing.T) {
			env := newFinalRecallDBEnv(t)
			if state == "archived" {
				_, err := env.db.Exec(`UPDATE memories SET archived=true WHERE id=$1`, env.ids[3])
				require.NoError(t, err)
			} else {
				_, err := env.db.Exec(`UPDATE memories SET expires_at=now()-interval '1 day' WHERE id=$1`, env.ids[3])
				require.NoError(t, err)
			}
			params := url.Values{"key": {"cobalt-base-03"}}
			code, body := env.serve(t, "/api/v1/memories/by-key", params)
			require.Equal(t, http.StatusOK, code, string(body))
			require.Empty(t, env.touchedIDs(t))
			params.Set("include_"+state, "true")
			code, body = env.serve(t, "/api/v1/memories/by-key", params)
			require.Equal(t, http.StatusOK, code, string(body))
			require.Contains(t, string(body), "cobalt-base-03")
			require.ElementsMatch(t, env.ids[3:4], env.touchedIDs(t))
		})
	}

	t.Run("GET /memories/:id missing entry touches nothing", func(t *testing.T) {
		env := newFinalRecallDBEnv(t)
		code, _ := env.serve(t, "/api/v1/memories/"+uuid.NewString(), nil)
		require.Equal(t, http.StatusNotFound, code)
		require.Empty(t, env.touchedIDs(t))
	})

	t.Run("an old stamp is refreshed", func(t *testing.T) {
		env := newFinalRecallDBEnv(t)
		_, err := env.db.Exec(`UPDATE memories SET last_accessed_at=now()-interval '2 hours' WHERE id=$1`, env.ids[0])
		require.NoError(t, err)
		code, body := env.serve(t, "/api/v1/memories/"+env.ids[0].String(), nil)
		require.Equal(t, http.StatusOK, code, string(body))
		var after time.Time
		require.NoError(t, env.db.Get(&after, `SELECT last_accessed_at FROM memories WHERE id=$1`, env.ids[0]))
		require.WithinDuration(t, time.Now(), after, 5*time.Second)
	})

	t.Run("GET /memories/:id touches that memory", func(t *testing.T) {
		env := newFinalRecallDBEnv(t)
		code, body := env.serve(t, "/api/v1/memories/"+env.ids[5].String(), nil)
		require.Equal(t, http.StatusOK, code, string(body))
		require.ElementsMatch(t, []uuid.UUID{env.ids[5]}, env.touchedIDs(t))
	})

	t.Run("GET /memories/:id of a foreign workspace touches nothing", func(t *testing.T) {
		env := newFinalRecallDBEnv(t)
		other := newReservedTagTestWorkspace(t, env.db, false)
		_, err := env.db.Exec(`UPDATE memories SET workspace_id=$1 WHERE id=$2`, other, env.ids[6])
		require.NoError(t, err)
		code, _ := env.serve(t, "/api/v1/memories/"+env.ids[6].String(), nil)
		require.Equal(t, http.StatusNotFound, code)
		var stamped int
		require.NoError(t, env.db.Get(&stamped, `SELECT count(*) FROM memories WHERE id=$1 AND last_accessed_at IS NOT NULL`, env.ids[6]))
		require.Zero(t, stamped, "a 404 read must not count as access")
	})

	t.Run("the one-hour window still holds", func(t *testing.T) {
		env := newFinalRecallDBEnv(t)
		_, err := env.db.Exec(`UPDATE memories SET last_accessed_at = now() - interval '10 minutes' WHERE id = ANY($1)`, pq.Array(env.ids[:1]))
		require.NoError(t, err)
		var before string
		require.NoError(t, env.db.Get(&before, `SELECT last_accessed_at::text FROM memories WHERE id=$1`, env.ids[0]))
		code, body := env.serve(t, "/api/v1/memories/"+env.ids[0].String(), nil)
		require.Equal(t, http.StatusOK, code, string(body))
		var after string
		require.NoError(t, env.db.Get(&after, `SELECT last_accessed_at::text FROM memories WHERE id=$1`, env.ids[0]))
		require.Equal(t, before, after)
	})
}
