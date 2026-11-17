package builtin_test

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/builtin"
	"github.com/entire-vc/evc-mesh/internal/extensions/migrationlint"
)

// legacyViolations are the findings of migrations that were applied before
// the lint existed. An applied migration is never edited, so these stay; the
// list is exact: a new finding fails, and so does an entry that no longer shows
// up (remove it then). Key: "<file>|<extension id>".
var legacyViolations = map[string][]string{
	// agent-chat gives projects and agents the UNIQUE (id, workspace_id) its
	// composite foreign keys need (up), and drops it again (down).
	"20261022001_ext_agent_chat.sql|agent-chat": {
		`table "projects" is outside the extension namespace (ALTER TABLE)`,
		`table "agents" is outside the extension namespace (ALTER TABLE)`,
		`table "agents" is outside the extension namespace (ALTER TABLE)`,
		`table "projects" is outside the extension namespace (ALTER TABLE)`,
	},
	// One migration changed both ext_agent_chat_turns and the core agent_sessions.
	"20261023199_chat_turn_events.sql|agent-chat": {
		`table "DROP INDEX IF EXISTS agent_sessions_chat" is outside the extension namespace (DROP/ALTER INDEX by name)`,
		`table "agent_sessions_chat_thread" is outside the extension namespace (DROP INDEX)`,
		`table "agent_sessions" is outside the extension namespace (ALTER TABLE)`,
		`table "agent_sessions" is outside the extension namespace (CREATE INDEX AGENT_SESSIONS_CHAT_THREAD ON)`,
		`table "agent_sessions" is outside the extension namespace (ALTER TABLE)`,
	},
}

// Every migration that belongs to a builtin extension (its file name is
// <version>_ext_<id>…, or it writes an ext_<id>_* table) touches only that
// extension's tables. The ids come from builtin.Providers, so a new builtin
// extension is covered without editing this test, and a migration writing an
// ext_ namespace no builtin owns fails.
func TestMigrationLint_BuiltinExtensions(t *testing.T) {
	var ids []string
	for _, p := range builtin.Providers(extensions.Deps{}) {
		ids = append(ids, p.Manifest().ID)
	}
	reports, err := migrationlint.LintDir(filepath.Join("..", "..", "..", "migrations"), ids)
	require.NoError(t, err)

	checked := map[string][]string{}
	seenLegacy := map[string]bool{}
	for _, r := range reports {
		got := make([]string, 0, len(r.Violations))
		for _, v := range r.Violations {
			got = append(got, v.String())
		}
		key := r.File + "|" + r.ExtensionID
		if want, ok := legacyViolations[key]; ok {
			seenLegacy[key] = true
			require.Equal(t, want, got, "migrations/%s: the legacy findings changed; fix the new ones or update the list", r.File)
		} else {
			who := r.ExtensionID
			if who == "" {
				who = "no builtin extension"
			}
			for _, v := range got {
				t.Errorf("migrations/%s (extension %s): %s", r.File, who, v)
			}
		}
		if r.ExtensionID != "" {
			checked[r.ExtensionID] = append(checked[r.ExtensionID], r.File)
		}
	}
	for key := range legacyViolations {
		require.True(t, seenLegacy[key], "legacy entry %s matches no migration any more: remove it", key)
	}

	// Positive control: a walk that found nothing would pass vacuously.
	require.Contains(t, checked["dashboards"], "20261114100_ext_dashboards.sql")
	require.Contains(t, checked["agent-chat"], "20261023001_chat_runners.sql", "found through its ext_ table, not its file name")

	names := make([]string, 0, len(checked))
	for id := range checked {
		names = append(names, id)
	}
	sort.Strings(names)
	var without []string
	for _, id := range ids {
		if _, ok := checked[id]; !ok {
			without = append(without, id)
		}
	}
	sort.Strings(without)
	t.Logf("extensions checked: %d", len(names))
	for _, id := range names {
		t.Logf("  %s: %s", id, strings.Join(checked[id], ", "))
	}
	t.Logf("builtin extensions without migrations: %s", strings.Join(without, ", "))
}
