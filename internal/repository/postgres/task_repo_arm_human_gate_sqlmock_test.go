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

// TaskRepo.ArmHumanGate is the ONLY statement that persists gate authorship to Postgres
// (task #4545660b). Everything above it — the service, the handler, the marker path — is
// covered against fake repositories, which means every one of those tests would stay
// green if this UPDATE silently stopped writing a column.
//
// That is not hypothetical. Independent verification of this very MR deleted
// `gate_author = $3` from the production statement and the ENTIRE suite (25 packages)
// stayed green: the sibling SetHumanGate sqlmock tests match on `regexp.QuoteMeta(
// "UPDATE tasks SET")`, which pins that a statement was sent, never WHICH columns it
// sets. This repo has already paid for that exact blind spot once — a field present in an
// UPDATE but missing from the matching INSERT, incident #564, named in .gitlab-ci.yml's
// own comments.
//
// So these tests assert the SET clause's column list itself, not merely that some UPDATE
// went out. A column dropped from the statement fails here and nowhere else.

// captureTaskRepoSQL returns a TaskRepo whose driver accepts any statement and records
// it, so the test can assert on the SQL text with a real message instead of sqlmock's
// opaque "query does not match" — the difference between a test that says which column
// went missing and one that says a regexp didn't match.
func captureTaskRepoSQL(t *testing.T) (*TaskRepo, sqlmock.Sqlmock, *string) {
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
	return NewTaskRepo(sqlx.NewDb(rawDB, "postgres")), mock, &captured
}

// armedColumns are the columns ArmHumanGate MUST write. Listed here rather than inlined
// so that adding a sixth gate field without covering it fails review loudly.
var armedColumns = []string{
	"human_gate",
	"human_gate_class",
	"human_gate_armed_at",
	"gate_author",
	"gate_author_type",
	"gate_reason",
	"recommended_default",
	"gate_deadline",
}

func TestTaskRepo_ArmHumanGate_SetsEveryGateColumn(t *testing.T) {
	repo, mock, captured := captureTaskRepoSQL(t)

	taskID, author := uuid.New(), uuid.New()
	deadline := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

	// Arg order is asserted too: a statement can name every column and still bind them
	// in the wrong order, which no column-name check would catch. The 8th arg is the
	// configured default-on-timeout window (task #060ccaae) — unused by this particular
	// call since an explicit Deadline is given, but still always bound.
	mock.ExpectExec("").
		WithArgs(taskID, domain.HumanGateClassSoft, author, string(domain.ActorTypeAgent),
			"why", "what I will do otherwise", deadline, DefaultGateTimeoutHours).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID:             taskID,
		Author:             author,
		AuthorType:         domain.ActorTypeAgent,
		Reason:             "why",
		RecommendedDefault: "what I will do otherwise",
		Deadline:           &deadline,
		Class:              domain.HumanGateClassSoft,
	}))
	require.NoError(t, mock.ExpectationsWereMet())

	for _, col := range armedColumns {
		assert.Regexp(t, regexp.MustCompile(`\b`+regexp.QuoteMeta(col)+`\s*=`), *captured,
			"ArmHumanGate must SET %s — a gate armed without it is exactly the authorless "+
				"state task #4545660b exists to eliminate", col)
	}
}

// TestTaskRepo_ArmHumanGate_DoesNotResetArmedAtOnReArm pins the CASE expression, not just
// the column's presence. Re-arming refreshes the ask (a later marker supersedes an
// earlier one) but must not restart the age the soft-timeout sweep measures — otherwise a
// driver's repeat ping keeps a soft gate alive forever, which is the latch shape of
// #84ab54fd. `human_gate_armed_at = NOW()` unconditionally would satisfy the
// column-presence check above while reintroducing exactly that bug.
func TestTaskRepo_ArmHumanGate_DoesNotResetArmedAtOnReArm(t *testing.T) {
	repo, mock, captured := captureTaskRepoSQL(t)

	mock.ExpectExec("").WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID: uuid.New(), Author: uuid.New(), AuthorType: domain.ActorTypeAgent,
		Class: domain.HumanGateClassHard,
	}))

	assert.Regexp(t,
		`human_gate_armed_at\s*=\s*CASE\s+WHEN\s+human_gate\s+THEN\s+human_gate_armed_at\s+ELSE\s+NOW\(\)\s+END`,
		*captured,
		"armed_at must be stamped only on the false->true transition, so a repeat ping "+
			"cannot keep a soft gate alive indefinitely")
}

// TestTaskRepo_SetHumanGate_ReleaseNullsTheAsk covers the other half of the same
// contract. The ask metadata describes a LIVE question; a recommended_default left on a
// released task is a default that something (task #060ccaae's timeout sweep) will
// eventually apply to a settled question.
func TestTaskRepo_SetHumanGate_ReleaseNullsTheAsk(t *testing.T) {
	repo, mock, captured := captureTaskRepoSQL(t)

	mock.ExpectExec("").WithArgs(sqlmock.AnyArg(), false).
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.SetHumanGate(context.Background(), uuid.New(), false))

	for _, col := range []string{"gate_author", "gate_author_type", "gate_reason",
		"recommended_default", "gate_deadline"} {
		assert.Regexp(t,
			regexp.MustCompile(`\b`+regexp.QuoteMeta(col)+`\s*=\s*CASE\s+WHEN\s+\$2\s+THEN\s+`+
				regexp.QuoteMeta(col)+`\s+ELSE\s+NULL\s+END`),
			*captured,
			"releasing must null %s — an ask that outlives its question is residue every "+
				"reader has to learn to ignore", col)
	}
}

// TestTaskRepo_ArmHumanGate_MissingTaskIsNotFound: RowsAffected 0 means the task does not
// exist (or is soft-deleted). Reporting success there would let a caller believe a gate
// is armed on a task that has none — a false negative on the fleet's freeze signal.
func TestTaskRepo_ArmHumanGate_MissingTaskIsNotFound(t *testing.T) {
	repo, mock, _ := captureTaskRepoSQL(t)

	mock.ExpectExec("").WillReturnResult(sqlmock.NewResult(0, 0))
	err := repo.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID: uuid.New(), Author: uuid.New(), AuthorType: domain.ActorTypeAgent,
		Class: domain.HumanGateClassHard,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Task")
}

// TestTaskRepo_ArmHumanGate_DeadlineCASE_ExplicitAlwaysWins pins the FIRST branch of the
// gate_deadline CASE (task #060ccaae): a caller-supplied deadline is used verbatim,
// regardless of class or recommended_default — the auto-default exists to fill in what
// the caller left unset, never to override what they actually said.
func TestTaskRepo_ArmHumanGate_DeadlineCASE_ExplicitAlwaysWins(t *testing.T) {
	repo, mock, captured := captureTaskRepoSQL(t)
	mock.ExpectExec("").WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID: uuid.New(), Author: uuid.New(), AuthorType: domain.ActorTypeAgent,
		Class: domain.HumanGateClassHard,
	}))
	assert.Regexp(t, `gate_deadline\s*=\s*CASE\s+WHEN\s+\$7::timestamptz\s+IS\s+NOT\s+NULL\s+THEN\s+\$7::timestamptz`,
		*captured, "an explicitly passed deadline must be the first, unconditional branch")
}

// TestTaskRepo_ArmHumanGate_DeadlineCASE_HardNeverAutoDefaults pins the second branch:
// a hard-classified gate gets NULL when no explicit deadline was given, never a computed
// one — HumanGateClassHard's whole contract is that no code path releases it on a clock,
// and an auto-computed deadline sitting unused on the row would be exactly the residue
// a future reader could mistake for a live one.
func TestTaskRepo_ArmHumanGate_DeadlineCASE_HardNeverAutoDefaults(t *testing.T) {
	repo, mock, captured := captureTaskRepoSQL(t)
	mock.ExpectExec("").WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID: uuid.New(), Author: uuid.New(), AuthorType: domain.ActorTypeAgent,
		RecommendedDefault: "merge as-is", Class: domain.HumanGateClassHard,
	}))
	assert.Regexp(t, `WHEN\s+\$2\s*=\s*'hard'\s+THEN\s+NULL`, *captured,
		"a hard gate must fall to NULL, not an auto-computed deadline, when none was given")
}

// TestTaskRepo_ArmHumanGate_DeadlineCASE_NoDefaultMeansNoDeadline pins the third branch:
// a gate with no stated recommended_default gets NULL too, on any class — nothing would
// exist for the timeout sweep to apply, so a computed deadline would be reachable dead
// data (see domain.HumanGateDefaultTimeoutCandidate's doc for why the sweep never needs
// to represent this case at all).
func TestTaskRepo_ArmHumanGate_DeadlineCASE_NoDefaultMeansNoDeadline(t *testing.T) {
	repo, mock, captured := captureTaskRepoSQL(t)
	mock.ExpectExec("").WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID: uuid.New(), Author: uuid.New(), AuthorType: domain.ActorTypeAgent,
		Class: domain.HumanGateClassSoft, // no RecommendedDefault
	}))
	assert.Regexp(t, `WHEN\s+NULLIF\(\$6,\s*''\)\s+IS\s+NULL\s+THEN\s+NULL`, *captured,
		"a soft gate with no stated default must still get no auto-deadline")
}

// TestTaskRepo_ArmHumanGate_DeadlineCASE_SoftWithDefaultComputesFromConfiguredHours pins
// the final branch AND that the interval is a BOUND parameter, not a literal: the one case
// that actually produces a computed deadline is non-hard class + a real
// recommended_default + no explicit deadline, and the window is
// HUMAN_GATE_DEFAULT_TIMEOUT_H (default 24h, task #060ccaae / Pavel decision 2026-09-06 —
// the original spec said 72h, changed before ship), never baked into the query text.
func TestTaskRepo_ArmHumanGate_DeadlineCASE_SoftWithDefaultComputesFromConfiguredHours(t *testing.T) {
	repo, mock, captured := captureTaskRepoSQL(t)
	mock.ExpectExec("").WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID: uuid.New(), Author: uuid.New(), AuthorType: domain.ActorTypeAgent,
		RecommendedDefault: "merge; gateway is inactive", Class: domain.HumanGateClassSoft,
	}))
	assert.Regexp(t,
		`ELSE\s+\(CASE\s+WHEN\s+human_gate\s+THEN\s+human_gate_armed_at\s+ELSE\s+NOW\(\)\s+END\)\s+\+\s+make_interval\(hours\s*=>\s*\$8\)`,
		*captured,
		"the computed default must be armed_at (old on re-arm, NOW() on first arm) + the "+
			"CONFIGURED window — the SAME CASE the armed_at column itself uses, so a re-arm "+
			"cannot silently push the deadline out from under an already-running clock; and "+
			"the window must be a bound parameter make_interval(hours => $8), not a literal "+
			"INTERVAL string — a literal would mean every config change is a code change")
	assert.NotRegexp(t, `INTERVAL\s+'\d+\s+hours?'`, *captured,
		"the interval must never be baked into the query text as a literal again")
}

// TestTaskRepo_ArmHumanGate_DefaultTimeoutHours_UsesBuiltinDefaultWhenUnset proves the
// zero-value TaskRepo (every one of NewTaskRepo's 60+ pre-existing call sites, and every
// other test in this file) computes the deadline using DefaultGateTimeoutHours (24), not
// zero hours — a misconfiguration or an un-migrated call site must not turn every soft
// gate's deadline into "right now".
func TestTaskRepo_ArmHumanGate_DefaultTimeoutHours_UsesBuiltinDefaultWhenUnset(t *testing.T) {
	repo, mock, _ := captureTaskRepoSQL(t)
	mock.ExpectExec("").WithArgs(
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), DefaultGateTimeoutHours,
	).WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID: uuid.New(), Author: uuid.New(), AuthorType: domain.ActorTypeAgent,
		RecommendedDefault: "merge; gateway is inactive", Class: domain.HumanGateClassSoft,
	}))
}

// TestTaskRepo_ArmHumanGate_DefaultTimeoutHours_UsesConfiguredValue proves
// SetDefaultGateTimeoutHours actually changes what gets bound — the whole point of making
// this configurable is that HUMAN_GATE_DEFAULT_TIMEOUT_H=48 (say) must reach this call,
// not just exist in code.
func TestTaskRepo_ArmHumanGate_DefaultTimeoutHours_UsesConfiguredValue(t *testing.T) {
	repo, mock, _ := captureTaskRepoSQL(t)
	repo.SetDefaultGateTimeoutHours(48)
	mock.ExpectExec("").WithArgs(
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), 48,
	).WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID: uuid.New(), Author: uuid.New(), AuthorType: domain.ActorTypeAgent,
		RecommendedDefault: "merge; gateway is inactive", Class: domain.HumanGateClassSoft,
	}))
}

// TestTaskRepo_ArmHumanGate_DefaultTimeoutHours_NonPositiveOverrideFallsBack: a caller
// that sets a zero or negative window (a bug upstream, since config.go's own getEnvInt
// parse-failure path already falls back before this point) must still floor to the
// built-in default, not compute a deadline of "right now" or "in the past".
func TestTaskRepo_ArmHumanGate_DefaultTimeoutHours_NonPositiveOverrideFallsBack(t *testing.T) {
	repo, mock, _ := captureTaskRepoSQL(t)
	repo.SetDefaultGateTimeoutHours(-5)
	mock.ExpectExec("").WithArgs(
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), DefaultGateTimeoutHours,
	).WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID: uuid.New(), Author: uuid.New(), AuthorType: domain.ActorTypeAgent,
		RecommendedDefault: "merge; gateway is inactive", Class: domain.HumanGateClassSoft,
	}))
}

func TestTaskRepo_ArmHumanGate_PropagatesDriverError(t *testing.T) {
	repo, mock, _ := captureTaskRepoSQL(t)

	mock.ExpectExec("").WillReturnError(errors.New("connection reset"))
	err := repo.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID: uuid.New(), Author: uuid.New(), AuthorType: domain.ActorTypeAgent,
		Class: domain.HumanGateClassHard,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection reset",
		"a failed arm must surface, never be swallowed into a silently un-gated task")
}
