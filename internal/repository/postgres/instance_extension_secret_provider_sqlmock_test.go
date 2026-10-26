package postgres

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions/secretstore"
)

const secretProviderQuery = "SELECT COALESCE(settings->>$2, '') FROM instance_extension_settings WHERE extension_id = $1"

type recordingKeep struct{ err error }

func (k recordingKeep) PutValue(_ context.Context, ref secretstore.Ref, _ string) (string, error) {
	if k.err != nil {
		return "", k.err
	}
	return "id-" + ref.ExtensionID + "-" + ref.Field, nil
}
func (k recordingKeep) GetValue(context.Context, string) (string, error) { return "", k.err }

type blobArg struct{ into *[]byte }

func (b blobArg) Match(v driver.Value) bool {
	raw, ok := v.([]byte)
	if ok {
		*b.into = raw
	}
	return ok
}

// installSelectedStore wires the process store the way cmd/api does: the
// Router's Selected reads the keep instance setting through the repo.
func installSelectedStore(t *testing.T, repo *InstanceExtensionSettingRepo, keep secretstore.KeepClient) {
	t.Helper()
	r := &secretstore.Router{Builtin: secretstore.Builtin{}}
	if keep != nil {
		r.Keep = secretstore.Keep{Client: keep}
	}
	r.Selected = (&secretstore.ProviderSetting{Read: repo.SecretProvider}).Selected
	prev := secretstore.SetDefault(r)
	t.Cleanup(func() { secretstore.SetDefault(prev) })
}

func newInstanceSettingMock(t *testing.T) (*InstanceExtensionSettingRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewInstanceExtensionSettingRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

func upsertGitlabToken(t *testing.T, repo *InstanceExtensionSettingRepo, mock sqlmock.Sqlmock, provider string) (stored string, err error) {
	t.Helper()
	mock.ExpectQuery(regexp.QuoteMeta(secretProviderQuery)).
		WithArgs(secretstore.KeepExtensionID, secretstore.SettingSecretProvider).
		WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(provider))
	var blob []byte
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO instance_extension_settings")).
		WithArgs("gitlab", blobArg{&blob}, true, string(domain.InstanceSettingSourceAdmin), nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	err = repo.Upsert(context.Background(), &domain.InstanceExtensionSetting{
		ExtensionID: "gitlab", Settings: json.RawMessage(`{"token":"plain-tok"}`), Enabled: true,
		Source: domain.InstanceSettingSourceAdmin,
	}, []string{"token"})
	if err != nil {
		return "", err
	}
	var m map[string]string
	require.NoError(t, json.Unmarshal(blob, &m))
	return m["token"], nil
}

func TestInstanceSettings_SecretProviderKeepWritesKeepRef(t *testing.T) {
	setEncryptionKey(t, 21)
	repo, mock := newInstanceSettingMock(t)
	installSelectedStore(t, repo, recordingKeep{})

	stored, err := upsertGitlabToken(t, repo, mock, secretstore.ProviderKeep)
	require.NoError(t, err)
	require.Equal(t, "keep:id-gitlab-token", stored, "secret_provider=keep writes the x-secret as keep:<id>")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInstanceSettings_SecretProviderDefaultStaysBuiltin(t *testing.T) {
	setEncryptionKey(t, 22)
	for _, provider := range []string{"", secretstore.ProviderBuiltin} {
		repo, mock := newInstanceSettingMock(t)
		installSelectedStore(t, repo, recordingKeep{})

		stored, err := upsertGitlabToken(t, repo, mock, provider)
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(stored, "enc:v1:"), "provider %q: got %q", provider, stored)
		plain, reason := secretstore.Open(context.Background(), stored)
		require.Empty(t, reason)
		require.Equal(t, "plain-tok", plain)
		require.NoError(t, mock.ExpectationsWereMet())
	}
}

// Keep selected and refusing: the save fails and no row is written, so the
// setting is never stored with an empty value.
func TestInstanceSettings_SecretProviderKeepDownWritesNothing(t *testing.T) {
	setEncryptionKey(t, 23)
	for name, keep := range map[string]secretstore.KeepClient{
		"keep refuses":        recordingKeep{err: errors.New("connection refused")},
		"keep not configured": nil,
	} {
		repo, mock := newInstanceSettingMock(t)
		installSelectedStore(t, repo, keep)
		mock.ExpectQuery(regexp.QuoteMeta(secretProviderQuery)).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(secretstore.ProviderKeep))

		err := repo.Upsert(context.Background(), &domain.InstanceExtensionSetting{
			ExtensionID: "gitlab", Settings: json.RawMessage(`{"token":"plain-tok"}`), Enabled: true,
		}, []string{"token"})
		require.Error(t, err, name)
		if keep == nil {
			require.ErrorIs(t, err, secretstore.ErrKeepNotConfigured, name)
		}
		require.NoError(t, mock.ExpectationsWereMet(), "%s: no INSERT may run", name)
	}
}

func TestInstanceSettings_SecretProviderUnsetRowIsBuiltin(t *testing.T) {
	repo, mock := newInstanceSettingMock(t)
	mock.ExpectQuery(regexp.QuoteMeta(secretProviderQuery)).WillReturnRows(sqlmock.NewRows([]string{"v"}))
	v, err := repo.SecretProvider(context.Background())
	require.NoError(t, err)
	require.Empty(t, v, "no keep row: the default applies")
	require.NoError(t, mock.ExpectationsWereMet())
}
