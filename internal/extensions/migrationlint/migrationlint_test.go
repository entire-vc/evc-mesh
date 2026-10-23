package migrationlint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTablePrefix(t *testing.T) {
	require.Equal(t, "ext_hello_world_", TablePrefix("hello-world"))
}

func TestViolation_String(t *testing.T) {
	v := Violation{Statement: "CREATE TABLE", Table: "tasks"}
	require.Equal(t, `table "tasks" is outside the extension namespace (CREATE TABLE)`, v.String())
}

func TestLint_UnparsableAndEmptyNames(t *testing.T) {
	require.Len(t, Lint("hello", `DROP TABLE (oops);`), 1, "a name that cannot be parsed is a violation, not skipped")
	require.Empty(t, Lint("hello", `DROP TABLE ext_hello_a, ;`), "an empty list item is ignored")
}

func TestLint_AcceptsOwnNamespace(t *testing.T) {
	sql := `-- +goose Up
CREATE TABLE ext_hello_notes (id uuid PRIMARY KEY);
CREATE TABLE IF NOT EXISTS public."ext_hello_tags" (id int);
ALTER TABLE ONLY ext_hello_notes ADD COLUMN body text;
/* DROP TABLE tasks; */
-- DROP TABLE users;
DROP TABLE IF EXISTS ext_hello_notes, ext_hello_tags;`
	require.Empty(t, Lint("hello", sql))
	require.Empty(t, Lint("hello", `CREATE TABLE "ext_hello_quoted" (id int);`), "a quoted lower-case name in the namespace is fine")
	own := `
INSERT INTO ext_hello_a (id) SELECT id FROM tasks;
UPDATE ext_hello_a SET x = 1;
DELETE FROM ext_hello_a USING tasks WHERE ext_hello_a.id = tasks.id;
INSERT INTO ext_hello_a (id) VALUES (1) ON CONFLICT (id) DO UPDATE SET x = 2;
CREATE INDEX ext_hello_a_idx ON ext_hello_a (id);
CREATE TRIGGER ext_hello_t AFTER UPDATE OF x ON ext_hello_a FOR EACH ROW EXECUTE FUNCTION ext_hello_f();
CREATE POLICY ext_hello_p ON ext_hello_a USING (true);
ALTER POLICY ext_hello_p ON ext_hello_a RENAME TO ext_other_name;
DROP TRIGGER IF EXISTS ext_hello_t ON ext_hello_a;
COMMENT ON TABLE ext_hello_a IS 'x';
LOCK TABLE ext_hello_a IN ACCESS EXCLUSIVE MODE;
REINDEX TABLE ext_hello_a;
MERGE INTO ext_hello_a t USING tasks s ON t.id = s.id WHEN MATCHED THEN DO NOTHING;
COPY ext_hello_a (id) FROM '/tmp/x.csv';
COPY (SELECT id FROM tasks) TO '/tmp/y.csv';
GRANT SELECT ON TABLE ext_hello_a TO PUBLIC;`
	require.Empty(t, Lint("hello", own), "in-namespace DML/DDL and reads of other tables are fine")
	require.Empty(t, Lint("hello", `CREATE TABLE ext_hello_fk (id int REFERENCES tasks (id), o int REFERENCES ext_hello_a (id)); CREATE SEQUENCE ext_hello_s; COMMENT ON COLUMN ext_hello_a.x IS 'a; b'; INSERT INTO ext_hello_a (x) VALUES ('PARTITION OF; INHERITS');`), "own refs, core FK, semicolon in a literal")
	require.Empty(t, Lint("hello", `ALTER TABLE ext_hello_a RENAME TO ext_hello_b; ALTER TABLE ext_hello_b RENAME COLUMN x TO y;`))
}

func TestLint_RejectsForeignTables(t *testing.T) {
	cases := map[string]string{
		"create core":          `CREATE TABLE tasks (id int);`,
		"alter core":           `ALTER TABLE users ADD COLUMN x int;`,
		"drop core":            `DROP TABLE IF EXISTS projects;`,
		"other extension":      `CREATE TABLE ext_other_notes (id int);`,
		"prefix only":          `CREATE TABLE ext_hello_ (id int);`,
		"sibling id prefix":    `CREATE TABLE ext_hellox_notes (id int);`,
		"qualified":            `CREATE TABLE public.tasks (id int);`,
		"set schema":           `ALTER TABLE ext_hello_a SET SCHEMA other;`,
		"other schema":         `CREATE TABLE other.ext_hello_data (id int);`,
		"other schema quoted":  `ALTER TABLE "Other"."ext_hello_data" ADD COLUMN x int;`,
		"second in drop":       `DROP TABLE ext_hello_a, tasks;`,
		"rename out":           `ALTER TABLE ext_hello_notes RENAME TO tasks;`,
		"rename to prefix":     `ALTER TABLE ext_hello_notes RENAME TO ext_hello_;`,
		"quoted upper case":    `CREATE TABLE "EXT_HELLO_TASKS" (id int);`,
		"quoted rename":        `ALTER TABLE ext_hello_a RENAME TO "EXT_HELLO_B";`,
		"insert":               `INSERT INTO tasks (id) VALUES (1);`,
		"update":               `UPDATE tasks SET title = 'x';`,
		"update only":          `UPDATE ONLY tasks SET title = 'x';`,
		"delete":               `DELETE FROM tasks WHERE true;`,
		"update in cte":        `WITH d AS (UPDATE tasks SET title = 'x' RETURNING id) SELECT * FROM d;`,
		"create index":         `CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS i ON tasks (id);`,
		"create index anon":    `CREATE INDEX ON tasks (id);`,
		"create trigger":       `CREATE TRIGGER t AFTER INSERT OR UPDATE ON tasks FOR EACH ROW EXECUTE FUNCTION f();`,
		"create policy":        `CREATE POLICY p ON tasks USING (true);`,
		"drop trigger":         `DROP TRIGGER IF EXISTS t ON tasks;`,
		"comment":              `COMMENT ON TABLE tasks IS 'x';`,
		"lock":                 `LOCK TABLE tasks IN ACCESS EXCLUSIVE MODE;`,
		"reindex":              `REINDEX TABLE tasks;`,
		"merge":                `MERGE INTO tasks t USING ext_hello_a s ON t.id = s.id WHEN MATCHED THEN DO NOTHING;`,
		"copy":                 `COPY tasks (id) FROM '/tmp/x.csv';`,
		"grant":                `GRANT SELECT ON TABLE tasks TO PUBLIC;`,
		"grant second":         `GRANT SELECT ON TABLE ext_hello_a, tasks TO PUBLIC;`,
		"truncate":             `TRUNCATE TABLE workspaces;`,
		"dash in literal":      `CREATE TABLE ext_hello_a (x text DEFAULT '--'); DROP TABLE tasks;`,
		"block in literal":     `CREATE TABLE ext_hello_a (x text DEFAULT '/*'); DROP TABLE tasks; --*/`,
		"dash in dollar":       `COMMENT ON TABLE ext_hello_a IS $$ -- $$; DROP TABLE tasks;`,
		"grant all tables":     `GRANT SELECT ON ALL TABLES IN SCHEMA public TO PUBLIC;`,
		"revoke all tables":    `REVOKE ALL ON ALL TABLES IN SCHEMA public FROM PUBLIC;`,
		"grant schema":         `GRANT USAGE ON SCHEMA public TO PUBLIC;`,
		"grant untyped":        `GRANT SELECT ON tasks TO PUBLIC;`,
		"drop own-named index": `DROP INDEX ext_hello_anything;`,
		"alter index rename":   `ALTER INDEX IF EXISTS ext_hello_i RENAME TO ext_hello_j;`,
		"temp":                 `CREATE TEMP TABLE scratch (id int);`,
		"partition of":         `CREATE TABLE ext_hello_child PARTITION OF tasks FOR VALUES IN (1);`,
		"partition of own":     `CREATE TABLE ext_hello_child PARTITION OF ext_hello_a FOR VALUES IN (1);`,
		"attach partition":     `ALTER TABLE ext_hello_parent ATTACH PARTITION tasks FOR VALUES IN (1);`,
		"detach partition":     `ALTER TABLE ext_hello_parent DETACH PARTITION ext_hello_old;`,
		"inherits":             `CREATE TABLE ext_hello_c (x int) INHERITS (tasks);`,
		"inherit":              `ALTER TABLE ext_hello_c INHERIT tasks;`,
		"unknown statement":    `SELECT pg_terminate_backend(1);`,
		"create function":      `CREATE FUNCTION ext_hello_f() RETURNS int AS $$ DELETE FROM tasks; SELECT 1 $$ LANGUAGE sql;`,
		"drop schema":          `DROP SCHEMA public CASCADE;`,
		"alter system":         `ALTER SYSTEM SET work_mem = '1GB';`,
		"fk to other table":    `CREATE TABLE ext_hello_fk (id int REFERENCES secrets (id));`,
		"fk to other ext":      `ALTER TABLE ext_hello_a ADD FOREIGN KEY (x) REFERENCES ext_other_t (id);`,
		"drop core index":      `DROP INDEX idx_tasks_status;`,
		"drop core sequence":   `DROP SEQUENCE IF EXISTS tasks_seq, ext_hello_s;`,
		"create core view":     `CREATE VIEW tasks_v AS SELECT 1;`,
		"comment on column":    `COMMENT ON COLUMN tasks.title IS 'x';`,
		"comment on index":     `COMMENT ON INDEX idx_tasks IS 'x';`,
	}
	for name, sql := range cases {
		t.Run(name, func(t *testing.T) {
			require.NotEmpty(t, Lint("hello", sql), sql)
		})
	}
}

// Red control: the same checker, fed a deliberately broken migration file,
// reports it. Without this the green run over real migrations proves nothing.
func TestLint_BrokenFixtureIsRed(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "bad", "hello", "migrations", "001_breaks_namespace.sql"))
	require.NoError(t, err)
	v := Lint("hello", string(b))
	require.Len(t, v, 1)
	require.Equal(t, "tasks", v[0].Table)
}

// TestExtensionMigrationsStayInNamespace is the CI gate: every migration under
// internal/extensions/<id>/migrations/*.sql may touch only ext_<id>_* tables.
func TestExtensionMigrationsStayInNamespace(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "*", "migrations", "*.sql"))
	require.NoError(t, err)
	for _, f := range files {
		id := filepath.Base(filepath.Dir(filepath.Dir(f)))
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		for _, v := range Lint(id, string(b)) {
			t.Errorf("%s: %s", strings.TrimPrefix(f, "../"), v)
		}
	}
}
