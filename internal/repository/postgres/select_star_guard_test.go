package postgres

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// selectStarPattern matches a bare `SELECT *` or an alias-qualified
// `SELECT <alias>.*`. sqlx's strict struct-scan (no Unsafe() is used
// anywhere in internal/) refuses to scan a column with no matching struct
// field, so either form breaks the instant an additive
// `ALTER TABLE ADD COLUMN` migration lands — a real, previously measured
// production 401 window.
var selectStarPattern = regexp.MustCompile(`(?i)SELECT\s+(DISTINCT\s+)?([A-Za-z_][A-Za-z0-9_]*\.)?\*`)

// recursiveCTEOpen matches the start of a `WITH RECURSIVE <name> AS (` —
// the only place a `SELECT *` is safe: inside a CTE branch that is never
// itself the target of GetContext/SelectContext (only the outer SELECT
// reading from the CTE is, and that one must stay explicit regardless).
var recursiveCTEOpen = regexp.MustCompile(`(?i)WITH\s+RECURSIVE\s+[A-Za-z_][A-Za-z0-9_]*\s+AS\s*\(`)

// findUnsafeSelectStars returns the byte offsets, within sql, of every
// `SELECT *` / `SELECT alias.*` that sits outside a recursive CTE's
// parenthesized body.
func findUnsafeSelectStars(sql string) []int {
	safeSpans := recursiveCTESpans(sql)
	var unsafe []int
	for _, loc := range selectStarPattern.FindAllStringIndex(sql, -1) {
		if !withinAny(loc[0], safeSpans) {
			unsafe = append(unsafe, loc[0])
		}
	}
	return unsafe
}

func withinAny(pos int, spans [][2]int) bool {
	for _, sp := range spans {
		if pos >= sp[0] && pos < sp[1] {
			return true
		}
	}
	return false
}

// recursiveCTESpans returns the [start,end) byte ranges covering the
// parenthesized body of every `WITH RECURSIVE ... AS (` in sql, found by
// tracking paren depth from the opening `(` to its match. A query merely
// CONTAINING a recursive CTE elsewhere is not enough to whitelist it — only
// text inside the CTE's own parens is exempt.
func recursiveCTESpans(sql string) [][2]int {
	var spans [][2]int
	for _, loc := range recursiveCTEOpen.FindAllStringIndex(sql, -1) {
		openParen := loc[1] - 1
		if end := matchingParen(sql, openParen); end >= 0 {
			spans = append(spans, [2]int{openParen, end + 1})
		}
	}
	return spans
}

func matchingParen(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// scanDirForUnsafeSelectStars parses every non-test .go file directly under
// dir and returns "file:line" for each unsafe SELECT * found in a string
// literal. Extracted as its own function (rather than inlined in the test)
// so TestScanDirForUnsafeSelectStars_MutationBothDirections can exercise
// the exact same AST-walking scan against throwaway fixture files.
func scanDirForUnsafeSelectStars(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	var violations []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return nil, err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, uqErr := strconv.Unquote(lit.Value)
			if uqErr != nil {
				value = lit.Value
			}
			for _, pos := range findUnsafeSelectStars(value) {
				line := fset.Position(lit.Pos()).Line + strings.Count(value[:pos], "\n")
				violations = append(violations, name+":"+strconv.Itoa(line))
			}
			return true
		})
	}
	return violations, nil
}

// TestNoUnsafeSelectStar is a regression guard for the class of bug fixed
// across this package: a bare `SELECT *` or alias-qualified `SELECT a.*`
// breaks the instant an additive migration adds a column the target struct
// doesn't know about yet. Every reader in this package lists its columns
// explicitly (see agentSelectCols in agent_repo.go for the pattern). The
// one exception is a `SELECT *` inside the body of a
// `WITH RECURSIVE ... AS (...)` CTE branch that is never itself the target
// of GetContext/SelectContext — see GetSubAgentTree in agent_repo.go.
func TestNoUnsafeSelectStar(t *testing.T) {
	violations, err := scanDirForUnsafeSelectStars(".")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("unsafe SELECT * (bare or alias-qualified) found — sqlx strict-scan breaks on the next additive migration:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// TestScanDirForUnsafeSelectStars_MutationBothDirections mutation-verifies
// the guard above by running the real scan against throwaway fixture
// files: it must go red on a freshly introduced SELECT * (bare or
// alias-qualified) and stay green on the legitimate recursive-CTE pattern
// and on an explicit column list.
func TestScanDirForUnsafeSelectStars_MutationBothDirections(t *testing.T) {
	write := func(t *testing.T, dir, src string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "fixture_repo.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("unsafe bare SELECT * is caught", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "package postgres\n\nconst q = `SELECT * FROM widgets WHERE id = $1`\n")
		violations, err := scanDirForUnsafeSelectStars(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(violations) == 0 {
			t.Fatal("expected the guard to flag the unsafe SELECT *, found none — guard is not red on a real violation")
		}
	})

	t.Run("unsafe qualified SELECT a.* is caught", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "package postgres\n\nconst q = `SELECT a.* FROM widgets a JOIN owners o ON o.id = a.owner_id`\n")
		violations, err := scanDirForUnsafeSelectStars(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(violations) == 0 {
			t.Fatal("expected the guard to flag the unsafe SELECT a.*, found none — guard is not red on a real violation")
		}
	})

	t.Run("safe recursive CTE pattern stays green", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "package postgres\n\n"+
			"const q = `\n"+
			"\tWITH RECURSIVE widget_tree AS (\n"+
			"\t\tSELECT *, 1 AS depth FROM widgets\n"+
			"\t\tWHERE parent_id = $1\n"+
			"\t\tUNION ALL\n"+
			"\t\tSELECT w.*, t.depth + 1 FROM widgets w\n"+
			"\t\tINNER JOIN widget_tree t ON w.parent_id = t.id\n"+
			"\t)\n"+
			"\tSELECT id, parent_id, depth FROM widget_tree ORDER BY depth\n"+
			"`\n")
		violations, err := scanDirForUnsafeSelectStars(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(violations) != 0 {
			t.Fatalf("expected the recursive-CTE fixture to stay green, got violations: %v — guard is not green on the legitimate pattern", violations)
		}
	})

	t.Run("star outside the CTE parens is still caught", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "package postgres\n\n"+
			"const q = `\n"+
			"\tWITH RECURSIVE widget_tree AS (\n"+
			"\t\tSELECT id, parent_id, 1 AS depth FROM widgets\n"+
			"\t\tWHERE parent_id = $1\n"+
			"\t\tUNION ALL\n"+
			"\t\tSELECT w.id, w.parent_id, t.depth + 1 FROM widgets w\n"+
			"\t\tINNER JOIN widget_tree t ON w.parent_id = t.id\n"+
			"\t)\n"+
			"\tSELECT * FROM widget_tree ORDER BY depth\n"+
			"`\n")
		violations, err := scanDirForUnsafeSelectStars(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(violations) == 0 {
			t.Fatal("expected the outer SELECT * (outside the CTE's own parens) to be flagged — the CTE exemption must not blanket-whitelist the whole query")
		}
	})

	t.Run("explicit column list stays green", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "package postgres\n\nconst q = `SELECT id, name FROM widgets WHERE id = $1`\n")
		violations, err := scanDirForUnsafeSelectStars(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(violations) != 0 {
			t.Fatalf("expected the explicit-column fixture to stay green, got violations: %v", violations)
		}
	})
}
