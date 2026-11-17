package migrationlint

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigrationLintDir_Owner(t *testing.T) {
	ids := []string{"agent", "agent-chat", "hello"}
	for name, want := range map[string]string{
		"ext_agent_chat_runners": "agent-chat",
		"ext_agent_runs":         "agent",
		"ext_hello_a":            "hello",
	} {
		got, ok := Owner(name, ids)
		require.True(t, ok, name)
		require.Equal(t, want, got, name)
	}
	for _, name := range []string{"ext_hello_", "ext_ghost_a", "tasks", "installed_extensions"} {
		_, ok := Owner(name, ids)
		require.False(t, ok, name)
	}
}

func TestMigrationLintDir_Targets(t *testing.T) {
	require.Equal(t,
		[]string{"ext_a_x", "tasks", "ext_a_y", "ext_a_z", "other.ext_a_w", "ext_a_v"},
		Targets(`CREATE TABLE ext_a_x (id int REFERENCES projects (id));
-- DROP TABLE users;
INSERT INTO tasks SELECT * FROM agents;
DROP TABLE ext_a_y, ext_a_z;
CREATE TABLE other.ext_a_w (id int);
ALTER TABLE ext_a_v ADD COLUMN q int;`))
}

// Each fixture file is assigned to its extension by file name or by the
// tables it writes, and every report names its file.
func TestMigrationLintDir_Fixture(t *testing.T) {
	reports, err := LintDir(filepath.Join("testdata", "dir"), []string{"hello", "hello-world"})
	require.NoError(t, err)
	got := map[string]int{}
	for _, r := range reports {
		got[r.File+"|"+r.ExtensionID] += len(r.Violations)
	}
	require.Equal(t, map[string]int{
		"002_ext_hello.sql|hello":               1, // ALTER TABLE tasks
		"003_more_notes.sql|hello":              1, // owned through ext_hello_notes, indexes tasks
		"004_ext_ghost.sql|":                    2, // no owner: file name and table, one report each
		"005_ext_hello_world.sql|hello-world":   0, // longest prefix wins, FK to a core table is fine
		"006_two_namespaces.sql|hello":          1, // ext_hello_world_c is hello-world's, though Lint("hello") alone accepts it
		"006_two_namespaces.sql|hello-world":    1, // ext_hello_b is not hello-world's
		"007_qualified_ghost.sql|":              1, // other.ext_ghost_q: unknown namespace in another schema
		"008_qualified_sibling.sql|hello-world": 1, // owned by its table part, rejected for the schema
		"009_public_qualified.sql|hello-world":  0, // public. is the default schema
	}, got, "001_core.sql writes no ext_ table and is not an extension migration")
}

func TestMigrationLintDir_MissingDirIsEmpty(t *testing.T) {
	reports, err := LintDir(filepath.Join("testdata", "no-such-dir"), []string{"hello"})
	require.NoError(t, err)
	require.Empty(t, reports, "callers must assert they found files; an empty result is not a pass")
}
