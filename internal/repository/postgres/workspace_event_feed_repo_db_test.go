package postgres

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// feedFixture is one workspace with a project the user and the agent belong to
// (A), a project nobody belongs to (B) and a deleted project (C), with events
// written the way the bus writes them: the actor mostly in the payload.
type feedFixture struct {
	db                *sqlx.DB
	repo              *WorkspaceEventFeedRepo
	ws, projA, projB  uuid.UUID
	user, agent, task uuid.UUID
	owner             uuid.UUID
	agentName         string
	now               time.Time
	byAgent, byUser   uuid.UUID // project A, payload actor
	bySystem          uuid.UUID // project B, no actor
	inDeleted         uuid.UUID // project C (deleted)
	byAgentColumn     uuid.UUID // project A, agent_id column, on the task
	tagged            uuid.UUID // = byAgent, the only one tagged "hot"
}

func newFeedFixture(t *testing.T) *feedFixture {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to run the workspace event feed repo tests against PostgreSQL")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	f := &feedFixture{db: db, repo: NewWorkspaceEventFeedRepo(db), now: time.Now().UTC().Truncate(time.Microsecond)}
	w := &domain.Workspace{ID: uuid.New(), Name: "Feed", Slug: "feed-" + uuid.NewString(), OwnerID: uuid.New()}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, w))
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id=$1`, w.ID) })
	f.ws, f.owner = w.ID, w.OwnerID

	project := func(name string) uuid.UUID {
		p := &domain.Project{ID: uuid.New(), WorkspaceID: w.ID, Name: name, Slug: "feed-" + uuid.NewString(), DefaultAssigneeType: domain.DefaultAssigneeNone}
		require.NoError(t, NewProjectRepo(db).Create(ctx, p))
		return p.ID
	}
	f.projA, f.projB = project("Alpha"), project("Beta")
	projC := project("Gone")
	_, err = db.Exec(`UPDATE projects SET deleted_at = now() WHERE id = $1`, projC)
	require.NoError(t, err)

	f.user, f.agent = uuid.New(), uuid.New()
	_, err = db.Exec(`INSERT INTO users (id, email, username, password_hash, display_name) VALUES ($1, $2, $3, 'x', 'Feed Person')`,
		f.user, f.user.String()+"@feed.test", "feed-"+f.user.String()[:30])
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id=$1`, f.user) })
	f.agentName = "feed agent " + uuid.NewString()[:6]
	_, err = db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix) VALUES ($1, $2, $3, $4, 'h', 'p')`,
		f.agent, w.ID, f.agentName, "feed-agent-"+uuid.NewString()[:8])
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1, $2, $3, 'member')`, uuid.New(), w.ID, f.user)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO project_members (id, project_id, workspace_id, user_id, role) VALUES ($1, $2, $3, $4, 'member')`, uuid.New(), f.projA, w.ID, f.user)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO project_members (id, project_id, workspace_id, agent_id, role) VALUES ($1, $2, $3, $4, 'member')`, uuid.New(), f.projA, w.ID, f.agent)
	require.NoError(t, err)

	status := uuid.New()
	_, err = db.Exec(`INSERT INTO task_statuses (id, project_id, name, slug, category) VALUES ($1, $2, 'Todo', 'todo', 'todo')`, status, f.projA)
	require.NoError(t, err)
	f.task = uuid.New()
	_, err = db.Exec(`INSERT INTO tasks (id, project_id, status_id, title, task_number, created_by) VALUES ($1, $2, $3, 'Feed task', 1, $4)`, f.task, f.projA, status, f.user)
	require.NoError(t, err)

	ev := func(proj uuid.UUID, et domain.EventType, agentCol, task *uuid.UUID, actorType string, actorID *uuid.UUID, tags string, ago time.Duration) uuid.UUID {
		payload := map[string]any{"action": "task.updated"}
		if actorType != "" {
			payload["actor_type"] = actorType
		}
		if actorID != nil {
			payload["actor_id"] = actorID.String()
		}
		raw, merr := json.Marshal(payload)
		require.NoError(t, merr)
		id := uuid.New()
		_, ierr := db.Exec(`INSERT INTO event_bus_messages (id, workspace_id, project_id, task_id, agent_id, event_type, subject, payload, tags, created_at, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, 'task.updated', $7, $8::text[], $9::timestamptz, $9::timestamptz + interval '1 day')`,
			id, w.ID, proj, task, agentCol, string(et), raw, tags, f.now.Add(-ago))
		require.NoError(t, ierr)
		return id
	}
	f.byAgent = ev(f.projA, domain.EventTypeStatusChange, nil, nil, "agent", &f.agent, "{auto,hot}", 1*time.Minute)
	f.byUser = ev(f.projA, domain.EventTypeSummary, nil, nil, "user", &f.user, "{auto}", 2*time.Minute)
	f.bySystem = ev(f.projB, domain.EventTypeError, nil, nil, "", nil, "{auto}", 3*time.Minute)
	f.inDeleted = ev(projC, domain.EventTypeCustom, nil, nil, "", nil, "{auto}", 4*time.Minute)
	f.byAgentColumn = ev(f.projA, domain.EventTypeCustom, &f.agent, &f.task, "", nil, "{auto}", 5*time.Minute)
	f.tagged = f.byAgent
	return f
}

func (f *feedFixture) list(t *testing.T, mod func(*WorkspaceEventFeedFilter)) ([]WorkspaceFeedEvent, bool) {
	t.Helper()
	flt := WorkspaceEventFeedFilter{WorkspaceID: f.ws, AllProjects: true}
	if mod != nil {
		mod(&flt)
	}
	items, more, err := f.repo.List(context.Background(), flt)
	require.NoError(t, err)
	return items, more
}

func feedEventIDs(items []WorkspaceFeedEvent) []uuid.UUID {
	out := make([]uuid.UUID, len(items))
	for i := range items {
		out[i] = items[i].ID
	}
	return out
}

func TestWorkspaceEventFeedRepo(t *testing.T) {
	f := newFeedFixture(t)
	ctx := context.Background()

	t.Run("no workspace or no project scope is refused", func(t *testing.T) {
		_, _, err := f.repo.List(ctx, WorkspaceEventFeedFilter{AllProjects: true})
		require.ErrorIs(t, err, ErrEventFeedNoScope)
		_, _, err = f.repo.List(ctx, WorkspaceEventFeedFilter{WorkspaceID: f.ws})
		require.ErrorIs(t, err, ErrEventFeedNoScope)
	})

	t.Run("all projects: newest first, deleted project left out, actors derived", func(t *testing.T) {
		items, more := f.list(t, nil)
		assert.False(t, more)
		require.Equal(t, []uuid.UUID{f.byAgent, f.byUser, f.bySystem, f.byAgentColumn}, feedEventIDs(items))

		agentRow, userRow, systemRow, columnRow := items[0], items[1], items[2], items[3]
		assert.Equal(t, EventActorAgent, agentRow.ActorType)
		require.NotNil(t, agentRow.ActorID)
		assert.Equal(t, f.agent, *agentRow.ActorID)
		require.NotNil(t, agentRow.ActorName)
		assert.Equal(t, f.agentName, *agentRow.ActorName)
		require.NotNil(t, agentRow.ProjectSlug)

		assert.Equal(t, EventActorUser, userRow.ActorType)
		require.NotNil(t, userRow.ActorName)
		assert.Equal(t, "Feed Person", *userRow.ActorName)

		assert.Equal(t, EventActorSystem, systemRow.ActorType)
		assert.Nil(t, systemRow.ActorID)

		assert.Equal(t, EventActorAgent, columnRow.ActorType, "agent_id column wins")
		require.NotNil(t, columnRow.TaskTitle)
		assert.Equal(t, "Feed task", *columnRow.TaskTitle)
	})

	t.Run("member scopes see only their projects", func(t *testing.T) {
		items, _ := f.list(t, func(x *WorkspaceEventFeedFilter) { x.AllProjects, x.MemberUserID = false, &f.user })
		assert.ElementsMatch(t, []uuid.UUID{f.byAgent, f.byUser, f.byAgentColumn}, feedEventIDs(items))
		items, _ = f.list(t, func(x *WorkspaceEventFeedFilter) { x.AllProjects, x.MemberAgentID = false, &f.agent })
		assert.ElementsMatch(t, []uuid.UUID{f.byAgent, f.byUser, f.byAgentColumn}, feedEventIDs(items))
		items, _ = f.list(t, func(x *WorkspaceEventFeedFilter) { x.AllProjects, x.MemberUserID = false, &f.owner })
		assert.ElementsMatch(t, []uuid.UUID{f.byAgent, f.byUser, f.bySystem, f.byAgentColumn}, feedEventIDs(items),
			"the workspace owner without a role still sees every live project")
		stranger := uuid.New()
		items, _ = f.list(t, func(x *WorkspaceEventFeedFilter) {
			x.AllProjects, x.MemberUserID, x.ProjectID = false, &stranger, &f.projB
		})
		assert.Empty(t, items, "a project filter never widens the scope")
	})

	t.Run("filters", func(t *testing.T) {
		cases := []struct {
			name string
			mod  func(*WorkspaceEventFeedFilter)
			want []uuid.UUID
		}{
			{"several types", func(x *WorkspaceEventFeedFilter) {
				x.EventTypes = []domain.EventType{domain.EventTypeSummary, domain.EventTypeError}
			}, []uuid.UUID{f.byUser, f.bySystem}},
			{"project", func(x *WorkspaceEventFeedFilter) { x.ProjectID = &f.projB }, []uuid.UUID{f.bySystem}},
			{"actor system", func(x *WorkspaceEventFeedFilter) { x.ActorType = EventActorSystem }, []uuid.UUID{f.bySystem}},
			{"actor user", func(x *WorkspaceEventFeedFilter) { x.ActorType = EventActorUser }, []uuid.UUID{f.byUser}},
			{"actor agent", func(x *WorkspaceEventFeedFilter) { x.ActorType = EventActorAgent }, []uuid.UUID{f.byAgent, f.byAgentColumn}},
			{"agent id from payload or column", func(x *WorkspaceEventFeedFilter) { x.AgentID = &f.agent }, []uuid.UUID{f.byAgent, f.byAgentColumn}},
			{"task", func(x *WorkspaceEventFeedFilter) { x.TaskID = &f.task }, []uuid.UUID{f.byAgentColumn}},
			{"tags any of", func(x *WorkspaceEventFeedFilter) { x.Tags = []string{"hot", "none"} }, []uuid.UUID{f.tagged}},
			{"date window", func(x *WorkspaceEventFeedFilter) {
				from, to := f.now.Add(-150*time.Second), f.now.Add(-90*time.Second)
				x.From, x.To = &from, &to
			}, []uuid.UUID{f.byUser}},
		}
		for _, tc := range cases {
			items, _ := f.list(t, tc.mod)
			assert.Equal(t, tc.want, feedEventIDs(items), tc.name)
		}
	})

	t.Run("cursor pages and limit bounds", func(t *testing.T) {
		first, more := f.list(t, func(x *WorkspaceEventFeedFilter) { x.Limit = 3 })
		require.True(t, more)
		require.Len(t, first, 3)
		last := first[2]
		rest, more := f.list(t, func(x *WorkspaceEventFeedFilter) {
			x.Limit = 3
			x.Cursor = &WorkspaceEventCursor{CreatedAt: last.CreatedAt, ID: last.ID}
		})
		assert.False(t, more)
		assert.Equal(t, []uuid.UUID{f.byAgentColumn}, feedEventIDs(rest))

		all, _ := f.list(t, func(x *WorkspaceEventFeedFilter) { x.Limit = 0 })
		assert.Len(t, all, 4, "a zero limit means the default page")
		all, _ = f.list(t, func(x *WorkspaceEventFeedFilter) { x.Limit = 10_000 })
		assert.Len(t, all, 4, "an oversized limit is clamped, not refused")
	})
}
