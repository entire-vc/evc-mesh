package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FilterWorkspaceMembers decides who is told the contents of a comment, and
// every caller fails closed when it returns an error. That combination makes a
// query that cannot execute indistinguishable from a workspace with no members:
// the notifications simply stop, nothing raises, and the logs fill with a line
// nobody reads.
//
// The sqlmock tests in notification_repo_test.go cannot see that. sqlmock matches
// the query text and replays rows the test wrote; it never asks PostgreSQL
// whether the SQL is valid, so a predicate that is a type error against the real
// schema passes every one of them. These run the statement against the actual
// tables, which is the only thing that answers the question.

func createMembershipTestUser(t *testing.T, db *sqlx.DB, label string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	handle := label + "-" + id.String()[:8]
	_, err := db.Exec(
		`INSERT INTO users (id, email, password_hash, display_name, username, is_active)
		 VALUES ($1, $2, 'not-a-real-hash', $3, $4, true)`,
		id, handle+"@example.test", label, handle,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id = $1`, id) })
	return id
}

// TestFilterWorkspaceMembersDB_AnswersAgainstTheRealSchema is the regression
// test for a predicate that was valid Go and invalid SQL.
//
// UserIsWorkspaceMember reads role into a string and checks role != "", which is
// vacuous — workspace_members.role is a NOT NULL enum of
// owner/admin/member/viewer. Transcribed into the query as a comparison against
// an empty string literal it became an error rather than a no-op: PostgreSQL has
// to coerce that literal to workspace_role, no such label exists, and so every
// call returned 22P02. Every caller fell into its fail-closed branch, and the
// notification system delivered nothing to anyone.
func TestFilterWorkspaceMembersDB_AnswersAgainstTheRealSchema(t *testing.T) {
	db := notifPrefsTestDB(t)
	repo := NewNotificationRepo(db)
	ctx := context.Background()

	owner := createMembershipTestUser(t, db, "owner")
	member := createMembershipTestUser(t, db, "member")
	stranger := createMembershipTestUser(t, db, "stranger")

	wsID := uuid.New()
	_, err := db.Exec(
		`INSERT INTO workspaces (id, name, slug, owner_id) VALUES ($1, 'Membership Test', $2, $3)`,
		wsID, "member-ws-"+wsID.String()[:8], owner,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id = $1`, wsID) })

	_, err = db.Exec(
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1, $2, $3, 'member')`,
		uuid.New(), wsID, member,
	)
	require.NoError(t, err)

	members, err := repo.FilterWorkspaceMembers(ctx, wsID, []uuid.UUID{owner, member, stranger})
	require.NoError(t, err, "the membership query does not execute against the real schema")

	assert.True(t, members[member], "a workspace member was not recognised as one")
	assert.True(t, members[owner], "the workspace owner, who has no workspace_members row, was dropped")
	assert.False(t, members[stranger], "a user with no membership was reported as a member")
}

// TestFilterWorkspaceMembersDB_EveryRoleCounts: the roles are a permission
// gradient, not a membership one. A viewer sees the workspace, so a viewer is
// told about it; filtering any of these out would silently unsubscribe a whole
// class of real users.
func TestFilterWorkspaceMembersDB_EveryRoleCounts(t *testing.T) {
	db := notifPrefsTestDB(t)
	repo := NewNotificationRepo(db)
	ctx := context.Background()

	owner := createMembershipTestUser(t, db, "wsowner")

	wsID := uuid.New()
	_, err := db.Exec(
		`INSERT INTO workspaces (id, name, slug, owner_id) VALUES ($1, 'Roles Test', $2, $3)`,
		wsID, "roles-ws-"+wsID.String()[:8], owner,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id = $1`, wsID) })

	roles := []string{"owner", "admin", "member", "viewer"}
	byRole := make(map[string]uuid.UUID, len(roles))
	candidates := make([]uuid.UUID, 0, len(roles))

	for _, role := range roles {
		userID := createMembershipTestUser(t, db, "role-"+role)
		_, insertErr := db.Exec(
			`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1, $2, $3, $4)`,
			uuid.New(), wsID, userID, role,
		)
		require.NoError(t, insertErr)
		byRole[role] = userID
		candidates = append(candidates, userID)
	}

	members, err := repo.FilterWorkspaceMembers(ctx, wsID, candidates)
	require.NoError(t, err)

	for _, role := range roles {
		assert.True(t, members[byRole[role]], "a %q was excluded from the workspace's notifications", role)
	}
}
