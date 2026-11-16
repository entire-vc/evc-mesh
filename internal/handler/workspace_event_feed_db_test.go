package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// GET /workspaces/:ws_id/events. The router is the production
// slice: DualAuth → WorkspaceRLS → RequireWorkspaceMemberScoped → the route's
// RequireWorkspaceMember, same as cmd/api. Events are seeded the way the task
// transition writer stores them: agent_id NULL, the actor in
// payload.actor_type / payload.actor_id.

type eventFeedEnv struct {
	db      *sqlx.DB
	server  *httptest.Server
	authSvc *auth.Service
	agents  service.AgentService
}

func newEventFeedEnv(t *testing.T) *eventFeedEnv {
	t.Helper()
	db := oauthE2ETestDB(t)
	userRepo := postgres.NewUserRepo(db)
	workspaceRepo := postgres.NewWorkspaceRepo(db)
	workspaceMemberRepo := postgres.NewWorkspaceMemberRepo(db)
	agentRepo := postgres.NewAgentRepo(db)
	agentSvc := service.NewAgentService(agentRepo, postgres.NewActivityLogRepo(db), workspaceRepo, userRepo)
	if configurable, ok := agentSvc.(service.AgentServiceConfigurable); ok {
		configurable.SetAgentWorkspaceGrantRepo(postgres.NewAgentWorkspaceGrantRepo(db))
	}
	authSvc := auth.NewService(userRepo, postgres.NewRefreshTokenRepo(db), workspaceRepo, workspaceMemberRepo, "event-feed-test-jwt-secret")

	e := echo.New()
	e.HTTPErrorHandler = NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)
	api := e.Group("/api/v1")
	api.Use(mw.DualAuth(authSvc, agentSvc, nil))
	api.Use(mw.WorkspaceRLS(db, postgres.NewProjectRepo(db)))
	api.Use(mw.RequireWorkspaceMemberScoped(db))
	api.GET("/workspaces/:ws_id/events", NewWorkspaceEventHandler(postgres.NewWorkspaceEventFeedRepo(db)).List, mw.RequireWorkspaceMember(db))

	server := httptest.NewServer(e)
	t.Cleanup(server.Close)
	return &eventFeedEnv{db: db, server: server, authSvc: authSvc, agents: agentSvc}
}

func (env *eventFeedEnv) register(t *testing.T, prefix string) (token string, userID, wsID uuid.UUID) {
	t.Helper()
	suffix := uuid.New().String()[:8]
	user, tokens, err := env.authSvc.Register(context.Background(),
		fmt.Sprintf("%s-%s@event-feed.example.com", prefix, suffix), "Correct-Horse-Battery-Staple-1", prefix+"-"+suffix)
	require.NoError(t, err)
	ws, err := postgres.NewWorkspaceRepo(env.db).ListForUser(context.Background(), user.ID)
	require.NoError(t, err)
	require.NotEmpty(t, ws)
	return tokens.AccessToken, user.ID, ws[0].ID
}

func (env *eventFeedEnv) project(t *testing.T, wsID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := env.db.Exec(`INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1, $2, $3, $4)`,
		id, wsID, name, "ef-"+id.String()[:8])
	require.NoError(t, err)
	return id
}

func (env *eventFeedEnv) event(t *testing.T, wsID, projID uuid.UUID, et domain.EventType, actorType string, actorID *uuid.UUID, at time.Time) uuid.UUID {
	t.Helper()
	payload := map[string]any{"action": "task.updated"}
	if actorType != "" {
		payload["actor_type"] = actorType
	}
	if actorID != nil {
		payload["actor_id"] = actorID.String()
	}
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	id := uuid.New()
	_, err = env.db.Exec(`INSERT INTO event_bus_messages (id, workspace_id, project_id, event_type, subject, payload, tags, created_at, expires_at)
		VALUES ($1, $2, $3, $4, 'task.updated', $5, '{auto,task}', $6::timestamptz, $6::timestamptz + interval '1 day')`,
		id, wsID, projID, string(et), raw, at)
	require.NoError(t, err)
	return id
}

type feedPage struct {
	Items []struct {
		ID          uuid.UUID `json:"id"`
		ProjectID   uuid.UUID `json:"project_id"`
		EventType   string    `json:"event_type"`
		ActorType   string    `json:"actor_type"`
		ActorName   *string   `json:"actor_name"`
		ProjectName *string   `json:"project_name"`
	} `json:"items"`
	HasMore    bool    `json:"has_more"`
	NextCursor *string `json:"next_cursor"`
}

func (env *eventFeedEnv) get(t *testing.T, token string, wsID uuid.UUID, q url.Values) (int, feedPage) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, env.server.URL+"/api/v1/workspaces/"+wsID.String()+"/events?"+q.Encode(), http.NoBody)
	require.NoError(t, err)
	if strings.HasPrefix(token, "agent:") {
		req.Header.Set("X-Agent-Key", strings.TrimPrefix(token, "agent:"))
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var page feedPage
	if resp.StatusCode == http.StatusOK {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&page))
	}
	return resp.StatusCode, page
}

func feedIDs(p feedPage) map[uuid.UUID]bool {
	out := map[uuid.UUID]bool{}
	for _, it := range p.Items {
		out[it.ID] = true
	}
	return out
}

func TestWorkspaceEventFeed(t *testing.T) {
	env := newEventFeedEnv(t)
	ownerToken, _, wsID := env.register(t, "ef-owner")
	now := time.Now().UTC().Truncate(time.Millisecond)

	// Seven projects: the old client walked only the first five.
	projects := make([]uuid.UUID, 7)
	for i := range projects {
		projects[i] = env.project(t, wsID, fmt.Sprintf("P%d", i+1))
	}
	reg, err := env.agents.Register(context.Background(), service.RegisterAgentInput{WorkspaceID: wsID, Name: "feed-agent-" + uuid.New().String()[:6], AgentType: domain.AgentTypeCustom})
	require.NoError(t, err)
	agentID := reg.Agent.ID

	evSixth := env.event(t, wsID, projects[5], domain.EventTypeStatusChange, "agent", &agentID, now.Add(-1*time.Minute))
	evSeventh := env.event(t, wsID, projects[6], domain.EventTypeSummary, "system", nil, now.Add(-2*time.Minute))
	evFirst := env.event(t, wsID, projects[0], domain.EventTypeCustom, "user", nil, now.Add(-3*time.Minute))
	evError := env.event(t, wsID, projects[1], domain.EventTypeError, "", nil, now.Add(-4*time.Minute))

	t.Run("projects six and seven are visible to the owner", func(t *testing.T) {
		status, page := env.get(t, ownerToken, wsID, url.Values{})
		require.Equal(t, http.StatusOK, status)
		got := feedIDs(page)
		assert.True(t, got[evSixth], "event of the 6th project")
		assert.True(t, got[evSeventh], "event of the 7th project")
		assert.True(t, got[evFirst])
		assert.Len(t, page.Items, 4)
		assert.False(t, page.HasMore)
		// Newest first, the agent named from payload.actor_id.
		require.Equal(t, evSixth, page.Items[0].ID)
		assert.Equal(t, "agent", page.Items[0].ActorType)
		require.NotNil(t, page.Items[0].ActorName)
		assert.Equal(t, reg.Agent.Name, *page.Items[0].ActorName)
		require.NotNil(t, page.Items[0].ProjectName)
		assert.Equal(t, "P6", *page.Items[0].ProjectName)
	})

	t.Run("several event types at once", func(t *testing.T) {
		for _, q := range []url.Values{
			{"event_type": {"summary", "error"}},
			{"event_type[]": {"summary", "error"}},
			{"event_type": {"summary,error"}},
		} {
			status, page := env.get(t, ownerToken, wsID, q)
			require.Equal(t, http.StatusOK, status)
			got := feedIDs(page)
			assert.Equal(t, map[uuid.UUID]bool{evSeventh: true, evError: true}, got, "query %v", q)
		}
		status, _ := env.get(t, ownerToken, wsID, url.Values{"event_type": {"bogus"}})
		assert.Equal(t, http.StatusBadRequest, status, "an unknown type is a 400, not an empty page")
	})

	t.Run("actor_type system, agent and user", func(t *testing.T) {
		status, page := env.get(t, ownerToken, wsID, url.Values{"actor_type": {"system"}})
		require.Equal(t, http.StatusOK, status)
		// payload actor_type system and a payload without any actor are both system.
		assert.Equal(t, map[uuid.UUID]bool{evSeventh: true, evError: true}, feedIDs(page))
		for _, it := range page.Items {
			assert.Equal(t, "system", it.ActorType)
		}
		_, page = env.get(t, ownerToken, wsID, url.Values{"actor_type": {"agent"}})
		assert.Equal(t, map[uuid.UUID]bool{evSixth: true}, feedIDs(page))
		_, page = env.get(t, ownerToken, wsID, url.Values{"actor_type": {"user"}})
		assert.Equal(t, map[uuid.UUID]bool{evFirst: true}, feedIDs(page))
		_, page = env.get(t, ownerToken, wsID, url.Values{"agent_id": {agentID.String()}})
		assert.Equal(t, map[uuid.UUID]bool{evSixth: true}, feedIDs(page))
	})

	t.Run("cursor pages through every row once", func(t *testing.T) {
		seen := map[uuid.UUID]bool{}
		q := url.Values{"limit": {"1"}}
		for i := 0; i < 10; i++ {
			status, page := env.get(t, ownerToken, wsID, q)
			require.Equal(t, http.StatusOK, status)
			for id := range feedIDs(page) {
				assert.False(t, seen[id], "row returned twice")
				seen[id] = true
			}
			if !page.HasMore {
				break
			}
			require.NotNil(t, page.NextCursor)
			q.Set("cursor", *page.NextCursor)
		}
		assert.Len(t, seen, 4)
	})

	t.Run("project and date filters", func(t *testing.T) {
		_, page := env.get(t, ownerToken, wsID, url.Values{"project_id": {projects[6].String()}})
		assert.Equal(t, map[uuid.UUID]bool{evSeventh: true}, feedIDs(page))
		_, page = env.get(t, ownerToken, wsID, url.Values{
			"date_from": {now.Add(-150 * time.Second).Format(time.RFC3339)},
			"date_to":   {now.Format(time.RFC3339)},
		})
		assert.Equal(t, map[uuid.UUID]bool{evSixth: true, evSeventh: true}, feedIDs(page))
	})

	t.Run("a member sees only the projects they belong to", func(t *testing.T) {
		memberToken, memberID, _ := env.register(t, "ef-member")
		_, err := env.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1, $2, $3, 'member')`, uuid.New(), wsID, memberID)
		require.NoError(t, err)

		// Not a member of any project yet: the 6th project's event is hidden.
		status, page := env.get(t, memberToken, wsID, url.Values{})
		require.Equal(t, http.StatusOK, status)
		assert.Empty(t, page.Items, "a workspace member outside every project sees no project events")
		status, page = env.get(t, memberToken, wsID, url.Values{"project_id": {projects[5].String()}})
		require.Equal(t, http.StatusOK, status)
		assert.Empty(t, page.Items, "naming a foreign project in project_id must not widen the scope")

		// Positive control: membership in the 6th project shows exactly its event.
		_, err = env.db.Exec(`INSERT INTO project_members (id, project_id, user_id, role, workspace_id) VALUES ($1, $2, $3, 'member', $4)`, uuid.New(), projects[5], memberID, wsID)
		require.NoError(t, err)
		status, page = env.get(t, memberToken, wsID, url.Values{})
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, map[uuid.UUID]bool{evSixth: true}, feedIDs(page))
	})

	t.Run("an agent sees only the projects it is a member of", func(t *testing.T) {
		agentToken := "agent:" + reg.APIKey
		status, page := env.get(t, agentToken, wsID, url.Values{})
		require.Equal(t, http.StatusOK, status)
		assert.Empty(t, page.Items, "an agent outside every project sees no project events")

		// Positive control: membership in the 7th project shows exactly its event.
		_, err := env.db.Exec(`INSERT INTO project_members (id, project_id, agent_id, role, workspace_id) VALUES ($1, $2, $3, 'member', $4)`, uuid.New(), projects[6], agentID, wsID)
		require.NoError(t, err)
		status, page = env.get(t, agentToken, wsID, url.Values{})
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, map[uuid.UUID]bool{evSeventh: true}, feedIDs(page))
	})

	t.Run("agent, task and tag filters reach the query", func(t *testing.T) {
		_, page := env.get(t, ownerToken, wsID, url.Values{"agent_id": {agentID.String()}})
		assert.Equal(t, map[uuid.UUID]bool{evSixth: true}, feedIDs(page))
		_, page = env.get(t, ownerToken, wsID, url.Values{"task_id": {uuid.New().String()}})
		assert.Empty(t, page.Items)
		_, page = env.get(t, ownerToken, wsID, url.Values{"tags": {"auto,none"}, "actor_type": {"all"}})
		assert.Len(t, page.Items, 4)
	})

	t.Run("malformed parameters are a 400", func(t *testing.T) {
		b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
		for _, q := range []url.Values{
			{"project_id": {"x"}},
			{"agent_id": {"x"}},
			{"task_id": {"x"}},
			{"date_from": {"yesterday"}},
			{"date_to": {"2026-13-01"}},
			{"actor_type": {"robot"}},
			{"limit": {"0"}},
			{"limit": {"201"}},
			{"limit": {"ten"}},
			{"cursor": {"%%%"}},
			{"cursor": {b64("no-separator")}},
			{"cursor": {b64("not-a-time|" + uuid.NewString())}},
			{"cursor": {b64(now.Format(time.RFC3339Nano) + "|not-a-uuid")}},
		} {
			status, _ := env.get(t, ownerToken, wsID, q)
			assert.Equal(t, http.StatusBadRequest, status, "query %v", q)
		}
	})

	t.Run("an owner without a workspace_members row still sees every project", func(t *testing.T) {
		// RequireWorkspaceMember admits such an owner without setting the role.
		_, err := env.db.Exec(`DELETE FROM workspace_members WHERE workspace_id = $1 AND user_id = (SELECT owner_id FROM workspaces WHERE id = $1)`, wsID)
		require.NoError(t, err)
		status, page := env.get(t, ownerToken, wsID, url.Values{})
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, map[uuid.UUID]bool{evSixth: true, evSeventh: true, evFirst: true, evError: true}, feedIDs(page))
	})

	t.Run("an outsider of the workspace is refused", func(t *testing.T) {
		outsiderToken, _, _ := env.register(t, "ef-outsider")
		status, _ := env.get(t, outsiderToken, wsID, url.Values{})
		assert.Equal(t, http.StatusForbidden, status)
	})
}
