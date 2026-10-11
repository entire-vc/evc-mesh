//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The live-marker exemption in FindStaleUnleasedInProgress must read a marker the
// way the fleet drivers do (bob scripts/fleet_wait.py DECLARATION and
// fleet_turn_outcome.py OUTCOME_LINE_RE): at the start of ANY line of the latest
// comment, not only at the start of the whole body.
//
// Incident 11.10: the lead's comment on #53f30091 (03:25Z) was a long report whose
// last line was `⏳ WAIT card:#2d54f716`. The anchored `^\s*(⏳ WAIT|↻ NEXT)` did not
// see it, the card was parked to backlog at 05:25Z, its WAIT was lost, and the UI
// card #14580276, formally blocked by it, stalled for ~16 h.
func TestFindStaleUnleasedInProgress_MarkerOnAnyLine(t *testing.T) {
	f := newMidPipelineFixture(t)
	repo := NewTaskRepo(f.db)

	report := strings.Join([]string{
		"Итог по карточке:",
		"- backend готов, MR !1187 влит",
		"- осталось: UI-часть в #14580276, ждёт этот API",
		"",
		"⏳ WAIT card:#2d54f716",
	}, "\n")

	cases := []struct {
		name  string
		body  string
		age   time.Duration
		swept bool
	}{
		// exempt: a marker opens some line of the latest comment
		{"53f30091 replay: WAIT on the last line of a long report", report, 3 * time.Hour, false},
		{"NEXT on the last line", "сделал миграцию\n↻ NEXT: добить тесты", 3 * time.Hour, false},
		{"bare NEXT on its own line", "сделал миграцию\n↻ NEXT\nдальше тесты", 3 * time.Hour, false},
		{"indented marker line", "отчёт\n   ⏳ WAIT 20m: пайплайн", 3 * time.Hour, false},
		{"CRLF line endings", "отчёт\r\n⏳ WAIT mr:entire-vc/evc-mesh!1187\r\n", 3 * time.Hour, false},
		{"lower-case marker, as the fleet parsers accept (re.I)", "отчёт\n⏳ wait 20m: пайплайн", 3 * time.Hour, false},
		{"marker on the first line still works", "⏳ WAIT card:#2d54f716\nподробности ниже", 3 * time.Hour, false},
		// parity pins with fleet_wait.DECLARATION / OUTCOME_LINE_RE
		{"no space between hourglass and WAIT", "отчёт\n⏳WAIT 20m: пайплайн", 3 * time.Hour, false},
		{"WAIT followed by punctuation", "отчёт\n⏳ WAIT-mr: ждём", 3 * time.Hour, false},
		{"NEXT with a space before the colon", "отчёт\n↻ NEXT : тесты", 3 * time.Hour, false},

		// swept: the 48h cap still applies to a marker on a later line
		{"WAIT on the last line but older than the cap", report, LiveWaitMarkerMaxAge + time.Hour, true},

		// swept: a marker that does not open a line is prose, not a declaration
		{"WAIT mid-line", "статус: ⏳ WAIT card:#2d54f716 позже", 3 * time.Hour, true},
		{"NEXT mid-line", "отчёт\nпотом ↻ NEXT: тесты", 3 * time.Hour, true},
		{"WAIT glued to a word (fleet requires a word boundary)", "отчёт\n⏳ WAITING for review", 3 * time.Hour, true},
		{"NEXT followed by prose without a colon (fleet requires ':' or end of line)", "отчёт\n↻ NEXT step is tests", 3 * time.Hour, true},
		{"two spaces between arrow and NEXT (fleet requires exactly one)", "отчёт\n↻  NEXT: тесты", 3 * time.Hour, true},
		{"bulleted marker is prose", "отчёт\n- ⏳ WAIT card:#2d54f716", 3 * time.Hour, true},
	}

	ids := make(map[string]uuid.UUID, len(cases))
	for _, tc := range cases {
		id := f.insertUnleasedTask(t, 5*time.Hour)
		f.insertCommentAs(t, id, "agent", tc.body, tc.age)
		ids[tc.name] = id
	}

	got, err := repo.FindStaleUnleasedInProgress(context.Background(), 2*time.Hour)
	require.NoError(t, err)
	found := map[uuid.UUID]bool{}
	for _, task := range got {
		found[task.ID] = true
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.swept, found[ids[tc.name]], "body %q, age %s", tc.body, tc.age)
		})
	}
}
