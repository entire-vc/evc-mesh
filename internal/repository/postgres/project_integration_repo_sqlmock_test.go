package postgres

import (
	"context"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/encryption"
)

// Whether the credential reaches Postgres encrypted is asserted here rather
// than only in the real-DB tests, because this is the assertion that must hold
// in the default test run: it is the one a reviewer of any future change to
// Upsert needs to see go red.
//
// The DB-side trigger and the round trip through a live Postgres are covered in
// project_integration_encryption_db_test.go (build tag `integration`).

func newProjectIntegrationRepoMock(t *testing.T) (*ProjectIntegrationRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewProjectIntegrationRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

func setEncryptionKey(t *testing.T, fill byte) {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = fill + byte(i)
	}
	encryption.ResetForTest()
	t.Setenv(encryption.EnvKey, base64.StdEncoding.EncodeToString(key))
	t.Cleanup(encryption.ResetForTest)
}

// capture is a sqlmock.Argument that accepts any string and records it, so a
// test can assert on the value the repository actually sent to the driver.
type capturingArg struct{ into *string }

func (c capturingArg) Match(v driver.Value) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	*c.into = s
	return true
}

func capture(into *string) sqlmock.Argument { return capturingArg{into: into} }

// trAgentKey builds a Team Relay agent key matching the shape Upsert
// requires ("tr_agent_" + 48 lowercase hex chars) from a single repeated hex
// digit, so each test case can stay visually distinct without hand-counting
// hex digits. hexDigit must be one of 0-9a-f.
func trAgentKey(hexDigit string) string {
	return "tr_agent_" + strings.Repeat(hexDigit, 48)
}

func sampleIntegration(agentKey string) *domain.ProjectIntegration {
	now := time.Now().UTC()
	return &domain.ProjectIntegration{
		ID:        uuid.New(),
		ProjectID: uuid.New(),
		Type:      "team_relay",
		Enabled:   true,
		Settings:  json.RawMessage(`{"share_slug":"probe"}`),
		AgentKey:  agentKey,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func TestUpsertSendsCiphertextToTheDatabase(t *testing.T) {
	setEncryptionKey(t, 91)
	repo, mock := newProjectIntegrationRepoMock(t)

	plaintext := trAgentKey("1")
	var captured string

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO project_integrations")).
		WithArgs(
			sqlmock.AnyArg(), sqlmock.AnyArg(), "team_relay", true, sqlmock.AnyArg(),
			// The sixth argument is the credential as it will be stored.
			// Capturing it is the whole point of this test.
			capture(&captured),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.Upsert(context.Background(), sampleIntegration(plaintext)))
	require.NoError(t, mock.ExpectationsWereMet())

	assert.NotEqual(t, plaintext, captured, "the plaintext credential must not reach the database")
	assert.True(t, encryption.IsEncrypted(captured), "stored value must carry the enc:v1: prefix")

	back, err := encryption.Decrypt(captured)
	require.NoError(t, err)
	assert.Equal(t, plaintext, back)
}

// The trigger rejects unencrypted credentials by default, so the one caller
// that legitimately cannot encrypt — an instance with no key configured — has
// to ask for the exemption explicitly, and only then.
func TestUpsertRequestsThePlaintextExemptionOnlyWhenItCannotEncrypt(t *testing.T) {
	t.Run("no key configured: exemption requested", func(t *testing.T) {
		encryption.ResetForTest()
		t.Setenv(encryption.EnvKey, "")
		t.Cleanup(encryption.ResetForTest)

		repo, mock := newProjectIntegrationRepoMock(t)
		mock.ExpectBegin()
		mock.ExpectExec(regexp.QuoteMeta("SET LOCAL mesh.allow_plaintext_agent_key")).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO project_integrations")).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()

		require.NoError(t, repo.Upsert(context.Background(), sampleIntegration(trAgentKey("2"))))
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("key configured: no exemption", func(t *testing.T) {
		setEncryptionKey(t, 97)
		repo, mock := newProjectIntegrationRepoMock(t)
		mock.ExpectBegin()
		// No SET LOCAL expected — an unexpected one fails the run.
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO project_integrations")).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()

		require.NoError(t, repo.Upsert(context.Background(), sampleIntegration(trAgentKey("3"))))
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("no credential at all: no exemption", func(t *testing.T) {
		setEncryptionKey(t, 101)
		repo, mock := newProjectIntegrationRepoMock(t)
		mock.ExpectBegin()
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO project_integrations")).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()

		require.NoError(t, repo.Upsert(context.Background(), sampleIntegration("")))
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestUpsertRollsBackWhenTheWriteFails(t *testing.T) {
	setEncryptionKey(t, 103)
	repo, mock := newProjectIntegrationRepoMock(t)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO project_integrations")).
		WillReturnError(errors.New("boom"))
	mock.ExpectRollback()

	err := repo.Upsert(context.Background(), sampleIntegration(trAgentKey("4")))
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Upsert must refuse a team_relay AgentKey that doesn't match the expected
// "tr_agent_" + 48 hex chars shape, and refuse it BEFORE opening a
// transaction — a ciphertext-shaped value (the f824032d double-encryption
// bug) must fail loudly here, on write, rather than silently landing in
// Postgres and surfacing as a 401 against Team Relay a day later.
func TestUpsertRejectsAMalformedTeamRelayAgentKey(t *testing.T) {
	setEncryptionKey(t, 131)
	repo, mock := newProjectIntegrationRepoMock(t)
	// No ExpectBegin: the malformed value must be caught before any DB call.

	cases := []string{
		"",                                    // handled separately (no-op), not this path
		"tr_agent_short",                      // too short
		"tr_agent_" + strings.Repeat("a", 47), // one char short
		"tr_agent_" + strings.Repeat("a", 49), // one char long
		"tr_agent_" + strings.Repeat("G", 48), // uppercase, not hex
		"not_even_the_right_prefix",
		// A ciphertext-shaped value — exactly the f824032d incident shape.
		"enc:v1:" + strings.Repeat("Q", 108),
	}
	for _, bad := range cases {
		if bad == "" {
			continue
		}
		err := repo.Upsert(context.Background(), sampleIntegration(bad))
		require.Error(t, err, "expected rejection for %q", bad)
		assert.Contains(t, err.Error(), "tr_agent_")
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpsertPropagatesABeginFailure(t *testing.T) {
	setEncryptionKey(t, 107)
	repo, mock := newProjectIntegrationRepoMock(t)

	mock.ExpectBegin().WillReturnError(errors.New("no connection"))

	require.Error(t, repo.Upsert(context.Background(), sampleIntegration(trAgentKey("5"))))
	require.NoError(t, mock.ExpectationsWereMet())
}

// toDomain must not hand a caller a value it failed to decrypt: downstream that
// becomes a 401 from Team Relay, which reads as "our credential was revoked"
// and sends the investigation to the wrong system.
func TestGetFailsRatherThanReturningAnUndecryptableCredential(t *testing.T) {
	setEncryptionKey(t, 109)
	sealed, err := encryption.Encrypt("tr_agent_written_under_the_old_key")
	require.NoError(t, err)

	// Rotate: the stored value can no longer be opened.
	setEncryptionKey(t, 211)
	repo, mock := newProjectIntegrationRepoMock(t)

	projID := uuid.New()
	now := time.Now().UTC()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, project_id, type, enabled, settings, agent_key")).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "project_id", "type", "enabled", "settings", "agent_key",
			"created_at", "updated_at", "created_by",
		}).AddRow(uuid.New(), projID, "team_relay", true, []byte(`{}`), sealed, now, now, nil))

	got, err := repo.Get(context.Background(), projID, "team_relay")
	require.ErrorIs(t, err, encryption.ErrUndecryptable)
	assert.Nil(t, got)
}

// Reading a row written before encryption was turned on must keep working, or
// deploying this would break every integration configured before the backfill.
func TestGetReadsLegacyPlaintextRow(t *testing.T) {
	setEncryptionKey(t, 113)
	repo, mock := newProjectIntegrationRepoMock(t)

	legacy := "tr_agent_written_before_encryption"
	projID := uuid.New()
	now := time.Now().UTC()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, project_id, type, enabled, settings, agent_key")).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "project_id", "type", "enabled", "settings", "agent_key",
			"created_at", "updated_at", "created_by",
		}).AddRow(uuid.New(), projID, "team_relay", true, []byte(`{}`), legacy, now, now, nil))

	got, err := repo.Get(context.Background(), projID, "team_relay")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, legacy, got.AgentKey)
}
