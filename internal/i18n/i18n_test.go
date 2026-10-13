package i18n

import "testing"

func TestEveryRUKeyExistsInEN(t *testing.T) {
	for k := range catalogRU {
		base := k
		for _, suf := range []string{".few", ".many"} {
			if len(k) > len(suf) && k[len(k)-len(suf):] == suf {
				base = k[:len(k)-len(suf)] + ".other"
			}
		}
		_, a := catalogEN[k]
		_, b := catalogEN[base]
		if !a && !b {
			t.Errorf("ru key %q has no en counterpart", k)
		}
	}
}

func TestT(t *testing.T) {
	p := map[string]any{"task": "Fix"}
	if got := T(EN, "notif.task.assigned.title", p); got != "Task assigned: Fix" {
		t.Fatalf("en: %q", got)
	}
	if got := T(RU, "notif.task.assigned.title", p); got != "Назначена задача: Fix" {
		t.Fatalf("ru: %q", got)
	}
	if got := T("de", "notif.task.assigned.title", p); got != "Task assigned: Fix" {
		t.Fatalf("unknown locale must fall back to en: %q", got)
	}
	if got := T(RU, "no.such.code", p); got != "" {
		t.Fatalf("unknown code must render empty, got %q", got)
	}
}

func TestPlural(t *testing.T) {
	for n, want := range map[int]string{1: "Ann: 1 правка.", 3: "Ann: 3 правки.", 5: "Ann: 5 правок.", 11: "Ann: 11 правок.", 21: "Ann: 21 правка."} {
		got := T(RU, "notif.doc.changed.body", map[string]any{"actor": "Ann", "count": n})
		if got != want {
			t.Errorf("ru %d: %q want %q", n, got, want)
		}
	}
	if got := T(EN, "notif.doc.changed.body", map[string]any{"actor": "Ann", "count": float64(2)}); got != "Ann made 2 edits." {
		t.Errorf("en json float: %q", got)
	}
}
