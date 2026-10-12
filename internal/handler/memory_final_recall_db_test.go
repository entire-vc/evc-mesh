package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// mock: external boundary — the embedding provider's network response is fixed;
// handler, service, graph traversal, SQL ranking and feedback all run for real.
type finalRecallEmbedder struct{}

func (finalRecallEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{1, 0, 0}, nil
}
func (finalRecallEmbedder) EmbedBatch(context.Context, []string) ([][]float32, error) {
	return nil, nil
}
func (finalRecallEmbedder) Model() string   { return "final-recall-test" }
func (finalRecallEmbedder) Dimensions() int { return 3 }

type finalRecallDBEnv struct {
	db    *sqlx.DB
	ws    uuid.UUID
	agent uuid.UUID
	h     *MemoryHandler
	repo  *postgres.MemoryRepo
	ids   []uuid.UUID
}

func newFinalRecallDBEnv(t *testing.T) *finalRecallDBEnv {
	t.Helper()
	db := reservedTagTestDB(t)
	ws := newReservedTagTestWorkspace(t, db, false)
	repo := postgres.NewMemoryRepo(db)
	edges := postgres.NewMemoryEdgesRepo(db)
	ms := service.NewMemoryService(repo, edges, finalRecallEmbedder{})
	env := &finalRecallDBEnv{db: db, ws: ws, agent: uuid.New(), repo: repo, h: NewMemoryHandler(ms, postgres.NewWorkspaceMemberRepo(db))}
	for i := 0; i < 12; i++ {
		memory := &domain.Memory{ID: uuid.New(), WorkspaceID: ws, Key: fmt.Sprintf("cobalt-base-%02d", i), Content: "cobalt checkout base answer", Scope: domain.ScopeWorkspace, SourceType: domain.SourceAgent, Relevance: 0.4, ImportanceScore: 0.8, Tags: pq.StringArray{"allowed"}}
		require.NoError(t, repo.Upsert(context.Background(), memory, domain.MemoryWriteIntent{}))
		require.NoError(t, repo.UpdateEmbedding(context.Background(), memory.ID, []float32{1, 0, 0}, "final-recall-test", 3))
		env.ids = append(env.ids, memory.ID)
	}
	// Neither neighbour is in the strict-AND retrieval arm or the dense arm.
	// Graph must hydrate without GetByID (which itself counts as a read).
	for _, allowed := range []bool{true, false} {
		tag := "allowed"
		if !allowed {
			tag = "filtered"
		}
		neighbor := &domain.Memory{ID: uuid.New(), WorkspaceID: ws, Key: "graph-cobalt-" + tag, Content: "cobalt neighbour answer", Scope: domain.ScopeWorkspace, SourceType: domain.SourceAgent, Relevance: 0.4, ImportanceScore: 0.8, Tags: pq.StringArray{tag}}
		require.NoError(t, repo.Upsert(context.Background(), neighbor, domain.MemoryWriteIntent{}))
		env.ids = append(env.ids, neighbor.ID)
		for _, seed := range env.ids[:12] {
			require.NoError(t, edges.UpsertEdge(context.Background(), &domain.MemoryEdge{WorkspaceID: ws, MemoryFromID: seed, MemoryToID: neighbor.ID, RelationshipType: domain.EdgeRelatesTo, Weight: 0.9}))
		}
	}
	return env
}

func (env *finalRecallDBEnv) request(t *testing.T, query string, limit, offset int, graph, legacy bool) (status int, items []domain.FinalRecallItem, envelope map[string]any) {
	t.Helper()
	params := url.Values{"q": {query}, "limit": {strconv.Itoa(limit)}, "offset": {strconv.Itoa(offset)}, "graph": {strconv.FormatBool(graph)}, "tags": {"allowed"}}
	return env.requestParams(t, params, legacy)
}

func (env *finalRecallDBEnv) requestParams(t *testing.T, params url.Values, legacy bool) (status int, items []domain.FinalRecallItem, envelope map[string]any) {
	t.Helper()
	path := "/api/v1/memories/recall-final/v1"
	if legacy {
		path = "/api/v1/memories/search"
	}
	e := echo.New()
	// mock: external boundary — authenticated agent context; requireWorkspaceID
	// still enforces the real tenant check. No internal handler/service is mocked.
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error { asAgent(c, env.agent, env.ws); return next(c) }
	})
	e.GET("/api/v1/memories/recall-final/v1", env.h.FinalRecall)
	e.GET("/api/v1/memories/search", env.h.Search)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path+"?"+params.Encode(), http.NoBody))
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	var decoded struct {
		Items []domain.FinalRecallItem `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded))
	return rec.Code, decoded.Items, body
}

func (env *finalRecallDBEnv) assertFeedback(t *testing.T, rows []domain.FinalRecallItem) {
	t.Helper()
	final := []uuid.UUID{}
	for _, row := range rows {
		if row.SourceType != domain.SourceDoc {
			final = append(final, row.ID)
		}
	}
	var touched, boosted []uuid.UUID
	require.NoError(t, env.db.Select(&touched, `SELECT id FROM memories WHERE workspace_id=$1 AND last_accessed_at IS NOT NULL`, env.ws))
	require.NoError(t, env.db.Select(&boosted, `SELECT id FROM memories WHERE workspace_id=$1 AND relevance>0.401`, env.ws))
	require.ElementsMatch(t, final, touched, "touchedIDs must equal actual final unique IDs")
	require.ElementsMatch(t, final, boosted, "boostedIDs must equal actual final unique IDs")
	var unchanged int
	require.NoError(t, env.db.Get(&unchanged, `SELECT count(*) FROM memories WHERE workspace_id=$1 AND NOT(id=ANY($2)) AND last_accessed_at IS NULL AND abs(relevance-0.4)<0.001`, env.ws, pq.Array(final)))
	require.Equal(t, len(env.ids)-len(final), unchanged, "discarded/filtered/graph-seed IDs remain unchanged")
}

func TestFinalRecallDB_DocumentsKeepPermissionsAndNeverGetFeedback(t *testing.T) {
	env := newFinalRecallDBEnv(t)
	_, err := env.db.Exec(`UPDATE memories SET archived=true WHERE workspace_id=$1`, env.ws)
	require.NoError(t, err)
	_, err = env.db.Exec(`INSERT INTO agents (id,workspace_id,name,slug,api_key_hash,api_key_prefix) VALUES ($1,$2,'reader',$3,'test-hash','test-prefix')`, env.agent, env.ws, "reader-"+env.agent.String())
	require.NoError(t, err)
	chunks := postgres.NewDocumentChunkRepo(env.db)
	ms := service.NewMemoryService(env.repo, postgres.NewMemoryEdgesRepo(env.db), finalRecallEmbedder{}, service.MemoryWithDocIndex(chunks, true))
	env.h = NewMemoryHandler(ms, postgres.NewWorkspaceMemberRepo(env.db))
	visible := uuid.New()
	for _, project := range []uuid.UUID{visible, uuid.New()} {
		_, err = env.db.Exec(`INSERT INTO projects (id,workspace_id,name,slug) VALUES ($1,$2,'docs',$3)`, project, env.ws, "docs-"+project.String())
		require.NoError(t, err)
		for i := 0; i < 2; i++ {
			doc := uuid.New()
			_, err = env.db.Exec(`INSERT INTO documents (id,project_id,slug,title,storage_key,created_by,created_by_type) VALUES ($1,$2,$3,'cobalt checkout','test',$4,'agent')`, doc, project, doc.String(), env.agent)
			require.NoError(t, err)
			require.NoError(t, chunks.ReplaceChunks(context.Background(), doc, project, 1, []domain.DocumentChunk{{ChunkIdx: 0, Content: "cobalt checkout document answer"}}))
		}
	}
	params := url.Values{"q": {"cobalt checkout"}, "limit": {"5"}, "graph": {"true"}}
	code, rows, _ := env.requestParams(t, params, false)
	require.Equal(t, http.StatusOK, code)
	require.Empty(t, rows, "a reader without project membership sees no document")
	env.assertFeedback(t, rows)
	_, err = env.db.Exec(`INSERT INTO project_members (id,project_id,workspace_id,agent_id) VALUES ($1,$2,$3,$4)`, uuid.New(), visible, env.ws, env.agent)
	require.NoError(t, err)
	code, rows, _ = env.requestParams(t, params, false)
	require.Equal(t, http.StatusOK, code)
	require.Len(t, rows, 1, "the document share is capped against the final limit5, not candidate10")
	require.Equal(t, domain.SourceDoc, rows[0].SourceType)
	require.Equal(t, visible, *rows[0].ProjectID)
	require.Equal(t, "cobalt checkout document answer", rows[0].Content)
	env.assertFeedback(t, rows)
}

func TestFinalRecallDB_HandlerFeedback(t *testing.T) {
	for _, graph := range []bool{false, true} {
		t.Run(fmt.Sprintf("graph=%t", graph), func(t *testing.T) {
			env := newFinalRecallDBEnv(t)
			code, rows, body := env.request(t, "cobalt checkout", 5, 0, graph, false)
			require.Equal(t, http.StatusOK, code)
			require.Equal(t, float64(1), body["final_recall_version"])
			require.Equal(t, float64(10), body["candidate_limit"])
			require.Len(t, rows, 5)
			count := 0
			for _, row := range rows {
				if row.GraphBoost {
					count++
					require.Equal(t, domain.ProvenanceGraph, row.Provenance)
					require.Greater(t, row.HopDistance, 0)
				} else {
					require.Equal(t, domain.ProvenanceRecall, row.Provenance)
				}
			}
			want := 0
			if graph {
				want = 1
			}
			require.Equal(t, want, count)
			env.assertFeedback(t, rows)
		})
	}
}

func TestFinalRecallDB_EmptyNoFeedback(t *testing.T) {
	env := newFinalRecallDBEnv(t)
	code, rows, _ := env.request(t, "xyzzyquux unrelatedidentifier", 5, 0, true, false)
	require.Equal(t, http.StatusOK, code)
	require.Empty(t, rows, "dense-only garbage must be gated before graph and feedback")
	env.assertFeedback(t, rows)
}

func TestFinalRecallDB_LegacyFinalSetPositiveControl(t *testing.T) {
	env := newFinalRecallDBEnv(t)
	code, rows, body := env.request(t, "cobalt checkout", 5, 0, false, true)
	require.Equal(t, http.StatusOK, code)
	require.NotContains(t, body, "final_recall_version")
	require.Len(t, rows, 5)
	env.assertFeedback(t, rows)
}

func TestFinalRecallDB_PaginationAndBounds(t *testing.T) {
	env := newFinalRecallDBEnv(t)
	code, rows, body := env.request(t, "cobalt checkout", 5, 5, false, false)
	require.Equal(t, http.StatusOK, code)
	require.Len(t, rows, 5)
	require.Equal(t, float64(5), body["offset"])
	env.assertFeedback(t, rows)
	for _, limit := range []int{0, 51, -1} {
		code, _, _ := env.request(t, "cobalt checkout", limit, 0, false, false)
		require.Equal(t, http.StatusBadRequest, code)
	}
	for _, offset := range []int{-1, 151} {
		code, _, _ := env.request(t, "cobalt checkout", 5, offset, false, false)
		require.Equal(t, http.StatusBadRequest, code)
	}
}

func TestFinalRecallDB_FiltersAndAuth(t *testing.T) {
	cases := map[string]string{"tags": "denied", "tags_any": "denied", "scope": "agent", "created_by": uuid.NewString(), "source_type": "human", "min_importance": "0.9", "relevance_min": "0.5", "since": "2100-01-01T00:00:00Z", "until": "2000-01-01T00:00:00Z"}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			env := newFinalRecallDBEnv(t)
			params := url.Values{"q": {"cobalt checkout"}, "limit": {"5"}, "graph": {"true"}, name: {value}}
			code, rows, _ := env.requestParams(t, params, false)
			require.Equal(t, http.StatusOK, code)
			require.Empty(t, rows)
			env.assertFeedback(t, rows)
		})
	}
	t.Run("foreign-workspace", func(t *testing.T) {
		env := newFinalRecallDBEnv(t)
		code, rows, _ := env.requestParams(t, url.Values{"q": {"cobalt checkout"}, "workspace_id": {uuid.NewString()}, "limit": {"5"}}, false)
		require.Equal(t, http.StatusForbidden, code)
		env.assertFeedback(t, rows)
	})
}

func TestFinalRecallDB_LifecycleOptInAndFullBody(t *testing.T) {
	for _, field := range []string{"include_expired", "include_archived"} {
		t.Run(field, func(t *testing.T) {
			env := newFinalRecallDBEnv(t)
			content := "cobalt checkout " + strings.Repeat("complete answer ", 100)
			id := uuid.New()
			memory := &domain.Memory{ID: id, WorkspaceID: env.ws, Key: "lifecycle-cobalt", Content: content, Scope: domain.ScopeWorkspace, SourceType: domain.SourceAgent, Relevance: 0.4, ImportanceScore: 0.8, Tags: pq.StringArray{"lifecycle"}}
			require.NoError(t, env.repo.Upsert(context.Background(), memory, domain.MemoryWriteIntent{}))
			env.ids = append(env.ids, id)
			if field == "include_expired" {
				_, err := env.db.Exec(`UPDATE memories SET expires_at=NOW()-INTERVAL '1 day' WHERE id=$1`, id)
				require.NoError(t, err)
			} else {
				_, err := env.db.Exec(`UPDATE memories SET archived=true WHERE id=$1`, id)
				require.NoError(t, err)
			}
			params := url.Values{"q": {"cobalt checkout"}, "limit": {"5"}, "tags": {"lifecycle"}, "full": {"true"}}
			code, rows, _ := env.requestParams(t, params, false)
			require.Equal(t, http.StatusOK, code)
			require.Empty(t, rows)
			env.assertFeedback(t, rows)
			params.Set(field, "TRUE")
			code, rows, _ = env.requestParams(t, params, false)
			require.Equal(t, http.StatusOK, code)
			require.Len(t, rows, 1)
			require.Equal(t, id, rows[0].ID)
			require.Equal(t, content, rows[0].Content)
			env.assertFeedback(t, rows)
		})
	}
}

func TestFinalRecallDB_InvalidParamsFailClosed(t *testing.T) {
	env := newFinalRecallDBEnv(t)
	for name, values := range map[string][]string{"limit": {"NaN", "51"}, "offset": {"-1", "151"}, "min_importance": {"NaN", "+Inf", "1.1"}, "relevance_min": {"NaN", "-0.1"}, "recency_weight": {"NaN"}, "half_life_days": {"NaN"}, "graph": {"invalid"}, "include_archived": {"invalid"}, "include_expired": {"invalid"}, "scope": {"foreign"}} {
		for _, value := range values {
			code, rows, _ := env.requestParams(t, url.Values{"q": {"cobalt checkout"}, "limit": {"5"}, name: {value}}, false)
			require.Equal(t, http.StatusBadRequest, code)
			env.assertFeedback(t, rows)
		}
	}
}

func TestFinalRecallDB_ExplicitOverrides(t *testing.T) {
	for _, field := range []string{"min_importance", "exclude_superseded"} {
		t.Run(field, func(t *testing.T) {
			env := newFinalRecallDBEnv(t)
			id := uuid.New()
			row := &domain.Memory{ID: id, WorkspaceID: env.ws, Key: "override-cobalt", Content: "cobalt checkout answer", Scope: domain.ScopeWorkspace, SourceType: domain.SourceAgent, Relevance: 0.4, ImportanceScore: 0.8, Tags: pq.StringArray{"override"}}
			require.NoError(t, env.repo.Upsert(context.Background(), row, domain.MemoryWriteIntent{}))
			env.ids = append(env.ids, id)
			if field == "min_importance" {
				_, err := env.db.Exec(`UPDATE memories SET importance_score=0.2 WHERE id=$1`, id)
				require.NoError(t, err)
			} else {
				_, err := env.db.Exec(`UPDATE memories SET status='superseded' WHERE id=$1`, id)
				require.NoError(t, err)
			}
			params := url.Values{"q": {"cobalt checkout"}, "limit": {"5"}, "tags": {"override"}}
			code, items, _ := env.requestParams(t, params, false)
			require.Equal(t, http.StatusOK, code)
			require.Empty(t, items)
			env.assertFeedback(t, items)
			if field == "min_importance" {
				params.Set(field, "0")
			} else {
				params.Set(field, "FALSE")
			}
			code, items, _ = env.requestParams(t, params, false)
			require.Equal(t, http.StatusOK, code)
			require.Len(t, items, 1)
			require.Equal(t, id, items[0].ID)
			env.assertFeedback(t, items)
		})
	}
}

func TestFinalRecallDB_ProjectGateAlsoAppliesToGraph(t *testing.T) {
	env := newFinalRecallDBEnv(t)
	project := uuid.New()
	_, err := env.db.Exec(`INSERT INTO projects (id,workspace_id,name,slug) VALUES ($1,$2,'recall',$3)`, project, env.ws, project.String())
	require.NoError(t, err)
	_, err = env.db.Exec(`UPDATE memories SET project_id=$1,scope='project' WHERE id=ANY($2)`, project, pq.Array(env.ids[:12]))
	require.NoError(t, err)
	code, rows, body := env.requestParams(t, url.Values{"q": {"cobalt checkout"}, "limit": {"5"}, "project_id": {project.String()}, "graph": {"true"}, "tags": {"allowed"}}, false)
	require.Equal(t, http.StatusOK, code)
	require.Len(t, rows, 5)
	require.Equal(t, float64(0), body["graph_boost_count"], "a workspace neighbour cannot enter a project-filtered page")
	for _, row := range rows {
		require.NotNil(t, row.ProjectID)
		require.Equal(t, project, *row.ProjectID)
	}
	env.assertFeedback(t, rows)
}

func TestFinalRecallDB_DenseAnswerSurvivesSparseBoost(t *testing.T) {
	for _, graph := range []bool{false, true} {
		for _, limit := range []int{5, 10} {
			t.Run(fmt.Sprintf("graph=%v/limit=%d", graph, limit), func(t *testing.T) {
				env := newFinalRecallDBEnv(t)
				// Nine lexical matches appear in both retrieval arms. The answer
				// matches the query more closely in the dense arm, but lacks two
				// query words and therefore has no strict-AND sparse score.
				_, err := env.db.Exec(`UPDATE memories SET content='cobalt checkout lock holder discussion' WHERE id=ANY($1)`, pq.Array(env.ids[:9]))
				require.NoError(t, err)
				for _, id := range env.ids[:9] {
					require.NoError(t, env.repo.UpdateEmbedding(context.Background(), id, []float32{1, 0.3, 0}, "final-recall-test", 3))
				}
				answer := env.ids[9]
				_, err = env.db.Exec(`UPDATE memories SET key='opaque-answer',content='cobalt holder: AckReservation is the answer' WHERE id=$1`, answer)
				require.NoError(t, err)
				_, err = env.db.Exec(`UPDATE memories SET archived=true WHERE id=ANY($1)`, pq.Array(env.ids[10:12]))
				require.NoError(t, err)
				code, rows, body := env.request(t, "cobalt checkout lock holder", limit, 0, graph, false)
				require.Equal(t, http.StatusOK, code)
				require.Equal(t, float64(10), body["candidate_limit"])
				ids := make([]uuid.UUID, 0, len(rows))
				for _, row := range rows {
					ids = append(ids, row.ID)
				}
				require.Contains(t, ids, answer, "a dense answer must survive dual-arm rank inflation at output5")
				env.assertFeedback(t, rows)
			})
		}
	}
}
