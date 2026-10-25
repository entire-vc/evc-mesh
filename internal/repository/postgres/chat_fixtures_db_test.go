package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// sandboxWorkspace makes sure e2e-ci-sandbox exists and removes it afterwards
// only when this test created it.
func sandboxWorkspace(t *testing.T, db *sqlx.DB) uuid.UUID {
	t.Helper()
	var ws uuid.UUID
	if err := db.Get(&ws, `SELECT id FROM workspaces WHERE slug = $1`, ChatFixturesWorkspaceSlug); err == nil {
		return ws
	}
	owner := uuid.New()
	_, err := db.Exec(`INSERT INTO users (id, email, username, password_hash, display_name) VALUES ($1,$2,$3,'x','sandbox owner')`,
		owner, owner.String()+"@sandbox.test", "so-"+owner.String()[:30])
	require.NoError(t, err)
	ws = uuid.New()
	_, err = db.Exec(`INSERT INTO workspaces (id, name, slug, owner_id) VALUES ($1,'sandbox',$2,$3)`, ws, ChatFixturesWorkspaceSlug, owner)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM workspaces WHERE id = $1`, ws)
		_, _ = db.Exec(`DELETE FROM users WHERE id = $1`, owner)
	})
	return ws
}

func fixturePasswords(suffix string) map[string]string {
	return map[string]string{"a": "pw-a-" + suffix, "b": "pw-b-" + suffix, "c": "pw-c-" + suffix, "v": "pw-v-" + suffix}
}

func TestChatFixtures_RefuseOtherWorkspaceAndEmptyPassword(t *testing.T) {
	_, err := SeedChatFixtures(context.Background(), nil, "some-real-workspace", fixturePasswords("x"))
	require.ErrorIs(t, err, ErrChatFixturesRefused)
	_, err = CleanupChatFixtures(context.Background(), nil, "some-real-workspace")
	require.ErrorIs(t, err, ErrChatFixturesRefused)

	pw := fixturePasswords("x")
	pw["c"] = ""
	_, err = SeedChatFixtures(context.Background(), nil, ChatFixturesWorkspaceSlug, pw)
	require.ErrorContains(t, err, "E2E_CHAT_PASSWORD_C")
}

func TestChatFixtures_SeedIsIdempotentAndCleanupExact(t *testing.T) {
	db := chatRepoDB(t)
	ctx := context.Background()
	ws := sandboxWorkspace(t, db)
	t.Cleanup(func() { _, _ = CleanupChatFixtures(context.Background(), db, ChatFixturesWorkspaceSlug) })
	_, err := CleanupChatFixtures(ctx, db, ChatFixturesWorkspaceSlug)
	require.NoError(t, err)

	// A bystander user and a bystander agent must survive both commands.
	bystander := uuid.New()
	_, err = db.Exec(`INSERT INTO users (id, email, username, password_hash, display_name) VALUES ($1,$2,$3,'x','bystander')`,
		bystander, bystander.String()+"@bystander.test", "by-"+bystander.String()[:30])
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id=$1`, bystander) })

	// A pre-existing, unmarked row with a reserved name is never adopted or
	// deleted. Red control first: seeding refuses, cleanup leaves it.
	squatter := uuid.New()
	_, err = db.Exec(`INSERT INTO users (id, email, username, password_hash, display_name) VALUES ($1,$2,'e2e-chat-a','x','Somebody Real')`,
		squatter, ChatFixtureEmail("a"))
	require.NoError(t, err)
	_, err = SeedChatFixtures(ctx, db, ChatFixturesWorkspaceSlug, fixturePasswords("1"))
	require.ErrorIs(t, err, ErrChatFixturesForeignRow)
	_, err = CleanupChatFixtures(ctx, db, ChatFixturesWorkspaceSlug)
	require.NoError(t, err)
	var kept int
	require.NoError(t, db.Get(&kept, `SELECT count(*) FROM users WHERE id = $1`, squatter))
	require.Equal(t, 1, kept, "cleanup must not delete a row it did not create")
	_, err = db.Exec(`DELETE FROM users WHERE id = $1`, squatter)
	require.NoError(t, err)

	first, err := SeedChatFixtures(ctx, db, ChatFixturesWorkspaceSlug, fixturePasswords("1"))
	require.NoError(t, err)
	require.Positive(t, first.Created)
	require.Len(t, first.Users, 4)
	require.Len(t, first.Agents, 3)

	roles := map[string]string{}
	rows, err := db.Query(`SELECT u.email, m.role::text FROM workspace_members m JOIN users u ON u.id = m.user_id
		WHERE m.workspace_id = $1 AND u.email LIKE 'e2e-chat-%'`, ws)
	require.NoError(t, err)
	for rows.Next() {
		var e, r string
		require.NoError(t, rows.Scan(&e, &r))
		roles[e] = r
	}
	require.NoError(t, rows.Close())
	require.Equal(t, map[string]string{
		ChatFixtureEmail("a"): "member", ChatFixtureEmail("b"): "member",
		ChatFixtureEmail("c"): "admin", ChatFixtureEmail("v"): "viewer"}, roles)

	projectRoles := map[string]string{}
	prows, err := db.Query(`SELECT u.email, pm.role::text FROM project_members pm JOIN users u ON u.id = pm.user_id
		WHERE pm.project_id = $1`, first.ProjectID)
	require.NoError(t, err)
	for prows.Next() {
		var e, r string
		require.NoError(t, prows.Scan(&e, &r))
		projectRoles[e] = r
	}
	require.NoError(t, prows.Close())
	require.Equal(t, roles, projectRoles, "every fixture user is a member of the fixture project with the same role")

	var hash string
	require.NoError(t, db.Get(&hash, `SELECT password_hash FROM users WHERE id = $1`, first.Users["c"]))
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte("pw-c-1")), "positive control: the CI value logs in")
	require.Error(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte("pw-c-2")))

	// The chat settings accept the offline agent, and it stays offline. The two harness
	// fixtures are served only once their live runners announce them, which this test does not
	// start: the same query leaving them out is the control that a controller row is still needed.
	served, err := NewAgentChatRepo(db).ServedAgents(ctx, first.ProjectID,
		[]uuid.UUID{first.Agents["claude"], first.Agents["codex"], first.Agents["offline"]})
	require.NoError(t, err)
	require.True(t, served[first.Agents["offline"]], "PUT chat settings rejects an offline agent without a controller row")
	require.False(t, served[first.Agents["claude"]], "no runner announced this harness fixture")
	require.False(t, served[first.Agents["codex"]], "no runner announced this harness fixture")
	var live bool
	require.NoError(t, db.Get(&live, `SELECT lease_expires_at > now() FROM agent_controllers WHERE agent_id = $1`, first.Agents["offline"]))
	require.False(t, live, "the offline fixture must not look live")

	// Second run, same passwords: nothing is created, ids are stable.
	second, err := SeedChatFixtures(ctx, db, ChatFixturesWorkspaceSlug, fixturePasswords("1"))
	require.NoError(t, err)
	require.Zero(t, second.Created)
	require.Equal(t, first.Users, second.Users)
	require.Equal(t, first.Agents, second.Agents)
	require.Equal(t, first.ProjectID, second.ProjectID)

	// A rotated CI variable re-hashes the same user instead of creating another.
	third, err := SeedChatFixtures(ctx, db, ChatFixturesWorkspaceSlug, fixturePasswords("2"))
	require.NoError(t, err)
	require.Equal(t, first.Users["c"], third.Users["c"])
	require.NoError(t, db.Get(&hash, `SELECT password_hash FROM users WHERE id = $1`, first.Users["c"]))
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte("pw-c-2")))

	// Chat data created in the fixture project goes with it.
	thread := uuid.New()
	_, err = db.Exec(`INSERT INTO ext_agent_chat_threads (id, workspace_id, project_id, agent_id, user_id) VALUES ($1,$2,$3,$4,$5)`,
		thread, ws, first.ProjectID, first.Agents["claude"], first.Users["a"])
	require.NoError(t, err)

	removed, err := CleanupChatFixtures(ctx, db, ChatFixturesWorkspaceSlug)
	require.NoError(t, err)
	require.Positive(t, removed.Removed)
	var left int
	require.NoError(t, db.Get(&left, `SELECT count(*) FROM users WHERE email LIKE 'e2e-chat-%@e2e-ci.invalid'`))
	require.Zero(t, left)
	require.NoError(t, db.Get(&left, `SELECT count(*) FROM ext_agent_chat_threads WHERE id = $1`, thread))
	require.Zero(t, left, "thread of the fixture project is gone")
	require.NoError(t, db.Get(&left, `SELECT count(*) FROM users WHERE id = $1`, bystander))
	require.Equal(t, 1, left, "cleanup touches only the reserved names")

	again, err := CleanupChatFixtures(ctx, db, ChatFixturesWorkspaceSlug)
	require.NoError(t, err)
	require.Zero(t, again.Removed, "idempotent")
}
