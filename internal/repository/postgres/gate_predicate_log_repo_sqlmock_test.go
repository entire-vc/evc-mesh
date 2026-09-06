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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// This file exists because of a defect found in the immediately preceding card
// (#4545660b): the production UPDATE that persisted gate authorship had NO test pinning
// its column list, so deleting a column from it left the entire 25-package suite green.
// Every layer above the SQL runs against fakes and cannot see the statement at all.
//
// The same shape applies here, worse: gate_predicate_log is written from a best-effort
// path that deliberately swallows errors, so a column silently dropped from this INSERT
// produces no failure ANYWHERE at runtime — just a log table that quietly answers the
// card's two-week question wrong. So these tests assert the statement itself.

func captureLogRepoSQL(t *testing.T) (*GatePredicateLogRepo, sqlmock.Sqlmock, *string) {
	t.Helper()
	var captured string
	rawDB, mock, err := sqlmock.New(
		sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(_, actualSQL string) error {
			captured = actualSQL
			return nil
		})),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewGatePredicateLogRepo(sqlx.NewDb(rawDB, "postgres")), mock, &captured
}

func sampleLogEntry() *domain.GatePredicateLogEntry {
	return &domain.GatePredicateLogEntry{
		ID:                 uuid.New(),
		TaskID:             uuid.New(),
		ActorID:            uuid.New(),
		ActorType:          domain.ActorTypeAgent,
		Outcome:            domain.GatePredicateRefusedSelfServe,
		CredentialExists:   true,
		Reversible:         true,
		BlockedByOtherTask: false,
		CustomerVisibleNow: false,
		CredentialReason:   "token in keys.env",
		ReversibleReason:   "migration is additive",
		BlockedReason:      "no other card",
		CustomerReason:     "gateway inactive",
		Source:             domain.ArmHumanGateSourceAPI,
		CreatedAt:          time.Now(),
	}
}

// loggedColumns are every column the INSERT must name. Listed explicitly so that adding
// a field to the entry without persisting it fails here rather than silently producing a
// column of NULLs nobody notices until the ratio is read weeks later.
var loggedColumns = []string{
	"id", "task_id", "actor_id", "actor_type", "outcome",
	"credential_exists", "reversible", "blocked_by_other_task", "customer_visible_now",
	"credential_reason", "reversible_reason", "blocked_reason", "customer_reason",
	"source", "created_at",
}

func TestGatePredicateLogRepo_RecordNamesEveryColumn(t *testing.T) {
	repo, mock, captured := captureLogRepoSQL(t)
	e := sampleLogEntry()

	// Arg order asserted too: an INSERT can name every column and still bind them in the
	// wrong order, which no column-name check would catch — and here the mistake would
	// be invisible, since the booleans and the free-text reasons are type-compatible
	// among themselves.
	mock.ExpectExec("").WithArgs(
		e.ID, e.TaskID, e.ActorID, string(e.ActorType), string(e.Outcome),
		e.CredentialExists, e.Reversible, e.BlockedByOtherTask, e.CustomerVisibleNow,
		e.CredentialReason, e.ReversibleReason, e.BlockedReason, e.CustomerReason,
		string(e.Source), e.CreatedAt,
	).WillReturnResult(sqlmock.NewResult(1, 1))

	require.NoError(t, repo.Record(context.Background(), e))
	require.NoError(t, mock.ExpectationsWereMet())

	for _, col := range loggedColumns {
		assert.Regexp(t, regexp.MustCompile(`\b`+regexp.QuoteMeta(col)+`\b`), *captured,
			"the INSERT must persist %s — a column missing here answers the card's "+
				"refusals-vs-arms question wrong, silently", col)
	}
}

// TestGatePredicateLogRepo_RecordSurfacesErrors: the SERVICE deliberately swallows a
// logging failure so that a broken recorder never becomes a failure to enforce. That
// makes it all the more important the repo itself does not also swallow — otherwise
// nothing anywhere can tell a quiet table from a broken one.
func TestGatePredicateLogRepo_RecordSurfacesErrors(t *testing.T) {
	repo, mock, _ := captureLogRepoSQL(t)

	mock.ExpectExec("").WillReturnError(errors.New("relation \"gate_predicate_log\" does not exist"))
	err := repo.Record(context.Background(), sampleLogEntry())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "gate_predicate_log")
}

// TestGatePredicateLogRepo_CountByOutcomeIsGrouped pins the query the card's acceptance
// actually runs. It must group by outcome and filter on time — a count that returns only
// refusals has no denominator, and "how often the guard fired" reads identically whether
// the guard is preventing everything or nothing.
func TestGatePredicateLogRepo_CountByOutcomeIsGrouped(t *testing.T) {
	repo, mock, captured := captureLogRepoSQL(t)
	since := time.Now().Add(-14 * 24 * time.Hour)

	mock.ExpectQuery("").WithArgs(since).WillReturnRows(
		sqlmock.NewRows([]string{"outcome", "count"}).
			AddRow(string(domain.GatePredicateAllowed), 7).
			AddRow(string(domain.GatePredicateRefusedSelfServe), 11).
			AddRow(string(domain.GatePredicateRefusedUseDependency), 3),
	)

	got, err := repo.CountByOutcome(context.Background(), since)
	require.NoError(t, err)

	assert.Equal(t, 7, got[domain.GatePredicateAllowed])
	assert.Equal(t, 11, got[domain.GatePredicateRefusedSelfServe])
	assert.Equal(t, 3, got[domain.GatePredicateRefusedUseDependency])

	assert.Regexp(t, `(?i)group\s+by\s+outcome`, *captured)
	assert.Regexp(t, `(?i)created_at\s*>=`, *captured,
		"must be time-bounded, or the 'last two weeks' answer silently becomes all-time")
}
