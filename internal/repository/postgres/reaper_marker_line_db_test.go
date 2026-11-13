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

// this change: comment text no longer decides whether an unleased in_progress card is
// abandoned. FindStaleUnleasedInProgress used to exempt a card whose latest
// non-system comment had a `⏳ WAIT` / `↻ NEXT` line younger than 48 h, for drivers
// that published no runtime_state. The last such driver (fiddler) is retired; Runner
// lanes publish runtime_state and typed waits keep a wait: ref. A marker with no live
// controller behind it is an abandoned card and is swept.
//
// The bodies are the shapes the old fallback exempted (incl. the this change replay of
// a WAIT on the last line of a long report), so this test was red before the change.
// The positive control — the same body with a live runtime_state — shows the sweep
// still spares a card a controller holds; the plain control shows the quiet card is
// swept with or without a marker.
func TestFindStaleUnleasedInProgress_MarkerTextIsNotAnExemption(t *testing.T) {
	f := newMidPipelineFixture(t)
	repo := NewTaskRepo(f.db)

	report := strings.Join([]string{
		"Итог по карточке:",
		"- backend готов, MR !1187 влит",
		"",
		"⏳ WAIT card:#00000000",
	}, "\n")

	markers := []struct{ name, body string }{
		{"WAIT opening the comment", "⏳ WAIT mr:entire-vc/evc-mesh!1110\nждём merge"},
		{"NEXT opening the comment", "↻ NEXT: добить тесты"},
		{"53f30091 replay: WAIT on the last line of a long report", report},
		{"CRLF WAIT line", "отчёт\r\n⏳ WAIT mr:example/product!42\r\n"},
		{"lower-case wait", "отчёт\n⏳ wait 20m: пайплайн"},
	}

	type probe struct {
		id    uuid.UUID
		swept bool
		label string
	}
	var probes []probe
	for _, m := range markers {
		bare := f.insertUnleasedTask(t, 5*time.Hour)
		f.insertCommentAs(t, bare, "agent", m.body, 3*time.Hour)
		probes = append(probes, probe{bare, true, m.name + ", no runtime_state"})

		held := f.insertUnleasedTask(t, 5*time.Hour)
		f.insertCommentAs(t, held, "agent", m.body, 3*time.Hour)
		f.setRuntimeState(t, held, "waiting", "card:#00000000", time.Minute, nil)
		probes = append(probes, probe{held, false, m.name + ", live runtime_state"})
	}
	plain := f.insertUnleasedTask(t, 5*time.Hour)
	f.insertCommentAs(t, plain, "agent", "жду merge, потом закрою", 3*time.Hour)
	probes = append(probes, probe{plain, true, "plain quiet comment"})

	got, err := repo.FindStaleUnleasedInProgress(context.Background(), 2*time.Hour)
	require.NoError(t, err)
	found := map[uuid.UUID]bool{}
	for _, task := range got {
		found[task.ID] = true
	}
	for _, p := range probes {
		t.Run(p.label, func(t *testing.T) {
			require.Equal(t, p.swept, found[p.id])
		})
	}
}
