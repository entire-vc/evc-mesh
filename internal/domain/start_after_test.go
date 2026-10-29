package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseStartAfter(t *testing.T) {
	cases := []struct {
		raw    string
		human  bool
		want   string // UTC RFC 3339, empty when refused
		offset string
		code   string
	}{
		{"2026-10-30T06:00:00Z", false, "2026-10-30T06:00:00Z", "Z", ""},
		{"2026-10-30T09:00:00+03:00", false, "2026-10-30T06:00:00Z", "+03:00", ""},
		{"2026-10-30T06:00:00", false, "", "", CodeNaiveTime},
		{"2026-10-30T06:00:00", true, "", "", CodeNaiveTime},
		{"2026-10-30", false, "", "", CodeNaiveTime},
		{"2026-10-30", true, "2026-10-30T00:00:00Z", "", ""},
		{"6h", false, "", "", CodeRelativeTime},
		{"+6h", false, "", "", CodeRelativeTime},
		{"tomorrow", true, "", "", CodeRelativeTime},
		{"in 2 days", false, "", "", CodeRelativeTime},
		{"soonish", false, "", "", CodeInvalidTarget},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			at, off, err := ParseStartAfter(tc.raw, tc.human)
			if tc.code != "" {
				var oe *OutcomeError
				require.ErrorAs(t, err, &oe)
				assert.Equal(t, tc.code, oe.Code)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, at.Format(time.RFC3339))
			assert.Equal(t, tc.offset, off)
		})
	}
}

func TestValidateStartAfterEvidence(t *testing.T) {
	ok := func() *StartAfterEvidence {
		return &StartAfterEvidence{EventRef: "nightly-export", Source: "https://ci.example/s", Why: "the export publishes at this time"}
	}
	require.NoError(t, ValidateStartAfterEvidence(ok()))
	comment := ok()
	comment.Source = "comment:" + uuid.NewString()
	require.NoError(t, ValidateStartAfterEvidence(comment), "a Mesh receipt is a valid shape; existence is the repository's check")

	for name, mutate := range map[string]func(*StartAfterEvidence){
		"no event_ref": func(e *StartAfterEvidence) { e.EventRef = " " },
		"no source":    func(e *StartAfterEvidence) { e.Source = "" },
		"short why":    func(e *StartAfterEvidence) { e.Why = "later" },
		"http source":  func(e *StartAfterEvidence) { e.Source = "http://ci.example/s" },
		"card source":  func(e *StartAfterEvidence) { e.Source = "card:00000000" },
		"bad receipt":  func(e *StartAfterEvidence) { e.Source = "comment:nope" },
		"poll":         func(e *StartAfterEvidence) { e.EventRef = "poll" },
		"backoff":      func(e *StartAfterEvidence) { e.EventRef = "gitlab-backoff" },
		"retry 6h":     func(e *StartAfterEvidence) { e.EventRef = "retry 6h" },
		"long event":   func(e *StartAfterEvidence) { e.EventRef = string(make([]byte, 501)) },
	} {
		t.Run(name, func(t *testing.T) {
			e := ok()
			mutate(e)
			assert.Error(t, ValidateStartAfterEvidence(e))
		})
	}
	assert.Error(t, ValidateStartAfterEvidence(nil))
}

// Mismatch is the compare of compare-clear: the same instant is not enough
// when the version, origin or receipt moved on (ABA).
func TestStartAfterCompareClearMismatch(t *testing.T) {
	at := time.Date(2026, 10, 30, 6, 0, 0, 123456789, time.UTC)
	origin := StartAfterAgentEvidence
	receipt := uuid.New()
	task := func() *Task {
		a := at.Truncate(time.Microsecond) // as stored by PostgreSQL
		o, r := origin, receipt
		return &Task{StartAfter: &a, StartAfterOrigin: &o, StartAfterReceiptID: &r, Version: 7}
	}
	v := int64(7)
	in := StartAfterCompareClear{ExpectedStartAfter: &at, ExpectedVersion: &v, ExpectedOrigin: &origin, ScheduleReceiptID: &receipt}
	assert.Nil(t, in.Mismatch(task()), "the read schedule matches at microsecond precision")

	newer := task()
	newer.Version = 8
	c := in.Mismatch(newer)
	require.NotNil(t, c, "same instant, new version: ABA")
	assert.EqualValues(t, 8, c.CurrentVersion)

	human := task()
	h := StartAfterHumanExplicit
	human.StartAfterOrigin = &h
	assert.NotNil(t, in.Mismatch(human), "a person re-set the same date")

	other := task()
	r2 := uuid.New()
	other.StartAfterReceiptID = &r2
	c = in.Mismatch(other)
	require.NotNil(t, c, "another schedule revision")
	assert.Equal(t, r2, *c.CurrentReceipt)

	gone := &Task{Version: 7}
	c = in.Mismatch(gone)
	require.NotNil(t, c, "already cleared is a conflict, not a false cleared")
	assert.Nil(t, c.CurrentStartAfter)
}

func TestStartAfter_CompareClearValidate(t *testing.T) {
	at := time.Date(2026, 10, 30, 6, 0, 0, 0, time.UTC)
	v := int64(3)
	origin := StartAfterAgentEvidence
	legacy := StartAfterLegacyUnknown
	long := strings.Repeat("x", MaxTimeEvidenceLen+1)
	cases := []struct {
		name  string
		in    StartAfterCompareClear
		human bool
		ok    bool
	}{
		{"no expected date", StartAfterCompareClear{ExpectedVersion: &v, IdempotencyKey: "k"}, true, false},
		{"no version", StartAfterCompareClear{ExpectedStartAfter: &at, IdempotencyKey: "k"}, true, false},
		{"no key", StartAfterCompareClear{ExpectedStartAfter: &at, ExpectedVersion: &v}, true, false},
		{"person", StartAfterCompareClear{ExpectedStartAfter: &at, ExpectedVersion: &v, IdempotencyKey: "k"}, true, true},
		{"agent without origin", StartAfterCompareClear{ExpectedStartAfter: &at, ExpectedVersion: &v, IdempotencyKey: "k"}, false, false},
		{"agent without receipt", StartAfterCompareClear{ExpectedStartAfter: &at, ExpectedVersion: &v, IdempotencyKey: "k", ExpectedOrigin: &origin}, false, false},
		{"long reason", StartAfterCompareClear{ExpectedStartAfter: &at, ExpectedVersion: &v, IdempotencyKey: "k", Reason: &long}, true, false},
		{"long key", StartAfterCompareClear{ExpectedStartAfter: &at, ExpectedVersion: &v, IdempotencyKey: strings.Repeat("k", 201)}, true, false},
		{"agent on legacy", StartAfterCompareClear{ExpectedStartAfter: &at, ExpectedVersion: &v, IdempotencyKey: "k", ExpectedOrigin: &legacy}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.in.Validate(tc.human)
			if tc.ok {
				assert.NoError(t, err)
			} else {
				assert.Equal(t, CodeInvalidTarget, codeOf(err))
			}
		})
	}
}

func codeOf(err error) string {
	var oe *OutcomeError
	if errors.As(err, &oe) {
		return oe.Code
	}
	return ""
}
