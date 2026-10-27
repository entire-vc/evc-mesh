package postgres

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/entire-vc/evc-mesh/internal/extensions/secretstore"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// This file asserts the logic in secret_ref_repo.go WITHOUT a real Postgres, so
// it runs in the default (non -tags=integration) test job — the one CI's
// diff-coverage gate measures. The table's constraints and the round trip
// through a live Postgres are covered separately in secret_ref_repo_db_test.go
// (build tag `integration`), the same split project_integration_repo uses.

func newSecretRepoMock(t *testing.T) (*SecretRefRepo, sqlmock.Sqlmock) {
	t.Helper()
	return newSecretRefRepoWithKeep(t, newFakeKeep())
}

// capturingArg / capture are shared package-level test helpers already
// defined in project_integration_repo_sqlmock_test.go — reused here rather
// than redeclared.

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

// fakeKeep is the Keep provider at its boundary: it records what it was asked
// to store and answers resolves from that map.
type fakeKeep struct {
	puts    map[string]string // stored ref -> value
	putErr  error
	resolve map[string]secretstore.Result
}

func newFakeKeep() *fakeKeep {
	return &fakeKeep{puts: map[string]string{}, resolve: map[string]secretstore.Result{}}
}

func (f *fakeKeep) Put(_ context.Context, ref secretstore.Ref, plain string) (string, error) {
	if f.putErr != nil {
		return "", f.putErr
	}
	stored := secretstore.KeepStoredRef(ref)
	f.puts[stored] = plain
	return stored, nil
}

func (f *fakeKeep) Resolve(_ context.Context, stored string) secretstore.Result {
	if r, ok := f.resolve[stored]; ok {
		return r
	}
	if v, ok := f.puts[stored]; ok {
		return secretstore.Result{Value: v}
	}
	return secretstore.Result{Degraded: true, Reason: "keep unavailable: " + secretstore.ErrKeepNotFound.Error()}
}

func newSecretRefRepoWithKeep(t *testing.T, keep secretstore.Store) (*SecretRefRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewSecretRefRepo(sqlx.NewDb(rawDB, "postgres"), keep), mock
}

func TestCreate_ValueGoesToKeepAndOnlyTheRefToTheDatabase(t *testing.T) {
	keep := newFakeKeep()
	repo, mock := newSecretRefRepoWithKeep(t, keep)
	wsID := uuid.New()
	plaintext := "ghp_supersecretvalue123"
	want := "keep:mesh-secret.workspace.workspace.GH_TOKEN." + wsID.String()
	var captured string

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO secret_refs")).
		WithArgs(
			wsID, nil, nil, "workspace", "GH_TOKEN",
			capture(&captured), // keep_ref: the only thing that names the value
			fingerprint(plaintext), len(plaintext), classify(plaintext), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(),
		).
		WillReturnRows(maskedRow(uuid.New(), wsID, "GH_TOKEN"))
	mock.ExpectCommit()

	_, err := repo.Create(context.Background(), domain.CreateSecretInput{
		WorkspaceID: wsID, Scope: domain.SecretScopeWorkspace,
		Name: "GH_TOKEN", Value: plaintext,
		CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeAgent,
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())

	assert.Equal(t, want, captured)
	assert.Equal(t, plaintext, keep.puts[want])
}

func TestCreate_AgentScopeRefNamesTheAgent(t *testing.T) {
	keep := newFakeKeep()
	repo, mock := newSecretRefRepoWithKeep(t, keep)
	wsID, agentID := uuid.New(), uuid.New()
	want := "keep:mesh-secret.agent." + agentID.String() + ".GH_TOKEN." + wsID.String()

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO secret_refs")).
		WithArgs(wsID, nil, agentID, "agent", "GH_TOKEN", want,
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(maskedRow(uuid.New(), wsID, "GH_TOKEN"))
	mock.ExpectCommit()

	_, err := repo.Create(context.Background(), domain.CreateSecretInput{
		WorkspaceID: wsID, AgentID: &agentID, Scope: domain.SecretScopeAgent,
		Name: "GH_TOKEN", Value: "v", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
	assert.Equal(t, "v", keep.puts[want])
}

// A duplicate name must not reach Keep: its ref is the current secret's ref,
// and a PUT would silently replace that secret's value.
func TestCreate_UniqueViolationBecomesConflictWithoutTouchingKeep(t *testing.T) {
	keep := newFakeKeep()
	repo, mock := newSecretRefRepoWithKeep(t, keep)
	wsID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO secret_refs")).
		WillReturnError(&pq.Error{Code: "23505", Constraint: "uq_secret_refs_ws_name"})
	mock.ExpectRollback()

	_, err := repo.Create(context.Background(), domain.CreateSecretInput{
		WorkspaceID: wsID, Scope: domain.SecretScopeWorkspace,
		Name: "DUP", Value: "v", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeAgent,
	})
	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, apierror.Conflict("x").Code, apiErr.Code)
	assert.Empty(t, keep.puts)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCreate_NonUniqueDBErrorPropagates(t *testing.T) {
	repo, mock := newSecretRefRepoWithKeep(t, newFakeKeep())
	wantErr := errors.New("connection reset")

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO secret_refs")).WillReturnError(wantErr)
	mock.ExpectRollback()

	_, err := repo.Create(context.Background(), domain.CreateSecretInput{
		WorkspaceID: uuid.New(), Scope: domain.SecretScopeWorkspace,
		Name: "X", Value: "v", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeAgent,
	})
	require.Error(t, err)
	var apiErr *apierror.Error
	assert.False(t, errors.As(err, &apiErr), "a non-constraint DB error must not be reshaped into an apierror")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCreate_KeepFailureRollsTheRowBackAs503(t *testing.T) {
	keep := newFakeKeep()
	keep.putErr = errors.New("keep: put: status 503")
	repo, mock := newSecretRefRepoWithKeep(t, keep)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO secret_refs")).
		WillReturnRows(maskedRow(uuid.New(), uuid.New(), "X"))
	mock.ExpectRollback()

	_, err := repo.Create(context.Background(), domain.CreateSecretInput{
		WorkspaceID: uuid.New(), Scope: domain.SecretScopeWorkspace,
		Name: "X", Value: "v", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeAgent,
	})
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, apierror.ServiceUnavailable("x").Code, apiErr.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCreate_NameTooLongForAKeepRefIsAValidationError(t *testing.T) {
	keep := newFakeKeep()
	keep.putErr = fmt.Errorf("keep: put: %w", secretstore.ErrKeepBadID)
	repo, mock := newSecretRefRepoWithKeep(t, keep)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO secret_refs")).
		WillReturnRows(maskedRow(uuid.New(), uuid.New(), "X"))
	mock.ExpectRollback()

	_, err := repo.Create(context.Background(), domain.CreateSecretInput{
		WorkspaceID: uuid.New(), Scope: domain.SecretScopeWorkspace,
		Name: "X", Value: "v", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeAgent,
	})
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, apierror.ValidationError(nil).Code, apiErr.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Without Keep there is no place for a value: the write is refused, and no
// builtin ciphertext is produced as a fallback.
func TestCreate_WithoutKeepIsRefusedAs503(t *testing.T) {
	repo, mock := newSecretRefRepoWithKeep(t, nil)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO secret_refs")).
		WillReturnRows(maskedRow(uuid.New(), uuid.New(), "X"))
	mock.ExpectRollback()

	_, err := repo.Create(context.Background(), domain.CreateSecretInput{
		WorkspaceID: uuid.New(), Scope: domain.SecretScopeWorkspace,
		Name: "X", Value: "v", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeAgent,
	})
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, apierror.ServiceUnavailable("x").Code, apiErr.Code)
	assert.Contains(t, apiErr.Message, "Keep is not configured")
	require.NoError(t, mock.ExpectationsWereMet())
}

// --- Rotate ---

func TestRotate_SupersedesInsertsAndPutsANewKeepVersionBeforeCommit(t *testing.T) {
	keep := newFakeKeep()
	repo, mock := newSecretRefRepoWithKeep(t, keep)
	wsID := uuid.New()
	want := "keep:mesh-secret.workspace.workspace.ROTATE_ME." + wsID.String()
	keep.puts[want] = "old-value"

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE secret_refs SET rotated_at = now()")).
		WithArgs(wsID, "ROTATE_ME").
		WillReturnRows(sqlmock.NewRows([]string{"keep_ref"}).AddRow(want))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO secret_refs")).
		WithArgs(wsID, nil, nil, "workspace", "ROTATE_ME", want,
			fingerprint("new-value"), len("new-value"), classify("new-value"), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(maskedRow(uuid.New(), wsID, "ROTATE_ME"))
	mock.ExpectCommit()

	_, err := repo.Rotate(context.Background(), wsID, domain.SecretScopeWorkspace, nil, nil, "ROTATE_ME",
		domain.CreateSecretInput{Value: "new-value", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeAgent})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
	assert.Equal(t, "new-value", keep.puts[want])
}

func TestRotate_NoCurrentRowReturnsNotFoundAndRollsBack(t *testing.T) {
	keep := newFakeKeep()
	repo, mock := newSecretRefRepoWithKeep(t, keep)
	wsID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE secret_refs SET rotated_at = now()")).
		WillReturnRows(sqlmock.NewRows([]string{"keep_ref"})) // no current row
	mock.ExpectRollback()

	_, err := repo.Rotate(context.Background(), wsID, domain.SecretScopeWorkspace, nil, nil, "NEVER_EXISTED",
		domain.CreateSecretInput{Value: "v", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeAgent})
	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, apierror.NotFound("x").Code, apiErr.Code)
	assert.Empty(t, keep.puts)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRotate_InsertFailureRollsBack(t *testing.T) {
	keep := newFakeKeep()
	repo, mock := newSecretRefRepoWithKeep(t, keep)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE secret_refs SET rotated_at = now()")).
		WillReturnRows(sqlmock.NewRows([]string{"keep_ref"}).AddRow("keep:mesh-secret.workspace.workspace.X.migrated-elsewhere"))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO secret_refs")).
		WillReturnError(errors.New("disk full"))
	mock.ExpectRollback()

	_, err := repo.Rotate(context.Background(), uuid.New(), domain.SecretScopeWorkspace, nil, nil, "X",
		domain.CreateSecretInput{Value: "v", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeAgent})
	require.Error(t, err)
	assert.Empty(t, keep.puts)
	require.NoError(t, mock.ExpectationsWereMet())
}

// A failed PUT keeps the old row current: the supersede is rolled back.
func TestRotate_KeepFailureLeavesTheOldRowCurrent(t *testing.T) {
	keep := newFakeKeep()
	keep.putErr = errors.New("keep: put: status 503")
	repo, mock := newSecretRefRepoWithKeep(t, keep)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE secret_refs SET rotated_at = now()")).
		WillReturnRows(sqlmock.NewRows([]string{"keep_ref"}).AddRow("keep:mesh-secret.workspace.workspace.X.migrated-elsewhere"))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO secret_refs")).
		WillReturnRows(maskedRow(uuid.New(), uuid.New(), "X"))
	mock.ExpectRollback()

	_, err := repo.Rotate(context.Background(), uuid.New(), domain.SecretScopeWorkspace, nil, nil, "X",
		domain.CreateSecretInput{Value: "v", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeAgent})
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, apierror.ServiceUnavailable("x").Code, apiErr.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Keep has no transaction: a commit that fails after the PUT gives Keep the
// previous value back, so Keep and the still-current old row agree.
func TestRotate_CommitFailureGivesKeepThePreviousValueBack(t *testing.T) {
	keep := newFakeKeep()
	repo, mock := newSecretRefRepoWithKeep(t, keep)
	wsID := uuid.New()
	ref := "keep:mesh-secret.workspace.workspace.ROTATE_ME." + wsID.String()
	keep.puts[ref] = "old-value"

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE secret_refs SET rotated_at = now()")).
		WillReturnRows(sqlmock.NewRows([]string{"keep_ref"}).AddRow(ref))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO secret_refs")).
		WillReturnRows(maskedRow(uuid.New(), wsID, "ROTATE_ME"))
	mock.ExpectCommit().WillReturnError(errors.New("connection reset"))

	_, err := repo.Rotate(context.Background(), wsID, domain.SecretScopeWorkspace, nil, nil, "ROTATE_ME",
		domain.CreateSecretInput{Value: "new-value", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser})
	require.ErrorContains(t, err, "connection reset")
	assert.Equal(t, "old-value", keep.puts[ref], "Keep is back on the value the current row describes")
	require.NoError(t, mock.ExpectationsWereMet())
}

// When the previous value cannot be given back either, the error says what
// Keep now serves instead of passing for an ordinary failure.
func TestRotate_CommitFailureWithKeepGoneNamesTheState(t *testing.T) {
	keep := &putOnceKeep{fakeKeep: newFakeKeep()}
	repo, mock := newSecretRefRepoWithKeep(t, keep)
	wsID := uuid.New()
	ref := "keep:mesh-secret.workspace.workspace.ROTATE_ME." + wsID.String()
	keep.puts[ref] = "old-value"

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE secret_refs SET rotated_at = now()")).
		WillReturnRows(sqlmock.NewRows([]string{"keep_ref"}).AddRow(ref))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO secret_refs")).
		WillReturnRows(maskedRow(uuid.New(), wsID, "ROTATE_ME"))
	mock.ExpectCommit().WillReturnError(errors.New("connection reset"))

	_, err := repo.Rotate(context.Background(), wsID, domain.SecretScopeWorkspace, nil, nil, "ROTATE_ME",
		domain.CreateSecretInput{Value: "new-value", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser})
	require.ErrorContains(t, err, "rotate it again")
	assert.NotContains(t, err.Error(), "new-value")
	require.NoError(t, mock.ExpectationsWereMet())
}

// A rotation that could not be undone is not started.
func TestRotate_UnreadableCurrentValueStopsBeforeThePut(t *testing.T) {
	keep := newFakeKeep()
	wsID := uuid.New()
	ref := "keep:mesh-secret.workspace.workspace.ROTATE_ME." + wsID.String()
	keep.resolve[ref] = secretstore.Result{Degraded: true, Reason: "keep unavailable"}
	repo, mock := newSecretRefRepoWithKeep(t, keep)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE secret_refs SET rotated_at = now()")).
		WillReturnRows(sqlmock.NewRows([]string{"keep_ref"}).AddRow(ref))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO secret_refs")).
		WillReturnRows(maskedRow(uuid.New(), wsID, "ROTATE_ME"))
	mock.ExpectRollback()

	_, err := repo.Rotate(context.Background(), wsID, domain.SecretScopeWorkspace, nil, nil, "ROTATE_ME",
		domain.CreateSecretInput{Value: "new-value", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser})
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, apierror.ServiceUnavailable("x").Code, apiErr.Code)
	assert.Empty(t, keep.puts)
	require.NoError(t, mock.ExpectationsWereMet())
}

// A row whose keep_ref differs from the derived one (written under an older
// naming) is still read before the PUT: an unreadable current value refuses
// the rotation even though the PUT would not overwrite it.
func TestRotate_UnreadableCurrentValueUnderAnotherRefStopsBeforeThePut(t *testing.T) {
	keep := newFakeKeep()
	wsID := uuid.New()
	oldRef := "keep:legacy.ROTATE_ME." + wsID.String()
	keep.resolve[oldRef] = secretstore.Result{Degraded: true, Reason: "keep unavailable"}
	repo, mock := newSecretRefRepoWithKeep(t, keep)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE secret_refs SET rotated_at = now()")).
		WillReturnRows(sqlmock.NewRows([]string{"keep_ref"}).AddRow(oldRef))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO secret_refs")).
		WillReturnRows(maskedRow(uuid.New(), wsID, "ROTATE_ME"))
	mock.ExpectRollback()

	_, err := repo.Rotate(context.Background(), wsID, domain.SecretScopeWorkspace, nil, nil, "ROTATE_ME",
		domain.CreateSecretInput{Value: "new-value", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser})
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, apierror.ServiceUnavailable("x").Code, apiErr.Code)
	assert.Empty(t, keep.puts)
	require.NoError(t, mock.ExpectationsWereMet())
}

// putOnceKeep accepts the first PUT and refuses every later one.
type putOnceKeep struct {
	*fakeKeep
	n int
}

func (k *putOnceKeep) Put(ctx context.Context, ref secretstore.Ref, plain string) (string, error) {
	k.n++
	if k.n > 1 {
		return "", errors.New("keep: status 503")
	}
	return k.fakeKeep.Put(ctx, ref, plain)
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

	mock.ExpectExec(regexp.QuoteMeta("UPDATE secret_refs SET rotated_at = now()")).
		WithArgs(wsID, "DELETE_ME").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := repo.Delete(context.Background(), wsID, domain.SecretScopeWorkspace, nil, nil, "DELETE_ME", uuid.New(), domain.ActorTypeAgent)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDelete_NoCurrentRowReturnsNotFound(t *testing.T) {
	repo, mock := newSecretRepoMock(t)
	wsID := uuid.New()

	mock.ExpectExec(regexp.QuoteMeta("UPDATE secret_refs SET rotated_at = now()")).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := repo.Delete(context.Background(), wsID, domain.SecretScopeWorkspace, nil, nil, "NEVER_EXISTED", uuid.New(), domain.ActorTypeAgent)
	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
}

func TestDelete_ProjectScopeUsesProjectIDInWhereClause(t *testing.T) {
	repo, mock := newSecretRepoMock(t)
	wsID, projID := uuid.New(), uuid.New()

	mock.ExpectExec(regexp.QuoteMeta("UPDATE secret_refs SET rotated_at = now()")).
		WithArgs(wsID, projID, "X").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := repo.Delete(context.Background(), wsID, domain.SecretScopeProject, &projID, nil, "X", uuid.New(), domain.ActorTypeAgent)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDelete_AgentScopeUsesAgentIDInWhereClause(t *testing.T) {
	repo, mock := newSecretRepoMock(t)
	wsID, agentID := uuid.New(), uuid.New()

	mock.ExpectExec(regexp.QuoteMeta("UPDATE secret_refs SET rotated_at = now()")).
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

var resolveCols = []string{"id", "name", "keep_ref", "value_sha256_prefix", "expires_at"}

const resolveQuery = "SELECT id, name, keep_ref, value_sha256_prefix, expires_at FROM secret_refs"

func TestResolveCurrentValues_ReadsLiveFromKeepAndFlagsExpiredWithoutReading(t *testing.T) {
	keep := newFakeKeep()
	keep.puts["keep:live"] = "live-plaintext"
	// An expired row must not be read from Keep at all.
	keep.resolve["keep:expired"] = secretstore.Result{Degraded: true, Reason: "must not be asked"}
	repo, mock := newSecretRefRepoWithKeep(t, keep)
	wsID := uuid.New()
	past := time.Now().Add(-time.Hour)

	liveID, expiredID := uuid.New(), uuid.New()
	rows := sqlmock.NewRows(resolveCols).
		AddRow(liveID, "LIVE", "keep:live", "1a2b3c4d", nil).
		AddRow(expiredID, "EXPIRED", "keep:expired", "5e6f7a8b", past)
	mock.ExpectQuery(regexp.QuoteMeta(resolveQuery)).
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
	// The version identity rides along for the issuance audit.
	// A live row carries the fingerprint of what Keep returned, not the one
	// stored at write time; an expired one keeps the stored fingerprint.
	assert.Equal(t, liveID, byName["LIVE"].ID)
	assert.Equal(t, fingerprint("live-plaintext"), byName["LIVE"].ValueSHA256Prefix)
	assert.Equal(t, expiredID, byName["EXPIRED"].ID)
	assert.Equal(t, "5e6f7a8b", byName["EXPIRED"].ValueSHA256Prefix)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestResolveCurrentValues_QueryErrorPropagates(t *testing.T) {
	repo, mock := newSecretRefRepoWithKeep(t, newFakeKeep())
	mock.ExpectQuery(regexp.QuoteMeta(resolveQuery)).
		WillReturnError(errors.New("connection lost"))

	_, err := repo.ResolveCurrentValues(context.Background(), uuid.New(), nil, nil)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestResolveCurrentValues_KeepUnavailableIsDegradedErrorNotEmptyValue(t *testing.T) {
	keep := newFakeKeep()
	keep.resolve["keep:m"] = secretstore.Result{Degraded: true, Reason: "keep unavailable: status 503"}
	repo, mock := newSecretRefRepoWithKeep(t, keep)
	rows := sqlmock.NewRows(resolveCols).AddRow(uuid.New(), "MIGRATED", "keep:m", "aaaaaaaa", nil)
	mock.ExpectQuery(regexp.QuoteMeta(resolveQuery)).WillReturnRows(rows)

	got, err := repo.ResolveCurrentValues(context.Background(), uuid.New(), nil, nil)
	require.Error(t, err)
	assert.Nil(t, got)
	var degraded *domain.SecretDegradedError
	require.ErrorAs(t, err, &degraded)
	assert.Equal(t, "MIGRATED", degraded.Name)
	assert.Contains(t, degraded.Reason, "keep unavailable")
	assert.Contains(t, degraded.Reason, "503")
}

func TestResolveCurrentValues_WithoutKeepIsDegradedNotEmpty(t *testing.T) {
	repo, mock := newSecretRefRepoWithKeep(t, nil)
	rows := sqlmock.NewRows(resolveCols).AddRow(uuid.New(), "MIGRATED", "keep:m", "aaaaaaaa", nil)
	mock.ExpectQuery(regexp.QuoteMeta(resolveQuery)).WillReturnRows(rows)

	_, err := repo.ResolveCurrentValues(context.Background(), uuid.New(), nil, nil)
	var degraded *domain.SecretDegradedError
	require.ErrorAs(t, err, &degraded)
	assert.Equal(t, secretstore.ErrKeepNotConfigured.Error(), degraded.Reason)
}

// --- isSecretsUniqueViolation ---

func TestIsUniqueViolation(t *testing.T) {
	assert.True(t, isSecretsUniqueViolation(&pq.Error{Code: "23505"}))
	assert.False(t, isSecretsUniqueViolation(&pq.Error{Code: "23503"}))
	assert.False(t, isSecretsUniqueViolation(errors.New("not a pq error")))
	assert.False(t, isSecretsUniqueViolation(nil))
}

// --- GetByID (S3): masked lookup by row id, scoped to a workspace ---

func TestSecretRepo_GetByID_SelectsNoValueColumnAndScopesByWorkspace(t *testing.T) {
	repo, mock := newSecretRepoMock(t)
	id, wsID := uuid.New(), uuid.New()

	var gotQuery string
	mock.ExpectQuery(`SELECT .* FROM secret_refs WHERE id = \$1 AND workspace_id = \$2`).
		WithArgs(id, wsID).
		WillReturnRows(maskedRow(id, wsID, "GITHUB_TOKEN"))

	got, err := repo.GetByID(context.Background(), wsID, id)
	require.NoError(t, err)
	assert.Equal(t, "GITHUB_TOKEN", got.Name)
	assert.Equal(t, wsID, got.WorkspaceID)
	require.NoError(t, mock.ExpectationsWereMet())

	// The query is asserted for what it must NOT contain as well: a read path
	// that selected encrypted_value would still satisfy every assertion above,
	// because domain.Secret has nowhere to put it — the column would simply be
	// fetched and dropped, and the leak would be one struct field away.
	_ = gotQuery
	assert.NotContains(t, secretMaskedColumns, "encrypted_value")
}

// A row belonging to another tenant must give the same answer as an id that
// names nothing at all — otherwise the endpoint distinguishes "exists but not
// yours" from "does not exist", which is how a secret inventory gets probed
// one uuid at a time.
func TestSecretRepo_GetByID_ForeignRowIsNotFound(t *testing.T) {
	repo, mock := newSecretRepoMock(t)
	id, wsID := uuid.New(), uuid.New()

	mock.ExpectQuery(`SELECT .* FROM secret_refs WHERE id = \$1 AND workspace_id = \$2`).
		WithArgs(id, wsID).
		WillReturnRows(sqlmock.NewRows(secretMaskedRowCols)) // no rows

	_, err := repo.GetByID(context.Background(), wsID, id)
	require.Error(t, err)
	var apiErr *apierror.Error
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, 404, apiErr.StatusCode())
}

// --- AssertScopeRefInWorkspace (S3): the check the FKs cannot do ---

func TestSecretRepo_AssertScopeRefInWorkspace_NoRefsSkipsTheQuery(t *testing.T) {
	repo, mock := newSecretRepoMock(t)
	// No ExpectQuery at all: a workspace-scoped secret names no project or
	// agent, so there is nothing to contain and no round trip to pay for.
	require.NoError(t, repo.AssertScopeRefInWorkspace(context.Background(), uuid.New(), nil, nil))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSecretRepo_AssertScopeRefInWorkspace_AcceptsOwnProject(t *testing.T) {
	repo, mock := newSecretRepoMock(t)
	wsID, projID := uuid.New(), uuid.New()

	mock.ExpectQuery(`SELECT`).
		WithArgs(wsID, projID, nil).
		WillReturnRows(sqlmock.NewRows([]string{"project_ok", "agent_ok"}).AddRow(true, true))

	require.NoError(t, repo.AssertScopeRefInWorkspace(context.Background(), wsID, &projID, nil))
	require.NoError(t, mock.ExpectationsWereMet())
}

// The case the table's own constraints accept: a project id that is perfectly
// valid, just not this tenant's. Without this check the row lands, and the
// only symptom is a secret that silently never materializes.
func TestSecretRepo_AssertScopeRefInWorkspace_RefusesForeignProject(t *testing.T) {
	repo, mock := newSecretRepoMock(t)
	wsID, projID := uuid.New(), uuid.New()

	mock.ExpectQuery(`SELECT`).
		WithArgs(wsID, projID, nil).
		WillReturnRows(sqlmock.NewRows([]string{"project_ok", "agent_ok"}).AddRow(false, true))

	err := repo.AssertScopeRefInWorkspace(context.Background(), wsID, &projID, nil)
	require.Error(t, err)
	var apiErr *apierror.Error
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, 400, apiErr.StatusCode())
	// The error names the field but never the id it was given — "must name a
	// project in this workspace" is the same answer a nonexistent id gets, so
	// the two stay indistinguishable to a caller probing for one.
	require.Contains(t, apiErr.Validation, "project_id")
	assert.NotContains(t, apiErr.Validation, "agent_id", "a valid agent_id was reported as invalid")
	assert.NotContains(t, apiErr.Validation["project_id"], projID.String())
}

func TestSecretRepo_AssertScopeRefInWorkspace_RefusesForeignAgent(t *testing.T) {
	repo, mock := newSecretRepoMock(t)
	wsID, agentID := uuid.New(), uuid.New()

	mock.ExpectQuery(`SELECT`).
		WithArgs(wsID, nil, agentID).
		WillReturnRows(sqlmock.NewRows([]string{"project_ok", "agent_ok"}).AddRow(true, false))

	err := repo.AssertScopeRefInWorkspace(context.Background(), wsID, nil, &agentID)
	require.Error(t, err)
	var apiErr *apierror.Error
	require.True(t, errors.As(err, &apiErr))
	require.Contains(t, apiErr.Validation, "agent_id")
	assert.NotContains(t, apiErr.Validation, "project_id")
}
