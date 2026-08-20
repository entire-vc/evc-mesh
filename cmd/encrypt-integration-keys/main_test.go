package main

import (
	"encoding/base64"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/encryption"
)

func withKey(t *testing.T, fill byte) {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = fill + byte(i)
	}
	encryption.ResetForTest()
	t.Setenv(encryption.EnvKey, base64.StdEncoding.EncodeToString(key))
	t.Cleanup(encryption.ResetForTest)
}

const selectPattern = `SELECT id, project_id, type, agent_key FROM project_integrations`

func credentialRows(values ...string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"id", "project_id", "type", "agent_key"})
	for i, v := range values {
		rows.AddRow("row-"+string(rune('a'+i)), "proj-1", "team_relay", v)
	}
	return rows
}

// The tool is worthless — worse, actively misleading — if it can run without a
// key: Encrypt degrades to a passthrough, so it would rewrite every row to
// itself and print a successful backfill.
func TestRefusesToRunWithoutAKey(t *testing.T) {
	encryption.ResetForTest()
	t.Setenv(encryption.EnvKey, "")
	t.Cleanup(encryption.ResetForTest)

	err := requireKey()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to run")
}

func TestRefusesToRunWithAMalformedKey(t *testing.T) {
	encryption.ResetForTest()
	t.Setenv(encryption.EnvKey, base64.StdEncoding.EncodeToString([]byte("tooshort")))
	t.Cleanup(encryption.ResetForTest)

	require.Error(t, requireKey())
}

func TestBackfillEncryptsPlaintextRows(t *testing.T) {
	withKey(t, 61)
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	plaintext := "tr_agent_" + strings.Repeat("a", 48)
	mock.ExpectQuery(regexp.QuoteMeta(selectPattern)).WillReturnRows(credentialRows(plaintext))
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE project_integrations SET agent_key")).
		WithArgs(sqlmock.AnyArg(), "row-a", plaintext).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// The post-check re-reads and must see the row encrypted.
	sealed, err := encryption.Encrypt(plaintext)
	require.NoError(t, err)
	mock.ExpectQuery(regexp.QuoteMeta(selectPattern)).WillReturnRows(credentialRows(sealed))

	require.NoError(t, backfill(db, false, ""))
	require.NoError(t, mock.ExpectationsWereMet())
}

// Already-encrypted rows must be left alone, so the tool is safe to re-run —
// a backfill you are afraid to repeat is a backfill nobody finishes.
func TestBackfillSkipsAlreadyEncryptedRows(t *testing.T) {
	withKey(t, 67)
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	sealed, err := encryption.Encrypt("tr_agent_already_done")
	require.NoError(t, err)
	mock.ExpectQuery(regexp.QuoteMeta(selectPattern)).WillReturnRows(credentialRows(sealed))
	// No Begin/Exec/Commit expected: nothing to do.

	require.NoError(t, backfill(db, false, ""))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDryRunWritesNothing(t *testing.T) {
	withKey(t, 71)
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(regexp.QuoteMeta(selectPattern)).
		WillReturnRows(credentialRows("tr_agent_plain"))
	mock.ExpectBegin()
	// Deliberately no ExpectExec: a dry run that issues an UPDATE fails here.
	mock.ExpectRollback()

	require.NoError(t, backfill(db, true, ""))
	require.NoError(t, mock.ExpectationsWereMet())
}

// The pre-image is pinned in the WHERE clause. Zero rows affected means the row
// changed under us between read and write, and the whole run must abort rather
// than skip one credential silently.
func TestConcurrentModificationAbortsTheRun(t *testing.T) {
	withKey(t, 73)
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(regexp.QuoteMeta(selectPattern)).
		WillReturnRows(credentialRows("tr_agent_plain"))
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE project_integrations SET agent_key")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	err = backfill(db, false, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "concurrent modification")
}

// The exit code has to reflect the end state, not the fact that the UPDATEs
// returned no error — otherwise a partial backfill reports success.
func TestPostCheckFailsWhenAPlaintextRowSurvives(t *testing.T) {
	withKey(t, 79)
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	plaintext := "tr_agent_plain"
	mock.ExpectQuery(regexp.QuoteMeta(selectPattern)).WillReturnRows(credentialRows(plaintext))
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE project_integrations SET agent_key")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// Re-read still shows plaintext — the run must not report success.
	mock.ExpectQuery(regexp.QuoteMeta(selectPattern)).WillReturnRows(credentialRows(plaintext))

	err = backfill(db, false, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still hold a plaintext credential")
}

func TestProjectFilterIsPassedToTheQuery(t *testing.T) {
	withKey(t, 83)
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(regexp.QuoteMeta(selectPattern)).
		WithArgs("proj-42").
		WillReturnRows(credentialRows())

	require.NoError(t, backfill(db, false, "proj-42"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBuildDSNPrefersDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://someone@example/db")
	assert.Equal(t, "postgres://someone@example/db", buildDSN())

	t.Setenv("DATABASE_URL", "")
	t.Setenv("DB_HOST", "dbhost")
	t.Setenv("DB_NAME", "dbname")
	dsn := buildDSN()
	assert.Contains(t, dsn, "host=dbhost")
	assert.Contains(t, dsn, "dbname=dbname")
}
