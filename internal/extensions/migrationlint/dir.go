package migrationlint

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FileReport is the lint result of one migration file under one extension id.
// ExtensionID is empty when the file names an ext_ namespace no known
// extension owns; that is itself a violation.
type FileReport struct {
	File        string
	ExtensionID string
	Violations  []Violation
}

// target is one table a statement writes; schema is empty for the default
// ("public") schema.
type target struct{ schema, table string }

// Targets returns the tables sql creates, changes, renames to or writes, as
// PostgreSQL stores their names (schema "public" dropped, any other schema
// kept as "schema.table"). Comments are ignored; reads are not targets.
func Targets(sql string) []string {
	var out []string
	for _, t := range targets(sql) {
		if t.schema != "" {
			out = append(out, t.schema+"."+t.table)
			continue
		}
		out = append(out, t.table)
	}
	return out
}

func targets(sql string) []target {
	sql = stripComments(sql)
	var out []target
	for _, m := range tableStmt.FindAllStringSubmatch(sql, -1) {
		for _, n := range strings.Split(m[3], ",") {
			n = strings.TrimSpace(n)
			if r := renameTo.FindStringSubmatch(n); r != nil {
				out = append(out, target{table: identName(r[1])})
			}
			nm := nameRe.FindStringSubmatch(n)
			if nm == nil {
				continue
			}
			t := target{table: identName(nm[2])}
			if nm[1] != "" && identName(nm[1]) != "public" {
				t.schema = identName(nm[1])
			}
			out = append(out, t)
		}
	}
	return out
}

// Owner returns the extension whose namespace name falls in: the id with the
// longest TablePrefix that name starts with, so "ext_agent_chat_x" belongs to
// "agent-chat" even when "agent" is known too. ok is false when name is no
// ext_ name or no id owns it.
func Owner(name string, ids []string) (id string, ok bool) {
	return owner(name, ids, false)
}

// owner is Owner; whole also accepts the bare prefix ("ext_<id>_"), which is
// how a file name names its extension.
func owner(name string, ids []string, whole bool) (id string, ok bool) {
	best := -1
	for _, cand := range ids {
		p := TablePrefix(cand)
		if strings.HasPrefix(name, p) && (whole || len(name) > len(p)) && len(p) > best {
			id, best = cand, len(p)
		}
	}
	return id, best >= 0
}

// owners lists the extensions a migration file belongs to: the one its file
// name names (<version>_ext_<id>[_<anything>].sql) and every one whose
// namespace holds a table the file writes. A file that writes a table of
// another extension therefore belongs to both and fails the lint of each.
// Ownership reads the table part of a schema-qualified name, so
// other.ext_<id>_x still belongs to <id> (and Lint then rejects the schema).
// unknown collects ext_ names no id owns, schema included.
func owners(file, sql string, ids []string) (found, unknown []string) {
	seen := map[string]bool{}
	add := func(name, shown string, whole bool) {
		if !strings.HasPrefix(name, "ext_") {
			return
		}
		if id, ok := owner(name, ids, whole); ok {
			if !seen[id] {
				seen[id] = true
				found = append(found, id)
			}
			return
		}
		unknown = append(unknown, shown)
	}
	stem := strings.TrimSuffix(file, filepath.Ext(file))
	if i := strings.IndexByte(stem, '_'); i >= 0 {
		name := strings.ToLower(stem[i+1:]) + "_"
		add(name, name, true)
	}
	for _, t := range targets(sql) {
		shown := t.table
		if t.schema != "" {
			shown = t.schema + "." + t.table
		}
		add(t.table, shown, false)
	}
	sort.Strings(found)
	return found, unknown
}

// LintDir lints every *.sql file in dir that belongs to an extension (see
// owners) under the id of each extension it belongs to. Files that touch no
// ext_ namespace are core migrations and are not reported. The result is
// sorted by file, then extension id.
func LintDir(dir string, ids []string) ([]FileReport, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var out []FileReport
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f, err)
		}
		base := filepath.Base(f)
		found, unknown := owners(base, string(raw), ids)
		for _, u := range unknown {
			out = append(out, FileReport{File: base, Violations: []Violation{{Statement: "no known extension owns this ext_ namespace", Table: u}}})
		}
		written := targets(string(raw))
		for _, id := range found {
			v := Lint(id, string(raw))
			// Lint alone accepts ext_hello_world_x for "hello"; with every id
			// known, that table is hello-world's and its purge, not hello's.
			for _, t := range written {
				if o, ok := Owner(t.table, ids); ok && o != id && strings.HasPrefix(t.table, TablePrefix(id)) {
					v = append(v, Violation{Statement: "belongs to extension " + o, Table: t.table})
				}
			}
			out = append(out, FileReport{File: base, ExtensionID: id, Violations: v})
		}
	}
	return out, nil
}
