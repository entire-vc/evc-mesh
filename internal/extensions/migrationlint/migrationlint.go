// Package migrationlint enforces the table-name rule for extension migrations:
// an extension may create, alter or drop only tables named ext_<id>_*, where
// <id> is its extension id with '-' written as '_'.
package migrationlint

import (
	"fmt"
	"regexp"
	"strings"
)

// TablePrefix is the namespace of one extension's tables.
func TablePrefix(extensionID string) string {
	return "ext_" + strings.ReplaceAll(extensionID, "-", "_") + "_"
}

// Violation is one statement that touches a table outside the namespace.
type Violation struct {
	Statement string
	Table     string
}

func (v Violation) String() string {
	return fmt.Sprintf("table %q is outside the extension namespace (%s)", v.Table, v.Statement)
}

var (
	// Every statement form that names a table it creates, changes, locks or
	// attaches something to. The name that follows may be schema-qualified and
	// double-quoted. UPDATE and COPY only count at the start of a statement (or after
	// "(" for a CTE) so that ON CONFLICT DO UPDATE, FOR UPDATE and trigger events
	// are not read as a table target. Reading a foreign table (SELECT, FROM in an
	// INSERT ... SELECT) is not a violation.
	tableStmt = regexp.MustCompile(`(?i)(?:\b(` +
		`CREATE\s+(?:(?:GLOBAL\s+|LOCAL\s+)?(?:TEMP|TEMPORARY)\s+|UNLOGGED\s+)?TABLE(?:\s+IF\s+NOT\s+EXISTS)?` +
		`|ALTER\s+TABLE(?:\s+IF\s+EXISTS)?(?:\s+ONLY)?` +
		`|DROP\s+TABLE(?:\s+IF\s+EXISTS)?` +
		`|TRUNCATE(?:\s+TABLE)?(?:\s+ONLY)?` +
		`|INSERT\s+INTO` +
		`|MERGE\s+INTO(?:\s+ONLY)?` +
		`|DELETE\s+FROM(?:\s+ONLY)?` +
		`|CREATE\s+(?:UNIQUE\s+)?INDEX(?:\s+CONCURRENTLY)?(?:\s+IF\s+NOT\s+EXISTS)?(?:\s+[^\s;(]+)?\s+ON(?:\s+ONLY)?` +
		`|CREATE\s+(?:OR\s+REPLACE\s+)?(?:CONSTRAINT\s+)?TRIGGER\s+[^;]*?\sON` +
		`|(?:CREATE|ALTER|DROP)\s+POLICY(?:\s+IF\s+EXISTS)?\s+[^\s;]+\s+ON` +
		`|DROP\s+TRIGGER(?:\s+IF\s+EXISTS)?\s+[^\s;]+\s+ON` +
		`|COMMENT\s+ON\s+TABLE` +
		`|LOCK\s+TABLE(?:\s+ONLY)?` +
		`|REINDEX\s+TABLE(?:\s+CONCURRENTLY)?` +
		`|(?:GRANT|REVOKE)\s+[^;]*?\sON\s+TABLE)` +
		`|(?:^|[;(])\s*(UPDATE(?:\s+ONLY)?|COPY))\s+([^;]+)`)
	identPart = `(?:"[^"]+"|[A-Za-z_][A-Za-z0-9_$]*)`
	// optional schema, then the table; group 1 is the table.
	// ALTER TABLE x RENAME TO y: y becomes a table name too.
	// ALTER TABLE x SET SCHEMA y moves the table out of the schema the purge job reads.
	setSchema = regexp.MustCompile(`(?i)\bSET\s+SCHEMA\b`)
	renameTo  = regexp.MustCompile(`(?i)\bRENAME\s+TO\s+([^\s;,]+)`)
	nameRe    = regexp.MustCompile(`^(?:(` + identPart + `)\.)?(` + identPart + `)`)
)

// identName is the name PostgreSQL stores: an unquoted identifier is folded to
// lower case, a double-quoted one keeps its exact case.
func identName(ident string) string {
	if strings.HasPrefix(ident, `"`) {
		return strings.Trim(ident, `"`)
	}
	return strings.ToLower(ident)
}

var (
	dollarTag    = regexp.MustCompile(`^[A-Za-z_]*$`)
	singleQuoted = regexp.MustCompile(`'(?:[^']|'')*'`)
	// Statements an extension migration may contain. Anything else is a violation.
	allowedDDL = regexp.MustCompile(`(?i)^(CREATE|ALTER|DROP)\s+(?:(?:OR\s+REPLACE|UNIQUE|UNLOGGED|GLOBAL|LOCAL|TEMP|TEMPORARY|MATERIALIZED|CONSTRAINT)\s+)*(TABLE|INDEX|SEQUENCE|VIEW|TRIGGER|POLICY)\b`)
	allowedDML = regexp.MustCompile(`(?i)^(?:INSERT\s+INTO|UPDATE|DELETE\s+FROM|MERGE\s+INTO|TRUNCATE|LOCK|REINDEX|COPY|GRANT|REVOKE|COMMENT\s+ON\s+(?:TABLE|COLUMN))\b`)
	// Objects whose own name must sit in the namespace (a table is checked by tableStmt).
	namedObject = regexp.MustCompile(`(?i)^(CREATE|ALTER|DROP)\s+(?:(?:OR\s+REPLACE|UNIQUE|UNLOGGED|GLOBAL|LOCAL|TEMP|TEMPORARY|MATERIALIZED)\s+)*(INDEX|SEQUENCE|VIEW)\s+(?:CONCURRENTLY\s+)?(?:IF\s+(?:NOT\s+)?EXISTS\s+)?([^;]*)`)
	// An index name does not say which table owns it, so DROP INDEX / ALTER INDEX
	// could reach an index on a core table. Extensions drop a table or a constraint instead.
	indexByName   = regexp.MustCompile(`(?i)^(?:DROP|ALTER)\s+INDEX\b`)
	grantStmt     = regexp.MustCompile(`(?i)^(?:GRANT|REVOKE)\b`)
	grantOnTable  = regexp.MustCompile(`(?i)\bON\s+TABLE\b`)
	commentColumn = regexp.MustCompile(`(?i)^COMMENT\s+ON\s+COLUMN\s+(\S+)`)
	// Partitioning and inheritance reach a table outside the namespace.
	forbiddenClause = regexp.MustCompile(`(?i)\bPARTITION\s+OF\b|\b(?:ATTACH|DETACH)\s+PARTITION\b|\bINHERITS?\b`)
	references      = regexp.MustCompile(`(?i)\bREFERENCES\s+(` + `(?:` + identPart + `\.)?` + identPart + `)`)
)

// coreReferenceTables are the core tables an extension table may point a foreign key at.
var coreReferenceTables = map[string]bool{"workspaces": true, "projects": true, "tasks": true, "agents": true, "users": true}

// splitStatements cuts sql at ';' outside single quotes, double quotes and dollar quotes.
func splitStatements(sql string) []string {
	var out []string
	start, i := 0, 0
	for i < len(sql) {
		c := sql[i]
		switch c {
		case '\'', '"':
			j := strings.IndexByte(sql[i+1:], c)
			if j < 0 {
				i = len(sql)
			} else {
				i += j + 2
			}
		case '$':
			end := strings.IndexByte(sql[i+1:], '$')
			if end >= 0 && dollarTag.MatchString(sql[i+1:i+1+end]) {
				tag := sql[i : i+end+2]
				j := strings.Index(sql[i+len(tag):], tag)
				if j < 0 {
					i = len(sql)
				} else {
					i += len(tag) + j + len(tag)
				}
			} else {
				i++
			}
		case ';':
			out = append(out, sql[start:i])
			i++
			start = i
		default:
			i++
		}
	}
	return append(out, sql[start:])
}

// stripComments blanks "--" and "/* */" comments that sit outside single-quoted,
// double-quoted and dollar-quoted text, so a "--" inside a literal cannot hide the
// statements behind it.
func stripComments(sql string) string {
	var b strings.Builder
	i := 0
	for i < len(sql) {
		c := sql[i]
		switch {
		case c == '\'' || c == '"':
			j := strings.IndexByte(sql[i+1:], c)
			end := len(sql)
			if j >= 0 {
				end = i + j + 2
			}
			b.WriteString(sql[i:end])
			i = end
		case c == '$':
			end := strings.IndexByte(sql[i+1:], '$')
			if end >= 0 && dollarTag.MatchString(sql[i+1:i+1+end]) {
				tag := sql[i : i+end+2]
				stop := len(sql)
				if j := strings.Index(sql[i+len(tag):], tag); j >= 0 {
					stop = i + len(tag) + j + len(tag)
				}
				b.WriteString(sql[i:stop])
				i = stop
			} else {
				b.WriteByte(c)
				i++
			}
		case c == '-' && strings.HasPrefix(sql[i:], "--"):
			j := strings.IndexByte(sql[i:], '\n')
			if j < 0 {
				i = len(sql)
			} else {
				i += j
			}
			b.WriteByte(' ')
		case c == '/' && strings.HasPrefix(sql[i:], "/*"):
			j := strings.Index(sql[i+2:], "*/")
			if j < 0 {
				i = len(sql)
			} else {
				i += j + 4
			}
			b.WriteByte(' ')
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

func inNamespace(ref, prefix string) (string, bool) {
	m := nameRe.FindStringSubmatch(ref)
	if m == nil {
		return ref, false
	}
	if m[1] != "" && identName(m[1]) != "public" {
		return identName(m[1]) + "." + identName(m[2]), false
	}
	t := identName(m[2])
	return t, strings.HasPrefix(t, prefix) && len(t) > len(prefix)
}

// lintStatements is the allow-list layer: a statement must be a known form,
// must not use partitioning or inheritance, and may reference only its own
// tables or core tables.
func lintStatements(sql, prefix string) []Violation {
	var out []Violation
	for _, st := range splitStatements(sql) {
		st = strings.TrimSpace(st)
		if st == "" {
			continue
		}
		head := strings.Join(strings.Fields(st), " ")
		if len(head) > 40 {
			head = head[:40]
		}
		if !allowedDDL.MatchString(st) && !allowedDML.MatchString(st) {
			out = append(out, Violation{Statement: "unsupported statement", Table: head})
			continue
		}
		bare := singleQuoted.ReplaceAllString(st, "''")
		if indexByName.MatchString(st) {
			out = append(out, Violation{Statement: "DROP/ALTER INDEX by name", Table: head})
		}
		// Only "ON TABLE <names>" grants have a target the table check can read;
		// ON ALL TABLES IN SCHEMA, ON SCHEMA, ON FUNCTION … reach beyond the namespace.
		if grantStmt.MatchString(st) && !grantOnTable.MatchString(bare) {
			out = append(out, Violation{Statement: "GRANT/REVOKE without ON TABLE", Table: head})
		}
		if forbiddenClause.MatchString(bare) {
			out = append(out, Violation{Statement: "partitioning or inheritance", Table: head})
		}
		for _, r := range references.FindAllStringSubmatch(bare, -1) {
			if t, ok := inNamespace(r[1], prefix); !ok && !(coreReferenceTables[t]) {
				out = append(out, Violation{Statement: "REFERENCES", Table: t})
			}
		}
		if m := namedObject.FindStringSubmatch(bare); len(m) == 4 && (!strings.EqualFold(m[1], "CREATE") || !strings.EqualFold(m[2], "INDEX")) {
			names := []string{m[3]}
			if strings.EqualFold(m[1], "DROP") {
				names = strings.Split(m[3], ",")
			}
			for _, n := range names {
				n = strings.TrimSpace(n)
				if n == "" {
					continue
				}
				if t, ok := inNamespace(n, prefix); !ok {
					out = append(out, Violation{Statement: strings.ToUpper(m[1] + " " + m[2]), Table: t})
				}
			}
		}
		if m := commentColumn.FindStringSubmatch(bare); m != nil {
			col := strings.TrimSpace(m[1])
			parts := strings.Split(col, ".")
			if len(parts) >= 3 && identName(parts[0]) == "public" {
				parts = parts[1:]
			}
			if t := identName(parts[0]); len(parts) < 2 || !strings.HasPrefix(t, prefix) {
				out = append(out, Violation{Statement: "COMMENT ON COLUMN", Table: t})
			}
		}
	}
	return out
}

// Lint returns every table a statement creates, changes or locks in sql that is not ext_<id>_*. Comments
// are ignored. A reference it cannot parse is reported as a violation, never
// skipped.
func Lint(extensionID, sql string) []Violation {
	sql = stripComments(sql)
	prefix := TablePrefix(extensionID)
	out := lintStatements(sql, prefix)
	for _, m := range tableStmt.FindAllStringSubmatch(sql, -1) {
		verbText := m[1]
		if verbText == "" {
			verbText = m[2]
		}
		verb := strings.ToUpper(strings.Join(strings.Fields(verbText), " "))
		rest := m[3]
		names := []string{rest}
		if strings.HasPrefix(verb, "DROP TABLE") || strings.HasPrefix(verb, "TRUNCATE") ||
			strings.HasPrefix(verb, "GRANT") || strings.HasPrefix(verb, "REVOKE") {
			names = strings.Split(rest, ",")
		}
		for _, n := range names {
			n = strings.TrimSpace(n)
			if n == "" || (verb == "COPY" && strings.HasPrefix(n, "(")) {
				continue
			}
			if strings.HasPrefix(verb, "ALTER TABLE") {
				if setSchema.MatchString(n) {
					out = append(out, Violation{Statement: verb + " SET SCHEMA", Table: n})
				}
				if r := renameTo.FindStringSubmatch(n); r != nil {
					if t := identName(r[1]); !strings.HasPrefix(t, prefix) || len(t) == len(prefix) {
						out = append(out, Violation{Statement: verb + " RENAME TO", Table: t})
					}
				}
			}
			m := nameRe.FindStringSubmatch(n)
			if m == nil {
				out = append(out, Violation{Statement: verb, Table: n})
				continue
			}
			// The purge job only looks in the default schema, so a table in any
			// other schema would outlive a removed extension.
			if m[1] != "" && identName(m[1]) != "public" {
				out = append(out, Violation{Statement: verb, Table: identName(m[1]) + "." + identName(m[2])})
				continue
			}
			table := identName(m[2])
			if !strings.HasPrefix(table, prefix) || len(table) == len(prefix) {
				out = append(out, Violation{Statement: verb, Table: table})
			}
		}
	}
	return out
}
