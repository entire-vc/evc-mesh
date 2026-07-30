package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The sqlmock test next door, TestDeletePreferenceForUser_SomebodyElsesRowRemovesNothing,
// calls mock.ExpectExec with no WithArgs — sqlmock matches on the query TEXT only, so
// the canned "0 rows affected" result comes back no matter what arguments the query
// actually executed with. That test passes identically whether DeletePreferenceForUser
// filters on user_id or ignores it entirely and deletes by id alone; it demonstrates the
// mock returning what it was told to, not that somebody else's row survives. Found by an
// independent verifier on #b0e1580b (task ec305b1e): a mutation removing user_id from the
// real WHERE clause left every sqlmock test green.
//
// This asserts the property the sqlmock test's name claims, against the real table: the
// stranger's row is still there after a same-id delete under a different user_id, and the
// owner's own delete under their own user_id is the one that removes it.

// TestDeletePreferenceForUserDB_SomebodyElsesRowSurvives is the regression test for the
// gap above. It seeds two preference rows for two different users, deletes the second
// user's row while authenticated as the first, and reads the table directly rather than
// trusting the returned row count — the row count on a broken WHERE clause could still
// happen to be right for this one row, but the row itself would be gone.
func TestDeletePreferenceForUserDB_SomebodyElsesRowSurvives(t *testing.T) {
	db := notifPrefsTestDB(t)
	repo := NewNotificationRepo(db)
	ctx := context.Background()

	wsA, ownerA := notifPrefsFixture(t, db)
	wsB, ownerB := notifPrefsFixture(t, db)

	prefA := insertRawPreference(t, db, wsA, ownerA)
	prefB := insertRawPreference(t, db, wsB, ownerB)

	// ownerA tries to delete ownerB's preference by id.
	removed, err := repo.DeletePreferenceForUser(ctx, prefB, ownerA)
	require.NoError(t, err)
	assert.EqualValues(t, 0, removed, "a delete for somebody else's row reported removing one")

	assert.Equal(t, 1, countPreferenceRowsByID(t, db, prefA), "the caller's own row disappeared")
	assert.Equal(t, 1, countPreferenceRowsByID(t, db, prefB), "a stranger's row was removed by an id guess")

	// ownerB deletes their own row — this is the row the AC requires to actually go.
	removed, err = repo.DeletePreferenceForUser(ctx, prefB, ownerB)
	require.NoError(t, err)
	assert.EqualValues(t, 1, removed, "the owner's own delete did not report removing their row")
	assert.Equal(t, 0, countPreferenceRowsByID(t, db, prefB), "the owner's own row is still in the table")
	assert.Equal(t, 1, countPreferenceRowsByID(t, db, prefA), "an unrelated row was removed as a side effect")
}

// insertRawPreference seeds a minimal preference row by direct SQL rather than
// through UpsertPreference, so this test does not depend on the correctness of
// a second repo method to set up its fixture.
func insertRawPreference(t *testing.T, db *sqlx.DB, wsID, userID uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.Exec(
		`INSERT INTO notification_preferences (id, workspace_id, user_id) VALUES ($1, $2, $3)`,
		id, wsID, userID,
	)
	require.NoError(t, err)
	return id
}

// countPreferenceRowsByID reads the table directly — the property under test is
// whether the row still exists, not what DeletePreferenceForUser's return value
// claims about it.
func countPreferenceRowsByID(t *testing.T, db *sqlx.DB, id uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, db.Get(&n, `SELECT count(*) FROM notification_preferences WHERE id = $1`, id))
	return n
}
