// Command mcp-drift-check compares the internal/mcp package in this repo
// (evc-mesh, served as cmd/mcp -> mesh-vm's mesh-mcp.service, the public SSE
// surface) against the internal/mcp package in evc-mesh-mcp (built into
// ~/bin/mesh-mcp, the stdio binary every agent workspace actually talks to).
//
// The two packages are independent copies of the same tool implementation,
// not one importing the other (Go's internal/ visibility forbids that across
// modules), so a bug fixed in one has zero effect on the other unless someone
// remembers to port it by hand. That already happened once silently (Mesh
// #9855f866 / #4222c17d): a comment-truncation fix merged clean, CI green,
// deployed — and the fleet kept serving the old broken behavior because the
// binary agents run is built from the other repo entirely.
//
// This tool makes that class of drift loud instead of silent: for every
// top-level function/method present in BOTH trees under the same name (and,
// for methods, the same receiver type), it gofmt-normalizes the full
// declaration (signature + body, comments stripped so incidental comment
// wording never trips it) and fails if the two differ. A function that
// legitimately needs to differ between the two surfaces stays out of the
// failure by an explicit, reviewed line in the allowlist file — never by
// silence.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type funcEntry struct {
	file string
	text string
}

// collectFuncs parses every non-test .go file directly in dir (internal/mcp
// is flat in both repos — no recursion needed) and returns one entry per
// top-level func/method, keyed by "Receiver.Name" (methods) or "Name"
// (package-level functions). Comments are dropped by parsing without
// parser.ParseComments, so a comment-only edit never registers as drift.
func collectFuncs(dir string) (map[string]funcEntry, error) {
	entries := map[string]funcEntry{}

	matches, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no .go files found in %s", dir)
	}

	fset := token.NewFileSet()
	for _, path := range matches {
		base := filepath.Base(path)
		if strings.HasSuffix(base, "_test.go") {
			continue
		}

		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}

		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}

			key := fd.Name.Name
			if fd.Recv != nil && len(fd.Recv.List) > 0 {
				key = receiverTypeName(fd.Recv.List[0].Type) + "." + key
			}

			var buf strings.Builder
			if err := format.Node(&buf, fset, fd); err != nil {
				return nil, fmt.Errorf("format %s in %s: %w", key, path, err)
			}

			if prior, dup := entries[key]; dup {
				// Can't happen for a package that itself compiles (Go rejects
				// duplicate declarations), but stay loud rather than silently
				// picking one side if this assumption ever breaks.
				return nil, fmt.Errorf("duplicate func key %q: %s and %s", key, prior.file, base)
			}
			entries[key] = funcEntry{file: base, text: buf.String()}
		}
	}
	return entries, nil
}

// printDiff shells out to the system `diff -u` (present on every CI runner
// and dev machine we use) rather than reimplementing one — this is a local
// triage aid, never parsed by CI.
func printDiff(key, a, b string) {
	dir, err := os.MkdirTemp("", "mcp-drift-check")
	if err != nil {
		fmt.Fprintf(os.Stderr, "  (diff unavailable: %v)\n", err)
		return
	}
	defer os.RemoveAll(dir)
	pathA := filepath.Join(dir, "a.go")
	pathB := filepath.Join(dir, "b.go")
	if err := os.WriteFile(pathA, []byte(a), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "  (diff unavailable: %v)\n", err)
		return
	}
	if err := os.WriteFile(pathB, []byte(b), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "  (diff unavailable: %v)\n", err)
		return
	}
	out, _ := exec.Command("diff", "-u", pathA, pathB).CombinedOutput()
	fmt.Printf("--- %s ---\n%s\n", key, out)
}

func receiverTypeName(e ast.Expr) string {
	if star, ok := e.(*ast.StarExpr); ok {
		e = star.X
	}
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return fmt.Sprintf("%T", e)
}

// loadAllowlist reads "<key>\t<reason>" lines (blank lines and lines
// starting with # ignored). A missing file is not an error — an empty
// allowlist is a legitimate starting state.
func loadAllowlist(path string) (map[string]string, error) {
	allow := map[string]string{}
	if path == "" {
		return allow, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return allow, nil
	}
	if err != nil {
		return nil, err
	}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
			return nil, fmt.Errorf("%s:%d: expected \"<func-key>\\t<reason>\", got %q", path, i+1, line)
		}
		key := strings.TrimSpace(parts[0])
		if _, dup := allow[key]; dup {
			return nil, fmt.Errorf("%s:%d: duplicate allowlist entry for %q", path, i+1, key)
		}
		allow[key] = strings.TrimSpace(parts[1])
	}
	return allow, nil
}

func main() {
	dirA := flag.String("a", "", "path to first internal/mcp directory")
	dirB := flag.String("b", "", "path to second internal/mcp directory")
	labelA := flag.String("label-a", "a", "label for -a in output (e.g. evc-mesh)")
	labelB := flag.String("label-b", "b", "label for -b in output (e.g. evc-mesh-mcp)")
	allowPath := flag.String("allow", "", "path to allowlist file (func-key TAB reason per line)")
	verbose := flag.Bool("v", false, "print a unified diff for each drifted function (local triage; not used in CI)")
	flag.Parse()

	if *dirA == "" || *dirB == "" {
		fmt.Fprintln(os.Stderr, "usage: mcp-drift-check -a <dir> -b <dir> [-label-a NAME] [-label-b NAME] [-allow FILE]")
		os.Exit(2)
	}

	funcsA, err := collectFuncs(*dirA)
	if err != nil {
		fmt.Fprintf(os.Stderr, "::error::reading %s (%s): %v\n", *labelA, *dirA, err)
		os.Exit(1)
	}
	funcsB, err := collectFuncs(*dirB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "::error::reading %s (%s): %v\n", *labelB, *dirB, err)
		os.Exit(1)
	}

	allow, err := loadAllowlist(*allowPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "::error::reading allowlist %s: %v\n", *allowPath, err)
		os.Exit(1)
	}

	var shared []string
	for k := range funcsA {
		if _, ok := funcsB[k]; ok {
			shared = append(shared, k)
		}
	}
	sort.Strings(shared)

	var staleAllow []string
	for k := range allow {
		if _, ok := funcsA[k]; !ok {
			staleAllow = append(staleAllow, k)
		} else if _, ok := funcsB[k]; !ok {
			staleAllow = append(staleAllow, k)
		}
	}
	sort.Strings(staleAllow)

	var drifted []string
	for _, k := range shared {
		if _, allowed := allow[k]; allowed {
			continue
		}
		if funcsA[k].text != funcsB[k].text {
			drifted = append(drifted, k)
		}
	}

	fmt.Printf("mcp-drift-check: %d functions shared between %s and %s, %d allowlisted, %d compared\n",
		len(shared), *labelA, *labelB, len(allow)-len(staleAllow), len(shared)-len(allow)+len(staleAllow))

	fail := false

	if len(staleAllow) > 0 {
		fail = true
		fmt.Fprintf(os.Stderr, "::error::allowlist %s names %d func-key(s) that no longer exist on both sides — remove them, they can't be masking anything: %s\n",
			*allowPath, len(staleAllow), strings.Join(staleAllow, ", "))
	}

	if len(drifted) > 0 {
		fail = true
		fmt.Fprintf(os.Stderr, "::error::DRIFT DETECTED — %d function(s) differ between %s and %s:\n", len(drifted), *labelA, *labelB)
		for _, k := range drifted {
			fmt.Fprintf(os.Stderr, "::error::  %s  (%s:%s vs %s:%s)\n", k, *labelA, funcsA[k].file, *labelB, funcsB[k].file)
			if *verbose {
				printDiff(k, funcsA[k].text, funcsB[k].text)
			}
		}
		fmt.Fprintln(os.Stderr, "::error::evc-mesh/internal/mcp (-> mesh-vm's mesh-mcp.service, the public SSE surface) and evc-mesh-mcp/internal/mcp (-> ~/bin/mesh-mcp, the stdio binary every agent workspace runs) are independent copies of the same tool implementation — neither imports the other. A fix landing in only one has zero effect on the other. Port the fix to both repos, or if the difference is intentional, add \"<func-key>\\t<reason>\" to the allowlist in a reviewed PR.")
	}

	if fail {
		os.Exit(1)
	}
	fmt.Println("No drift detected.")
}
