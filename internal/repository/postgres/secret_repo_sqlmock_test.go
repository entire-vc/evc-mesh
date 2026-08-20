package postgres

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/encryption"
)

// This file asserts the logic in secret_repo.go WITHOUT a real Postgres, so
// it runs in the default (non -tags=integration) test job — the one CI's
// diff-coverage gate measures. The DB-side trigger and the round trip
// through a live Postgres are covered separately in secret_repo_db_test.go
// (build tag `integration`), the same split project_integration_repo uses.

func newSecretRepoMock(t *testing.T) (*SecretRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewSecretRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

// capturingArg / capture / setEncryptionKey are shared package-level test
// helpers already defined in project_integration_repo_sqlmock_test.go —
// reused here rather than redeclared.

var secretMaskedRowCols = []string{
	"id", "workspace_id", "project_id", "agent_id", "scope", "name",
	"value_sha256_prefix", "value_length", "value_char_class", "expires_at",
	"created_by", "created_by_type", "created_at", "rotated_at",
}

func maskedRow(id, wsID uuid.UUID, name string) *sqlmock.Rows {
	now := time.Now()
	return sqlmock.NewRows(secretMaskedRowCols).AddRow(
		id, wsID, nil, nil, "workspace", name,
		"abcd1234", 12, "a-z+0-9", nil,
		uuid.New(), "agent", now, nil,
	)
}

// --- classify / fingerprint : pure functions, exercised directly so every
// branch shows up in the diff-coverage report regardless of which
// higher-level test happens to route through it. ---

func TestClassify_AllFourBucketsAndEmpty(t *testing.T) {
	assert.Equal(t, "a-z", classify("abc"))
	assert.Equal(t, "A-Z", classify("ABC"))
	assert.Equal(t, "0-9", classify("123"))
	assert.Equal(t, "sym", classify("!@#"))
	assert.Equal(t, "a-z+A-Z+0-9+sym", classify("aB3!"))
	assert.Equal(t, "?", classify(""))
}

func TestFingerprint_Is8HexChars(t *testing.T) {
	fp := fingerprint("some-secret-value")
	assert.Len(t, fp, 8)
	assert.Regexp(t, "^[0-9a-f]{8}$", fp)
	// Deterministic: same input, same fingerprint.
	assert.Equal(t, fp, fingerprint("some-secret-value"))
	// Different input, (almost certainly) different fingerprint.
	assert.NotEqual(t, fp, fingerprint("a-different-value"))
}

// --- scopeRefColumns validation, exercised through Create/Rotate so the
// early-return paths (no DB call reached) are covered as they are actually
// used, not just as a standalone unit. ---

func TestCreate_RejectsWorkspaceScopeWithProjectID(t *testing.T) {
	repo, _ := newSecretRepoMock(t)
	proj := uuid.New()
	_, err := repo.Create(context.Background(), domain.CreateSecretInput{
		WorkspaceID: uuid.New(), Scope: domain.SecretScopeWorkspace, ProjectID: &proj,
		Name: "X", Value: "v", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeAgent,
	})
	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
}

func TestCreate_RejectsProjectScopeWithoutProjectID(t *testing.T) {
	repo, _ := newSecretRepoMock(t)
	_, err := repo.Create(context.Background(), domain.CreateSecretInput{
		WorkspaceID: uuid.New(), Scope: domain.SecretScopeProject,
		Name: "X", Value: "v", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeAgent,
	})
	require.Error(t, err)
}

func TestCreate_RejectsAgentScopeWithoutAgentID(t *testing.T) {
	repo, _ := newSecretRepoMock(t)
	_, err := repo.Create(context.Background(), domain.CreateSecretInput{
		WorkspaceID: uuid.New(), Scope: domain.SecretScopeAgent,
		Name: "X", Value: "v", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeAgent,
	})
	require.Error(t, err)
}

func TestCreate_RejectsUnknownScope(t *testing.T) {
	repo, _ := newSecretRepoMock(t)
	_, err := repo.Create(context.Background(), domain.CreateSecretInput{
		WorkspaceID: uuid.New(), Scope: domain.SecretScope("bogus"),
		Name: "X", Value: "v", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeAgent,
	})
	require.Error(t, err)
}

// --- Create ---

func TestCreate_SendsCiphertextToTheDatabase(t *testing.T) {
	setEncryptionKey(t, 7)
	repo, mock := newSecretRepoMock(t)
	wsID := uuid.New()
	plaintext := "ghp_supersecretvalue123"
	var captured string

	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO secrets")).
		WithArgs(
			wsID, nil, nil, "workspace", "GH_TOKEN",
			capture(&captured), // encrypted_value: the whole point of this test
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(),
		).
		WillReturnRows(maskedRow(uuid.New(), wsID, "GH_TOKEN"))

	_, err := repo.Create(context.Background(), domain.CreateSecretInput{
		WorkspaceID: wsID, Scope: domain.SecretScopeWorkspace,
		Name: "GH_TOKEN", Value: plaintext,
		CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeAgent,
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())

	assert.NotEqual(t, plaintext, captured)
	assert.Regexp(t, `^enc:v1:`, captured)
	plain, err := encryption.Decrypt(captured)
	require.NoError(t, err)
	assert.Equal(t, plaintext, plain)
}

func TestCreate_UniqueViolationBecomesConflict(t *testing.T) {
	setEncryptionKey(t, 8)
	repo, mock := newSecretRepoMock(t)
	wsID := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO secrets")).
		WillReturnError(&pq.Error{Code: "23505", Constraint: "uq_secrets_ws_name"})

	_, err := repo.Create(context.Background(), domain.CreateSecretInput{
		WorkspaceID: wsID, Scope: domain.SecretScopeWorkspace,
		Name: "DUP", Value: "v", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeAgent,
	})
	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, apierror.Conflict("x").Code, apiErr.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCreate_NonUniqueDBErrorPropagates(t *testing.T) {
	setEncryptionKey(t, 9)
	repo, mock := newSecretRepoMock(t)
	wsID := uuid.New()
	wantErr := errors.New("connection reset")

	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO secrets")).WillReturnError(wantErr)

	_, err := repo.Create(context.Background(), domain.CreateSecretInput{
		WorkspaceID: wsID, Scope: domain.SecretScopeWorkspace,
		Name: "X", Value: "v", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeAgent,
	})
	require.Error(t, err)
	var apiErr *apierror.Error
	assert.False(t, errors.As(err, &apiErr), "a non-constraint DB error must not be reshaped into an apierror")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCreate_EncryptErrorPropagatesBeforeAnyQuery(t *testing.T) {
	// No key configured AND EnvRequired set -> Encrypt returns ErrKeyRequired
	// without the repo ever touching the database.
	encryption.ResetForTest()
	t.Setenv(encryption.EnvRequired, "true")
	t.Cleanup(encryption.ResetForTest)
	repo, mock := newSecretRepoMock(t)

	_, err := repo.Create(context.Background(), domain.CreateSecretInput{
		WorkspaceID: uuid.New(), Scope: domain.SecretScopeWorkspace,
		Name: "X", Value: "v", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeAgent,
	})
	require.ErrorIs(t, err, encryption.ErrKeyRequired)
	require.NoError(t, mock.ExpectationsWereMet(), "no query should have been issued")
}

// --- Rotate ---

func TestRotate_SupersedesThenInsertsInOneTransaction(t *testing.T) {
	setEncryptionKey(t, 10)
	repo, mock := newSecretRepoMock(t)
	wsID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE secrets SET rotated_at = now()")).
		WithArgs(wsID, "ROTATE_ME").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO secrets")).
		WillReturnRows(maskedRow(uuid.New(), wsID, "ROTATE_ME"))
	mock.ExpectCommit()

	_, err := repo.Rotate(context.Background(), wsID, domain.SecretScopeWorkspace, nil, nil, "ROTATE_ME",
		domain.CreateSecretInput{Value: "new-value", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeAgent})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRotate_NoCurrentRowReturnsNotFoundAndRollsBack(t *testing.T) {
	setEncryptionKey(t, 11)
	repo, mock := newSecretRepoMock(t)
	wsID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE secrets SET rotated_at = now()")).
		WillReturnResult(sqlmock.NewResult(0, 0)) // zero rows affected
	mock.ExpectRollback()

	_, err := repo.Rotate(context.Background(), wsID, domain.SecretScopeWorkspace, nil, nil, "NEVER_EXISTED",
		domain.CreateSecretInput{Value: "v", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeAgent})
	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, apierror.NotFound("x").Code, apiErr.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRotate_InsertFailureRollsBack(t *testing.T) {
	setEncryptionKey(t, 12)
	repo, mock := newSecretRepoMock(t)
	wsID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE secrets SET rotated_at = now()")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO secrets")).
		WillReturnError(errors.New("disk full"))
	mock.ExpectRollback()

	_, err := repo.Rotate(context.Background(), wsID, domain.SecretScopeWorkspace, nil, nil, "X",
		domain.CreateSecretInput{Value: "v", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeAgent})
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRotate_ScopeMismatchRejectedBeforeAnyQuery(t *testing.T) {
	repo, mock := newSecretRepoMock(t)
	proj := uuid.New()
	_, err := repo.Rotate(context.Background(), uuid.New(), domain.SecretScopeWorkspace, &proj, nil, "X",
		domain.CreateSecretInput{Value: "v", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeAgent})
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// --- Delete ---

func TestDelete_Success(t *testing.T) {
	repo, mock := newSecretRepoMock(t)
	wsID := uuid.New()

	mock.ExpectExec(regexp.QuoteMeta("UPDATE secrets SET rotated_at = now()")).
		WithArgs(wsID, "DELETE_ME").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := repo.Delete(context.Background(), wsID, domain.SecretScopeWorkspace, nil, nil, "DELETE_ME", uuid.New(), domain.ActorTypeAgent)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDelete_NoCurrentRowReturnsNotFound(t *testing.T) {
	repo, mock := newSecretRepoMock(t)
	wsID := uuid.New()

	mock.ExpectExec(regexp.QuoteMeta("UPDATE secrets SET rotated_at = now()")).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := repo.Delete(context.Background(), wsID, domain.SecretScopeWorkspace, nil, nil, "NEVER_EXISTED", uuid.New(), domain.ActorTypeAgent)
	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
}

func TestDelete_ProjectScopeUsesProjectIDInWhereClause(t *testing.T) {
	repo, mock := newSecretRepoMock(t)
	wsID, projID := uuid.New(), uuid.New()

	mock.ExpectExec(regexp.QuoteMeta("UPDATE secrets SET rotated_at = now()")).
		WithArgs(wsID, projID, "X").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := repo.Delete(context.Background(), wsID, domain.SecretScopeProject, &projID, nil, "X", uuid.New(), domain.ActorTypeAgent)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDelete_AgentScopeUsesAgentIDInWhereClause(t *testing.T) {
	repo, mock := newSecretRepoMock(t)
	wsID, agentID := uuid.New(), uuid.New()

	mock.ExpectExec(regexp.QuoteMeta("UPDATE secrets SET rotated_at = now()")).
		WithArgs(wsID, agentID, "X").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := repo.Delete(context.Background(), wsID, domain.SecretScopeAgent, nil, &agentID, "X", uuid.New(), domain.ActorTypeAgent)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// --- ListCurrent ---

func TestListCurrent_ReturnsMaskedRows(t *testing.T) {
	repo, mock := newSecretRepoMock(t)
	wsID := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT")).
		WithArgs(wsID, nil, nil).
		WillReturnRows(maskedRow(uuid.New(), wsID, "A").AddRow(
			uuid.New(), wsID, nil, nil, "workspace", "B",
			"11112222", 5, "a-z", nil, uuid.New(), "agent", time.Now(), nil,
		))

	list, err := repo.ListCurrent(context.Background(), wsID, nil, nil)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, "A", list[0].Name)
	assert.Equal(t, "B", list[1].Name)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestListCurrent_QueryErrorPropagates(t *testing.T) {
	repo, mock := newSecretRepoMock(t)
	wsID := uuid.New()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT")).WillReturnError(errors.New("timeout"))

	_, err := repo.ListCurrent(context.Background(), wsID, nil, nil)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// --- ResolveCurrentValues ---

func TestResolveCurrentValues_DecryptsLiveAndFlagsExpiredWithoutDecrypting(t *testing.T) {
	setEncryptionKey(t, 13)
	repo, mock := newSecretRepoMock(t)
	wsID := uuid.New()

	live, err := encryption.Encrypt("live-plaintext")
	require.NoError(t, err)
	expiredCipher, err := encryption.Encrypt("expired-plaintext")
	require.NoError(t, err)
	past := time.Now().Add(-time.Hour)

	rows := sqlmock.NewRows([]string{"name", "encrypted_value", "expires_at"}).
		AddRow("LIVE", live, nil).
		AddRow("EXPIRED", expiredCipher, past)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name, encrypted_value, expires_at")).
		WithArgs(wsID, nil, nil).
		WillReturnRows(rows)

	resolved, err := repo.ResolveCurrentValues(context.Background(), wsID, nil, nil)
	require.NoError(t, err)
	require.Len(t, resolved, 2)

	byName := map[string]domain.MaterializedSecret{}
	for _, m := range resolved {
		byName[m.Name] = m
	}
	assert.False(t, byName["LIVE"].Expired)
	assert.Equal(t, "live-plaintext", byName["LIVE"].Value)
	assert.True(t, byName["EXPIRED"].Expired)
	assert.Empty(t, byName["EXPIRED"].Value)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestResolveCurrentValues_UndecryptableValueReturnsNamedError(t *testing.T) {
	setEncryptionKey(t, 14)
	repo, mock := newSecretRepoMock(t)
	wsID := uuid.New()

	rows := sqlmock.NewRows([]string{"name", "encrypted_value", "expires_at"}).
		AddRow("BROKEN", "enc:v1:not-valid-base64-or-wrong-key", nil)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name, encrypted_value, expires_at")).
		WillReturnRows(rows)

	_, err := repo.ResolveCurrentValues(context.Background(), wsID, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BROKEN", "the error must name which secret failed to decrypt")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestResolveCurrentValues_QueryErrorPropagates(t *testing.T) {
	repo, mock := newSecretRepoMock(t)
	wsID := uuid.New()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name, encrypted_value, expires_at")).
		WillReturnError(errors.New("connection lost"))

	_, err := repo.ResolveCurrentValues(context.Background(), wsID, nil, nil)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// --- isUniqueViolation ---

func TestIsUniqueViolation(t *testing.T) {
	assert.True(t, isUniqueViolation(&pq.Error{Code: "23505"}))
	assert.False(t, isUniqueViolation(&pq.Error{Code: "23503"}))
	assert.False(t, isUniqueViolation(errors.New("not a pq error")))
	assert.False(t, isUniqueViolation(nil))
}
