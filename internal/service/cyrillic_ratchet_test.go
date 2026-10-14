package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Server-written text is English (i18n L3.2): a Cyrillic string literal in
// internal/service or internal/handler is either a recognition dictionary
// (the server still has to read comments written in Russian) or a regression.
// The test parses every non-test file and looks at string literals only, so
// comments may stay in any language. The allowlist below names the top-level
// declaration that may hold Cyrillic literals; anything else fails.
//
// To add a dictionary, add its "<file>:<declaration>" here with the reason.
// Do not allowlist a function that writes text a user or agent reads.
var cyrillicLiteralAllowlist = map[string]string{
	// Recognition dictionaries and regexps: they match Russian text people write.
	"comment_service.go:autoMarkerSubstrings":                      "triage-move recognition dictionary",
	"comment_service.go:triageExitNegators":                        "withdrawal dictionary",
	"comment_service.go:negationTokens":                            "completion-report dictionary",
	"comment_service.go:rawArmMarkerSubstring":                     "reads raw-arm comments written before system_code",
	"comment_service.go:legacyEndpointGateHintMarker":              "reads hint comments written before system_code",
	"comment_mention_handoff_gate.go:askImperativeWords":           "handoff verb dictionary",
	"comment_service_human_gate_arming.go:gateDeadlineRegex":       "marker regexp",
	"comment_service_human_gate_arming.go:recommendedDefaultRegex": "marker regexp",
	"secret_rotation_gate.go:secretRotationNoiseTokens":            "secret-rotation dictionary",
	"comment_closed_task_followup.go:driverCommentPrefixes":        "service-comment recognition",
	"human_gate_soft_timeout.go:legacyReleaseSoftTimeoutMark":      "reads release comments written before system_code",
	"task_service.go:legacyRepeatCommentPrefix":                    "reads repeat comments written before system_code",
	"comment_service.go:completionKeywords":                        "completion-report dictionary",
	"secret_rotation_gate.go:secretRotationVerbRe":                 "secret-rotation dictionary",
	"secret_rotation_gate.go:secretRotationNounRe":                 "secret-rotation dictionary",
	"comment_service.go:askWords":                                  "withdrawal dictionary",
	"comment_service.go:blockerStillOpenMarkers":                   "withdrawal dictionary",
	"comment_service.go:explicitNonWithdrawalMarkers":              "withdrawal dictionary",
	"comment_service.go:repeatPingWords":                           "withdrawal dictionary",
	// Two handler messages still Russian; they belong to the L3.3 error-code work (card 3db5f597, MR !1206).
	"task_handler.go:handleError":      "mention-handoff error text (L3.3)",
	"task_handler.go:gateClearRefusal": "lists the Russian withdrawal words the marker accepts (L3.3)",
	// Error hints of the task handler belong to L3.3.
}

var cyrillicRe = regexp.MustCompile(`[А-Яа-яЁё]`)

func TestNoCyrillicStringLiteralsOutsideDictionaries(t *testing.T) {
	var offenders []string
	for _, dir := range []string{".", "../handler"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			base := filepath.Base(path)
			if _, ok := cyrillicLiteralAllowlist[base+":*"]; ok {
				continue
			}
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			found, err := cyrillicOffenders(path, src)
			if err != nil {
				t.Fatal(err)
			}
			offenders = append(offenders, found...)
		}
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Fatalf("Cyrillic string literals outside the allowlist (translate, or add a recognition dictionary to cyrillicLiteralAllowlist):\n%s",
			strings.Join(offenders, "\n"))
	}
}

// cyrillicOffenders returns "<position> in <unit>" for every Cyrillic string
// literal in src whose unit is not allowlisted for this file.
func cyrillicOffenders(path string, src []byte) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		return nil, err
	}
	base := filepath.Base(path)
	var offenders []string
	for _, node := range scanUnits(f) {
		name := unitName(node)
		if _, ok := cyrillicLiteralAllowlist[base+":"+name]; ok {
			continue
		}
		ast.Inspect(node, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && literalHasCyrillic(lit.Value) {
				offenders = append(offenders, fset.Position(lit.Pos()).String()+" in "+name)
			}
			return true
		})
	}
	return offenders, nil
}

// literalHasCyrillic looks at the decoded string, so "\u041f" (an escape that
// evaluates to a Cyrillic letter) is caught the same as the letter itself.
func literalHasCyrillic(src string) bool {
	if v, err := strconv.Unquote(src); err == nil {
		return cyrillicRe.MatchString(v)
	}
	return cyrillicRe.MatchString(src)
}

// The ratchet only means something if it can fail: a Cyrillic literal in a new
// file or in a function outside the allowlist must be reported, while the same
// text in a comment, in an English literal, or in an allowlisted dictionary
// must not.
func TestCyrillicRatchetScannerCatchesALiteral(t *testing.T) {
	const bad = "package x\nfunc writeNote() string { return \"\u041f\u0440\u0438\u0432\u0435\u0442\" }\n"
	got, err := cyrillicOffenders("zz_red_control.go", []byte(bad))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.HasSuffix(got[0], " in writeNote") {
		t.Fatalf("a Cyrillic literal in a new file was not reported exactly once: %v", got)
	}

	const clean = "package x\n// \u041f\u0440\u0438\u0432\u0435\u0442 in a comment\nfunc writeNote() string { return \"hello\" }\n"
	got, err = cyrillicOffenders("zz_red_control.go", []byte(clean))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a comment or an English literal was reported: %v", got)
	}

	// The same word spelled with escapes evaluates to Cyrillic text and must not slip through.
	const escaped = "package x\nfunc writeNote() string { return \"\\u041f\\u0440\" }\nfunc rawNote() string { return `\\u041f` }\n"
	got, err = cyrillicOffenders("zz_red_control.go", []byte(escaped))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.HasSuffix(got[0], " in writeNote") {
		t.Fatalf("an escaped Cyrillic literal was missed, or a raw backslash-u string was reported: %v", got)
	}

	// An allowlisted name in a multi-name declaration does not shelter its sibling.
	const sibling = "package x\nvar rawArmMarkerSubstring, other = \"\u0432\u0437\u0432\u0435\u0434\u0451\u043d\", \"\u0432\u0437\u0432\u0435\u0434\u0451\u043d\"\n"
	got, err = cyrillicOffenders("comment_service.go", []byte(sibling))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.HasSuffix(got[0], " in other") {
		t.Fatalf("a sibling name in an allowlisted declaration was sheltered: %v", got)
	}

	// An allowlisted unit stays quiet; the same literal one unit over does not.
	const dict = "package x\nvar rawArmMarkerSubstring = \"\u0432\u0437\u0432\u0435\u0434\u0451\u043d\"\nvar other = \"\u0432\u0437\u0432\u0435\u0434\u0451\u043d\"\n"
	got, err = cyrillicOffenders("comment_service.go", []byte(dict))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.HasSuffix(got[0], " in other") {
		t.Fatalf("allowlist did not isolate the dictionary unit: %v", got)
	}
}

// scanUnits splits the file into the units the allowlist keys on: each
// function, and each spec of a var/const/type block (so one name in a grouped
// block does not shelter the others).
func scanUnits(f *ast.File) []ast.Node {
	var units []ast.Node
	for _, d := range f.Decls {
		if g, ok := d.(*ast.GenDecl); ok {
			for _, s := range g.Specs {
				units = append(units, splitValueSpec(s)...)
			}
			continue
		}
		units = append(units, d)
	}
	return units
}

// splitValueSpec gives each name of `var a, b = x, y` its own unit, so an
// allowlist entry for one name cannot shelter a literal assigned to another.
// A spec whose values do not pair up with its names (`var a, b = f()`) stays
// whole and is keyed by all of its names, which no allowlist entry spells.
func splitValueSpec(s ast.Spec) []ast.Node {
	vs, ok := s.(*ast.ValueSpec)
	if !ok || len(vs.Names) < 2 {
		return []ast.Node{s}
	}
	if len(vs.Values) != len(vs.Names) {
		return []ast.Node{s}
	}
	units := make([]ast.Node, len(vs.Names))
	for i := range vs.Names {
		units[i] = &ast.ValueSpec{Names: vs.Names[i : i+1], Type: vs.Type, Values: vs.Values[i : i+1]}
	}
	return units
}

func unitName(n ast.Node) string {
	switch n := n.(type) {
	case *ast.FuncDecl:
		return n.Name.Name
	case *ast.ValueSpec:
		names := make([]string, len(n.Names))
		for i, id := range n.Names {
			names[i] = id.Name
		}
		return strings.Join(names, ",")
	case *ast.TypeSpec:
		return n.Name.Name
	}
	return "?"
}
