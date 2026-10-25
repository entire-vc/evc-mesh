package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// The DB-backed flow lives in internal/handler/user_runtime_handler_db_test.go and
// needs Postgres; these tests pin the same behaviour without one, so CI (no DB in
// the unit job) still exercises the authorization order, the revision check and
// what each path writes.

func newUserRuntimeMock(t *testing.T) (*UserRuntimeRepo, sqlmock.Sqlmock) {
	t.Helper()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	return NewUserRuntimeRepo(sqlx.NewDb(raw, "postgres")), mock
}

func statusOf(t *testing.T, err error) int {
	t.Helper()
	var api *apierror.Error
	require.True(t, errors.As(err, &api), "want an apierror, got %v", err)
	return api.StatusCode()
}

var cfgCols = []string{"user_id", "revision", "config", "updated_at"}

func TestNormalizeConfig(t *testing.T) {
	ok := `{"accounts":{"a":{"credential":"cred:x"}},"pools":{"p":{"accounts":["a"]}},"profiles":{"f":{"model":"m"}},"agents":{}}`
	out, err := normalizeConfig(json.RawMessage(ok))
	require.NoError(t, err)
	assert.JSONEq(t, ok, string(out))

	for name, raw := range map[string]string{
		"empty":             ``,
		"not an object":     `[1]`,
		"null":              `null`,
		"unknown section":   `{"extra":{}}`,
		"section not map":   `{"accounts":[]}`,
		"secret key":        `{"accounts":{"a":{"api_key":"sk"}}}`,
		"nested secret":     `{"profiles":{"f":{"x":[{"password":"p"}]}}}`,
		"plain credential":  `{"accounts":{"a":{"credential":"plain"}}}`,
		"non-string cred":   `{"accounts":{"a":{"credential":5}}}`,
		"oversized":         `{"accounts":{"a":"` + strings.Repeat("x", maxUserRuntimeConfigBytes) + `"}}`,
		"generic value":     `{"accounts":{"a":{"value":"plaintext-secret"}}}`,
		"nested account":    `{"accounts":{"a":{"provider":{"v":"x"}}}}`,
		"account not obj":   `{"accounts":{"a":"x"}}`,
		"profiles not map":  `{"profiles":"x"}`,
		"agents not object": `{"agents":1}`,
	} {
		_, err := normalizeConfig(json.RawMessage(raw))
		require.Error(t, err, name)
		assert.Equal(t, 400, statusOf(t, err), name)
	}
}

func TestReferencedResources_OnlyWhatTheProfileReaches(t *testing.T) {
	doc := map[string]json.RawMessage{
		"accounts": json.RawMessage(`{"a1":{},"a2":{},"a3":{}}`),
		"pools":    json.RawMessage(`{"p1":{"accounts":["a1"]},"p2":{"accounts":["a2"]}}`),
		"profiles": json.RawMessage(`{"fast":{"pool":"p1"},"slow":{"pool":"p2"},"edge":{"account":"a3"},"unused":{}}`),
	}
	profile := json.RawMessage(`{"profile":"fast","primary_profiles":["fast"],"quota_edges":[{"to":"edge"}]}`)
	var got struct {
		Accounts, Pools, Profiles map[string]json.RawMessage
	}
	out := referencedResources(doc, profile)
	require.NoError(t, json.Unmarshal([]byte(strings.NewReplacer("accounts", "Accounts", "pools", "Pools", "profiles", "Profiles").Replace(string(out))), &got))
	assert.Len(t, got.Profiles, 2)
	assert.Contains(t, got.Profiles, "fast")
	assert.Contains(t, got.Profiles, "edge")
	assert.Equal(t, []string{"p1"}, keys(got.Pools))
	assert.ElementsMatch(t, []string{"a1", "a3"}, keys(got.Accounts))

	// no profile at all: nothing leaks
	empty := referencedResources(doc, json.RawMessage(`null`))
	assert.JSONEq(t, `{"accounts":{},"pools":{},"profiles":{}}`, string(empty))

	// arbitrary strings that merely equal a resource name do not select it
	doc["profiles"] = json.RawMessage(`{"fast":{"pool":"p1","description":"p2 a2 a3","model":"p2"}}`)
	leak := referencedResources(doc, json.RawMessage(`{"profile":"fast"}`))
	var lg struct {
		Accounts, Pools map[string]json.RawMessage
	}
	require.NoError(t, json.Unmarshal([]byte(strings.NewReplacer("accounts", "Accounts", "pools", "Pools").Replace(string(leak))), &lg))
	assert.Equal(t, []string{"p1"}, keys(lg.Pools))
	assert.Equal(t, []string{"a1"}, keys(lg.Accounts))
}

func keys(m map[string]json.RawMessage) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestUserRuntime_ActorGuards(t *testing.T) {
	repo, _ := newUserRuntimeMock(t)
	ctx := context.Background()
	user, agent := uuid.New(), uuid.New()
	for name, actor := range map[string]UserRuntimeActor{
		"agent on operator route":     {UserID: user, AgentID: agent},
		"connector":                   {UserID: user, Connector: true},
		"nobody":                      {},
		"agent claiming a user as ok": {AgentID: agent, UserID: user},
	} {
		_, err := repo.Get(ctx, actor)
		assert.Equal(t, 403, statusOf(t, err), name)
		_, err = repo.Save(ctx, actor, UserRuntimeSave{Config: json.RawMessage(`{}`)})
		assert.Equal(t, 403, statusOf(t, err), name)
		_, err = repo.History(ctx, actor, 10)
		assert.Equal(t, 403, statusOf(t, err), name)
		_, err = repo.Managers(ctx, actor)
		assert.Equal(t, 403, statusOf(t, err), name)
		assert.Equal(t, 403, statusOf(t, repo.SetLimits(ctx, actor, agent, 1)), name)
		assert.Equal(t, 403, statusOf(t, repo.GrantManager(ctx, actor, agent)), name)
		assert.Equal(t, 403, statusOf(t, repo.RevokeManager(ctx, actor, agent)), name)
	}
	for name, actor := range map[string]UserRuntimeActor{
		"user on agent route": {UserID: user},
		"connector agent":     {AgentID: agent, Connector: true},
	} {
		_, err := repo.SaveAsManager(ctx, actor, UserRuntimeSave{Config: json.RawMessage(`{}`)})
		assert.Equal(t, 403, statusOf(t, err), name)
		assert.Equal(t, 403, statusOf(t, repo.SetLimitsAsManager(ctx, actor, agent, 1)), name)
		_, err = repo.AgentRuntime(ctx, actor)
		assert.Equal(t, 403, statusOf(t, err), name)
	}
}

func TestUserRuntime_GetDefaultsToEmptyRevisionZero(t *testing.T) {
	repo, mock := newUserRuntimeMock(t)
	user := uuid.New()
	mock.ExpectQuery("FROM user_runtime_configs").WithArgs(user).WillReturnError(sql.ErrNoRows)
	got, err := repo.Get(context.Background(), UserRuntimeActor{UserID: user})
	require.NoError(t, err)
	assert.EqualValues(t, 0, got.Revision)
	assert.JSONEq(t, `{}`, string(got.Config))
	require.NoError(t, mock.ExpectationsWereMet())
}

func expectSave(mock sqlmock.Sqlmock, operator uuid.UUID, currentRev, newRev int64, author uuid.UUID, authorType string) {
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO user_runtime_configs").WithArgs(operator).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("FROM user_runtime_configs WHERE user_id=\\$1 FOR UPDATE").WithArgs(operator).
		WillReturnRows(sqlmock.NewRows(cfgCols).AddRow(operator, currentRev, []byte(`{}`), time.Now()))
	if newRev > 0 {
		mock.ExpectQuery("UPDATE user_runtime_configs").WithArgs(operator, sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows(cfgCols).AddRow(operator, newRev, []byte(`{}`), time.Now()))
		mock.ExpectExec("INSERT INTO user_runtime_history").WithArgs(operator, newRev, sqlmock.AnyArg(), authorType, author).
			WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit()
	} else {
		mock.ExpectRollback()
	}
}

func TestUserRuntime_SaveWritesRevisionAndHistory(t *testing.T) {
	repo, mock := newUserRuntimeMock(t)
	user := uuid.New()
	expectSave(mock, user, 0, 1, user, "user")
	got, err := repo.Save(context.Background(), UserRuntimeActor{UserID: user}, UserRuntimeSave{IfRevision: 0, Config: json.RawMessage(`{"accounts":{}}`)})
	require.NoError(t, err)
	assert.EqualValues(t, 1, got.Revision)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserRuntime_SaveRejectsStaleRevisionAndBadConfig(t *testing.T) {
	repo, mock := newUserRuntimeMock(t)
	user := uuid.New()
	expectSave(mock, user, 3, 0, user, "user")
	_, err := repo.Save(context.Background(), UserRuntimeActor{UserID: user}, UserRuntimeSave{IfRevision: 1, Config: json.RawMessage(`{}`)})
	assert.Equal(t, 409, statusOf(t, err))
	require.NoError(t, mock.ExpectationsWereMet())

	// a bad config is refused before any transaction starts
	_, err = repo.Save(context.Background(), UserRuntimeActor{UserID: user}, UserRuntimeSave{Config: json.RawMessage(`{"accounts":{"a":{"token":"t"}}}`)})
	assert.Equal(t, 400, statusOf(t, err))
}

func expectManagerLookup(mock sqlmock.Sqlmock, agent, operator uuid.UUID, isManager bool) {
	q := mock.ExpectQuery("FROM user_runtime_managers m").WithArgs(agent)
	if isManager {
		q.WillReturnRows(sqlmock.NewRows([]string{"operator_user_id"}).AddRow(operator))
	} else {
		q.WillReturnError(sql.ErrNoRows)
	}
}

func TestUserRuntime_ManagerSaveNeedsLiveAssignment(t *testing.T) {
	repo, mock := newUserRuntimeMock(t)
	agent, operator := uuid.New(), uuid.New()
	in := UserRuntimeSave{Config: json.RawMessage(`{}`)}

	expectManagerLookup(mock, agent, operator, false)
	_, err := repo.SaveAsManager(context.Background(), UserRuntimeActor{AgentID: agent}, in)
	assert.Equal(t, 403, statusOf(t, err), "no assignment: forbidden")

	expectManagerLookup(mock, agent, operator, true)
	expectSave(mock, operator, 0, 1, agent, "agent")
	got, err := repo.SaveAsManager(context.Background(), UserRuntimeActor{AgentID: agent}, in)
	require.NoError(t, err)
	assert.EqualValues(t, 1, got.Revision)

	mock.ExpectQuery("FROM user_runtime_managers m").WithArgs(agent).WillReturnError(errors.New("db down"))
	_, err = repo.SaveAsManager(context.Background(), UserRuntimeActor{AgentID: agent}, in)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserRuntime_HistoryAndManagersLists(t *testing.T) {
	repo, mock := newUserRuntimeMock(t)
	user, agent := uuid.New(), uuid.New()
	mock.ExpectQuery("FROM user_runtime_history").WithArgs(user, 50).WillReturnRows(
		sqlmock.NewRows([]string{"id", "revision", "kind", "detail", "author_type", "author_id", "created_at"}).
			AddRow(2, 2, "config", []byte(`{}`), "agent", agent, time.Now()))
	hist, err := repo.History(context.Background(), UserRuntimeActor{UserID: user}, 0)
	require.NoError(t, err)
	require.Len(t, hist, 1)
	assert.Equal(t, "agent", hist[0].AuthorType)

	mock.ExpectQuery("FROM user_runtime_managers m JOIN agents").WithArgs(user).WillReturnRows(
		sqlmock.NewRows([]string{"agent_id", "agent_name", "granted_at"}).AddRow(agent, "robin", time.Now()))
	mgrs, err := repo.Managers(context.Background(), UserRuntimeActor{UserID: user})
	require.NoError(t, err)
	require.Len(t, mgrs, 1)
	assert.Equal(t, "robin", mgrs[0].AgentName)
	require.NoError(t, mock.ExpectationsWereMet())
}

func expectLimits(mock sqlmock.Sqlmock, agent, operator uuid.UUID, limit int, affected int64, author uuid.UUID, authorType string) {
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE agents SET max_concurrent_tasks").WithArgs(agent, operator, limit).WillReturnResult(sqlmock.NewResult(0, affected))
	if affected == 0 {
		mock.ExpectRollback()
		return
	}
	mock.ExpectQuery("FROM user_runtime_configs").WithArgs(operator).WillReturnRows(sqlmock.NewRows(cfgCols).AddRow(operator, 4, []byte(`{}`), time.Now()))
	mock.ExpectExec("INSERT INTO user_runtime_history").WithArgs(operator, 4, sqlmock.AnyArg(), authorType, author).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
}

func TestUserRuntime_Limits(t *testing.T) {
	repo, mock := newUserRuntimeMock(t)
	ctx := context.Background()
	agent, operator, manager := uuid.New(), uuid.New(), uuid.New()

	expectLimits(mock, agent, operator, 3, 1, operator, "user")
	require.NoError(t, repo.SetLimits(ctx, UserRuntimeActor{UserID: operator}, agent, 3))

	expectLimits(mock, agent, operator, 3, 0, operator, "user")
	assert.Equal(t, 404, statusOf(t, repo.SetLimits(ctx, UserRuntimeActor{UserID: operator}, agent, 3)), "not the operator's agent")

	assert.Equal(t, 400, statusOf(t, repo.SetLimits(ctx, UserRuntimeActor{UserID: operator}, agent, -1)))
	assert.Equal(t, 400, statusOf(t, repo.SetLimits(ctx, UserRuntimeActor{UserID: operator}, agent, maxConcurrentTasksCap+1)))

	expectManagerLookup(mock, manager, operator, true)
	expectLimits(mock, agent, operator, 4, 1, manager, "agent")
	require.NoError(t, repo.SetLimitsAsManager(ctx, UserRuntimeActor{AgentID: manager}, agent, 4))

	expectManagerLookup(mock, manager, operator, false)
	assert.Equal(t, 403, statusOf(t, repo.SetLimitsAsManager(ctx, UserRuntimeActor{AgentID: manager}, agent, 4)))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserRuntime_GrantAndRevokeManager(t *testing.T) {
	repo, mock := newUserRuntimeMock(t)
	ctx := context.Background()
	agent, operator := uuid.New(), uuid.New()
	actor := UserRuntimeActor{UserID: operator}

	for _, tc := range []struct {
		grant bool
		sql   string
		kind  string
	}{
		{true, "INSERT INTO user_runtime_managers", "manager_granted"},
		{false, "UPDATE user_runtime_managers SET revoked_at", "manager_revoked"},
	} {
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT EXISTS \\(SELECT 1 FROM agents").WithArgs(agent, operator).WillReturnRows(sqlmock.NewRows([]string{"e"}).AddRow(true))
		mock.ExpectExec(tc.sql).WithArgs(operator, agent).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery("FROM user_runtime_configs").WithArgs(operator).WillReturnError(sql.ErrNoRows)
		mock.ExpectExec("INSERT INTO user_runtime_history").WithArgs(operator, 0, tc.kind, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit()
		if tc.grant {
			require.NoError(t, repo.GrantManager(ctx, actor, agent))
		} else {
			require.NoError(t, repo.RevokeManager(ctx, actor, agent))
		}
	}

	// an agent of someone else is a plain 404
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT EXISTS \\(SELECT 1 FROM agents").WithArgs(agent, operator).WillReturnRows(sqlmock.NewRows([]string{"e"}).AddRow(false))
	mock.ExpectRollback()
	assert.Equal(t, 404, statusOf(t, repo.GrantManager(ctx, actor, agent)))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserRuntime_AgentRuntimeView(t *testing.T) {
	repo, mock := newUserRuntimeMock(t)
	ctx := context.Background()
	agent, operator := uuid.New(), uuid.New()
	actor := UserRuntimeActor{AgentID: agent}
	agentCols := []string{"operator_user_id", "max_concurrent_tasks"}

	// no operator: valid, empty answer
	mock.ExpectQuery("FROM agents WHERE id=\\$1").WithArgs(agent).WillReturnRows(sqlmock.NewRows(agentCols).AddRow(nil, 5))
	view, err := repo.AgentRuntime(ctx, actor)
	require.NoError(t, err)
	assert.False(t, view.OperatorResolved)
	assert.Equal(t, 5, view.Limits.MaxConcurrentTasks)

	// with operator, assignment and a document
	doc := `{"accounts":{"a1":{},"a2":{}},"pools":{"p1":{"accounts":["a1"]},"p2":{}},"profiles":{"fast":{"pool":"p1"},"other":{"pool":"p2"}},"agents":{"` + agent.String() + `":{"profile":"fast"}}}`
	mock.ExpectQuery("FROM agents WHERE id=\\$1").WithArgs(agent).WillReturnRows(sqlmock.NewRows(agentCols).AddRow(operator, 4))
	expectManagerLookup(mock, agent, operator, true)
	mock.ExpectQuery("FROM user_runtime_configs").WithArgs(operator).WillReturnRows(sqlmock.NewRows(cfgCols).AddRow(operator, 7, []byte(doc), time.Now()))
	view, err = repo.AgentRuntime(ctx, actor)
	require.NoError(t, err)
	assert.True(t, view.OperatorResolved)
	assert.True(t, view.RuntimeManager)
	assert.EqualValues(t, 7, view.Revision)
	assert.JSONEq(t, `{"profile":"fast"}`, string(view.Profile))
	res := string(view.Resources)
	assert.Contains(t, res, `"p1"`)
	assert.NotContains(t, res, `"p2"`)
	assert.Contains(t, res, `"a1"`)
	assert.NotContains(t, res, `"a2"`)

	// deleted / unknown agent
	mock.ExpectQuery("FROM agents WHERE id=\\$1").WithArgs(agent).WillReturnError(sql.ErrNoRows)
	_, err = repo.AgentRuntime(ctx, actor)
	assert.Equal(t, 404, statusOf(t, err))
	require.NoError(t, mock.ExpectationsWereMet())
}
