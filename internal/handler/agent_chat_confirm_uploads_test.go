package handler

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/storage"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// statStore answers StatObject per key; S3 is the true external boundary here.
type statStore struct {
	sizes map[string]int64
	err   error
}

func (s *statStore) PresignPutURL(context.Context, string, time.Duration, http.Header) (string, error) {
	return "", nil
}

func (s *statStore) GetPresignedURL(context.Context, string, time.Duration, string, string) (string, error) {
	return "", nil
}

func (s *statStore) StatObject(_ context.Context, key string) (int64, error) {
	if s.err != nil {
		return 0, s.err
	}
	if size, ok := s.sizes[key]; ok {
		return size, nil
	}
	return 0, storage.ErrNotFound
}

var pendingCols = []string{"id", "workspace_id", "thread_id", "message_id", "s3_key", "file_name", "mime", "size", "width", "height", "uploaded_at"}

func confirmFixture(t *testing.T, store ChatAttachmentStore) (*AgentChatHandler, sqlmock.Sqlmock, *chatScope, uuid.UUID) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	h := (&AgentChatHandler{}).WithAttachments(postgres.NewChatAttachmentRepo(sqlx.NewDb(db, "postgres")), store)
	return h, mock, &chatScope{gate: &postgres.ChatGate{WorkspaceID: uuid.New()}}, uuid.New()
}

func pendingRow(id uuid.UUID, key string, size int64) *sqlmock.Rows {
	return sqlmock.NewRows(pendingCols).AddRow(id, uuid.New(), uuid.New(), nil, key, "a.png", "image/png", size, nil, nil, nil)
}

func TestConfirmUploads_StampsOnlyMatchingObjects(t *testing.T) {
	ok, wrong, missing := uuid.New(), uuid.New(), uuid.New()
	store := &statStore{sizes: map[string]int64{"k-ok": 10, "k-wrong": 1}}
	h, mock, s, thread := confirmFixture(t, store)

	rows := pendingRow(ok, "k-ok", 10)
	rows.AddRow(wrong, uuid.New(), thread, nil, "k-wrong", "b.png", "image/png", int64(10), nil, nil, nil)
	rows.AddRow(missing, uuid.New(), thread, nil, "k-missing", "c.png", "image/png", int64(10), nil, nil, nil)
	mock.ExpectQuery("FROM ext_agent_chat_attachments").WillReturnRows(rows)
	// Red control: only the matching object is stamped; an unexpected exec fails the run.
	mock.ExpectExec("UPDATE ext_agent_chat_attachments SET uploaded_at").WithArgs(ok).WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, h.confirmUploads(context.Background(), s, thread, []uuid.UUID{ok, wrong, missing}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestConfirmUploads_NoopWithoutIDsOrStore(t *testing.T) {
	h, mock, s, thread := confirmFixture(t, &statStore{})
	require.NoError(t, h.confirmUploads(context.Background(), s, thread, nil))
	require.NoError(t, (&AgentChatHandler{}).confirmUploads(context.Background(), s, thread, []uuid.UUID{uuid.New()}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestConfirmUploads_Failures(t *testing.T) {
	id := uuid.New()
	t.Run("lookup error", func(t *testing.T) {
		h, mock, s, thread := confirmFixture(t, &statStore{})
		mock.ExpectQuery("FROM ext_agent_chat_attachments").WillReturnError(errors.New("db down"))
		require.Error(t, h.confirmUploads(context.Background(), s, thread, []uuid.UUID{id}))
	})
	t.Run("storage unavailable is 503", func(t *testing.T) {
		h, mock, s, thread := confirmFixture(t, &statStore{err: errors.New("s3 down")})
		mock.ExpectQuery("FROM ext_agent_chat_attachments").WillReturnRows(pendingRow(id, "k", 10))
		err := h.confirmUploads(context.Background(), s, thread, []uuid.UUID{id})
		var ae *apierror.Error
		require.ErrorAs(t, err, &ae)
		require.Equal(t, http.StatusServiceUnavailable, ae.Code)
	})
	t.Run("stamp error", func(t *testing.T) {
		h, mock, s, thread := confirmFixture(t, &statStore{sizes: map[string]int64{"k": 10}})
		mock.ExpectQuery("FROM ext_agent_chat_attachments").WillReturnRows(pendingRow(id, "k", 10))
		mock.ExpectExec("UPDATE ext_agent_chat_attachments SET uploaded_at").WillReturnError(errors.New("db down"))
		require.Error(t, h.confirmUploads(context.Background(), s, thread, []uuid.UUID{id}))
	})
}
