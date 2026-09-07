package service

import (
	"strings"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// hardGateDisposableLabels are the labels that mark a task as throwaway/probe —
// created once to exercise something (a test fixture, a one-off repro, a sweep
// artifact) and never revisited by a human afterward. Task #318de303: 3 of the 27
// human-gate cards Riker's 07.09 triage found sitting on Pavel's queue were exactly
// this shape — a disposable card armed a HARD gate that then waited forever, because
// nobody was ever coming back to answer it.
var hardGateDisposableLabels = map[string]struct{}{
	"throwaway":  {},
	"probe":      {},
	"kind:sweep": {},
}

// hardGateFixtureTitleMarker is the literal marker (matched case-insensitively,
// since task titles are freeform prose) that names a task as a test-harness
// fixture rather than real work — the form task #318de303's description names
// explicitly ("HARNESS FIXTURE в заголовке").
const hardGateFixtureTitleMarker = "HARNESS FIXTURE"

// isDisposableGateTarget reports whether task is marked throwaway/probe — by a
// label in hardGateDisposableLabels (case-insensitive) or by hardGateFixtureTitleMarker
// appearing in its title (case-insensitive) — and, when true, names which marker
// fired so a refusal or a downgrade can say why.
func isDisposableGateTarget(task *domain.Task) (marker string, disposable bool) {
	if task == nil {
		return "", false
	}
	for _, l := range task.Labels {
		if _, ok := hardGateDisposableLabels[strings.ToLower(l)]; ok {
			return "label:" + l, true
		}
	}
	if strings.Contains(strings.ToUpper(task.Title), hardGateFixtureTitleMarker) {
		return "title contains \"" + hardGateFixtureTitleMarker + "\"", true
	}
	return "", false
}
