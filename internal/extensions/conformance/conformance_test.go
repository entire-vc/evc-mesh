// Package conformance_test checks that the extension contract executes what it
// promises: every UI slot, API right and capability field that
// internal/extensions/schema/manifest.v1.json lets a manifest declare, and every
// entry point of the Provider contract, has a consumer — a handler in web/src or
// a route/call on the server. Elements known to have none are listed, one per
// line with the reason, in known_gaps.txt; that list may only shrink: a line
// whose element has gained a consumer, or that names nothing, fails the test too.
// The theme SDK copy of the schema must equal the server's.
package conformance_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/middleware"
)

const (
	serverSchema = "internal/extensions/schema/manifest.v1.json"
	sdkSchema    = "packages/mesh-theme-sdk/schema/manifest.v1.json"
	providerFile = "internal/extensions/provider.go"
	gapsFile     = "known_gaps.txt"
)

// Element kinds, as written in the first column of known_gaps.txt.
const (
	kindSlot       = "slot"       // $defs.slot: capabilities.ui and external.ui of a manifest
	kindThemeSlot  = "theme-slot" // $defs.themeSlot enum: theme.slots of a theme manifest
	kindAPI        = "api"        // capabilities.api values
	kindCapability = "capability" // fields of capabilities
	kindContract   = "contract"   // Provider methods and Hooks fields of provider.go
)

// enumeratorFiles list every capability group only to flatten, display or diff
// the consent; listing a group there is not consuming it.
var enumeratorFiles = map[string]bool{
	"internal/extensions/repoinstall/validate.go": true,
	"internal/extensions/catalog/catalog.go":      true,
	"internal/extensions/catalog/updates.go":      true,
	"internal/extensions/status.go":               true,
}

type element struct{ kind, name string }

func (e element) String() string { return e.kind + " " + strconv.Quote(e.name) }

type gap struct {
	element
	reason string
	line   int
}

// consumers is what the code base actually reads.
type consumers struct {
	slotUses     map[string]bool // literal arguments of slot lookups and UI-context requests
	themeUses    map[string]bool // theme host lookups, including dynamic families
	webCode      []string        // non-test web/src code with strings and comments removed
	apiRights    map[string]bool // rights a mapped extension route or HasAPI call checks
	capFields    map[string]bool // capabilities fields (json names) read on the server
	contractUsed map[string]bool // contract element name → called/read
}

func (c *consumers) consumed(e element) bool {
	switch e.kind {
	case kindSlot:
		// Declarations, allowlists and contribution maps do not host a slot.
		// The UI must request contributors or an iframe context for it.
		return c.slotUses[e.name]
	case kindThemeSlot:
		return c.themeUses[e.name]
	case kindAPI:
		return c.apiRights[e.name]
	case kindCapability:
		if c.capFields[e.name] {
			return true
		}
		re := regexp.MustCompile(`capabilities\??\.` + regexp.QuoteMeta(e.name) + `\b`)
		for _, code := range c.webCode {
			if re.MatchString(code) {
				return true
			}
		}
		return false
	case kindContract:
		return c.contractUsed[e.name]
	}
	return false
}

func TestConformance(t *testing.T) {
	root := repoRoot(t)
	schema := readFile(t, root, serverSchema)

	elems, err := schemaElements(schema)
	if err != nil {
		t.Fatal(err)
	}
	contract, err := contractElements(filepath.Join(root, providerFile))
	if err != nil {
		t.Fatal(err)
	}
	elems = append(elems, contract...)
	if err = checkCapabilityFieldsMapped(elems); err != nil {
		t.Fatal(err)
	}

	cons := collectConsumers(t, root, contract)
	gaps, err := parseGaps(gapsFile)
	if err != nil {
		t.Fatal(err)
	}
	retired := deprecatedElements(t, schema)
	active := make([]element, 0, len(elems))
	for _, e := range elems {
		if !slices.Contains(retired, e) {
			active = append(active, e)
		}
	}
	for _, f := range audit(active, cons, gaps, gapsFile) {
		t.Error(f)
	}
	if t.Failed() {
		t.Logf("every element above needs a consumer, or a line in %s (\"<kind> <name> — <reason>\"); a stale line is deleted", gapsFile)
	}

	for _, d := range diffJSON(schema, readFile(t, root, sdkSchema)) {
		t.Errorf("%s differs from %s: %s", sdkSchema, serverSchema, d)
	}
}

// TestConformanceRedOnInventedSlot: a manifest schema that offers a slot nobody
// renders makes the audit name it; the slots that do have a consumer stay quiet
// (positive control).
func TestConformanceRedOnInventedSlot(t *testing.T) {
	root := repoRoot(t)
	elems, err := schemaElements(readFile(t, "", "testdata/invented_slot.manifest.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	cons := collectConsumers(t, root, nil)
	findings := audit(elems, cons, nil, "none")
	joined := strings.Join(findings, "\n")
	if !strings.Contains(joined, `slot "hologram.panel"`) {
		t.Fatalf("invented slot not named; findings:\n%s", joined)
	}
	if strings.Contains(joined, `"task.panel"`) || strings.Contains(joined, `"tasks:read"`) {
		t.Fatalf("a consumed element was reported; findings:\n%s", joined)
	}
}

// TestConformanceRedOnStaleGap: a gap line for an element that has a consumer,
// or for an element the schema does not declare, is named by file and line.
func TestConformanceRedOnStaleGap(t *testing.T) {
	root := repoRoot(t)
	elems, err := schemaElements(readFile(t, root, serverSchema))
	if err != nil {
		t.Fatal(err)
	}
	const fixture = "testdata/stale_gaps.txt"
	gaps, err := parseGaps(fixture)
	if err != nil {
		t.Fatal(err)
	}
	cons := collectConsumers(t, root, nil)
	findings := audit(elems, cons, gaps, fixture)
	joined := strings.Join(findings, "\n")
	for _, want := range []string{
		fixture + `:3: slot "task.panel" has a consumer now`,
		fixture + `:4: api "tasks:read" has a consumer now`,
		fixture + `:5: slot "no.such.slot" is not declared`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing finding %q; findings:\n%s", want, joined)
		}
	}
}

// TestConformanceRedOnSchemaDrift: the SDK copy differing from the server
// schema is named by its JSON path.
func TestConformanceRedOnSchemaDrift(t *testing.T) {
	root := repoRoot(t)
	server := readFile(t, root, serverSchema)
	drifted := bytes.Replace(server, []byte(`"doc.panel", "project.view"`), []byte(`"project.view"`), 1)
	if bytes.Equal(drifted, server) {
		t.Fatal("fixture edit did not apply: the slot enum changed shape")
	}
	diffs := diffJSON(server, drifted)
	if len(diffs) == 0 || !strings.Contains(strings.Join(diffs, "\n"), "$defs.slot.enum") {
		t.Fatalf("drift not named; diffs: %v", diffs)
	}
	if d := diffJSON(server, server); len(d) != 0 {
		t.Fatalf("identical schemas reported as different: %v", d)
	}
}

// TestConformanceSlotScan pins what counts as a slot consumer: a JSON sample in
// a template literal yields no string, and slots count only at host lookups.
func TestConformanceSlotScan(t *testing.T) {
	strs, heads, _ := scanTS("const a = \"task.panel\";\n" +
		"const sample = `{\n  \"command\": \"mesh-mcp\",\n  \"url\": \"${origin}\"\n}`;\n" +
		"const slot = `dashboard.widget.${id}`; // \"doc.panel\"\n")
	if !slices.Equal(strs, []string{"task.panel"}) {
		t.Fatalf("string literals = %q, want only task.panel (template and comment text is not code)", strs)
	}
	if !slices.Contains(heads, "dashboard.widget.") {
		t.Fatalf("template heads = %q, want dashboard.widget.", heads)
	}

	c := &consumers{
		slotUses:  map[string]bool{"task.panel": true},
		themeUses: map[string]bool{"board.card": true, "dashboard.widget.*": true},
	}
	for _, tc := range []struct {
		e    element
		want bool
	}{
		{element{kindSlot, "command"}, false}, // a literal without a slot lookup
		{element{kindSlot, "task.panel"}, true},
		{element{kindThemeSlot, "board.card"}, true},
		{element{kindThemeSlot, "dashboard.widget.*"}, true},
		{element{kindThemeSlot, "nav.widget.*"}, false},
	} {
		if got := c.consumed(tc.e); got != tc.want {
			t.Errorf("consumed(%s) = %v, want %v", tc.e, got, tc.want)
		}
	}

	elems, err := schemaElements(readFile(t, repoRoot(t), serverSchema))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(elems, element{kindThemeSlot, "dashboard.widget.*"}) {
		t.Fatal("the $defs.themeSlot pattern family is not audited")
	}
	if _, err := literalPrefix(`^[a-z]+$`); err == nil {
		t.Fatal("a pattern without a literal prefix must be refused, not audited as matching everything")
	}
}

func TestConformanceRejectsUnrelatedGoMethods(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"internal/extensions", "cmd"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"go.mod": "module example.com/conformance\n\ngo 1.23\n",
		"internal/extensions/provider.go": `package extensions
type Hooks struct { Routes, EventHandlers []string }
type Provider interface { UI() string; Manifest() string; Hooks() Hooks }
type Registry struct { p Provider }
func (r Registry) EventHandlers() []string { return r.p.Hooks().EventHandlers }
func (r Registry) Routes() []string { return r.p.Hooks().Routes }
func Use(p Provider, r Registry) { _ = p.Manifest(); _ = r.Routes() }
type OtherHooks struct { EventHandlers []string }
type Other struct{}
func (Other) UI() string { return "" }
func (Other) Hooks() OtherHooks { return OtherHooks{} }
func (Other) EventHandlers() []string { return nil }
func Unrelated(o Other) { _ = o.UI(); _ = o.Hooks().EventHandlers; _ = o.EventHandlers() }
`,
	}
	for path, src := range files {
		if err := os.WriteFile(filepath.Join(root, path), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := &consumers{apiRights: map[string]bool{}, capFields: map[string]bool{}, contractUsed: map[string]bool{}}
	elems := []element{
		{kindContract, "Provider.UI()"}, {kindContract, "Provider.Manifest()"},
		{kindContract, "Hooks.EventHandlers"}, {kindContract, "Hooks.Routes"},
	}
	collectGo(t, root, c, elems)
	for _, e := range elems {
		want := e.name == "Provider.Manifest()" || e.name == "Hooks.Routes"
		if got := c.consumed(e); got != want {
			t.Errorf("consumed(%s) = %v, want %v: unrelated receiver methods must not count", e, got, want)
		}
	}
}

func TestConformanceRejectsSlotDeclarations(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "web", "src", "lib")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := `type SlotId = "hologram.panel" | "task.panel";
const SLOT_IDS = ["hologram.panel", "task.panel"];
const NONE = { "hologram.panel": [] };
const declaration = { slot: "hologram.panel" };
const themeDeclaration = { "ghost.view": Component };
const familyDeclaration = ` + "`ghost.${id}`" + `;
// useSlotExtensions("hologram.panel")
const sample = ` + "`useSlotExtensions(\"hologram.panel\")`" + `;
const quoted = "useSlotExtensions('hologram.panel')";
const regex = /useSlotExtensions("hologram.panel")/;
const themeRegex = /resolveSlotView("ghost.view")/;
const classRegex = /[/]useSlotExtensions("hologram.panel")/g;
const escapedRegex = /\/useSlotExtensions("hologram.panel")/;
const arrowRegex = () => /useSlotExtensions("hologram.panel")/;
function pattern() { return /useSlotExtensions("hologram.panel")/; }
if (enabled) /useSlotExtensions("hologram.panel")/.test(value);
const jsxRegex = /<Slot name="ghost.view">/;
api(` + "`/api/v1/ext/${id}/other`" + `, {method: "POST", body: {slot: "hologram.panel"}});
api(` + "`/api/v1/other/${id}/ui-context`" + `, {method: "POST", body: {slot: "hologram.panel"}});
api(` + "`/api/v1/ext/${id}/ui-context`" + `, {method: "GET", body: {slot: "hologram.panel"}});
const panels = useSlotExtensions("task.panel");
const ratio = total++ / useSlotExtensions("task.actions").length;
const card = resolveSlotView("board.card");
const override = resolveSlotOverride("nav.sidebar", CoreSidebar);
const auth = <Slot name="auth.screen" fallback={CoreAuth} />;
const widget = useDashboardSlotContext(` + "`dashboard.widget.${id}`" + `);
api<UiContext>(` + "`/api/v1/ext/${id}/ui-context`" + `, {
  method: "POST", body: { workspace_id: wsId, slot: "dashboard.widget" },
});
`
	if err := os.WriteFile(filepath.Join(dir, "extension-slots.ts"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &consumers{slotUses: map[string]bool{}, themeUses: map[string]bool{}}
	collectWeb(t, root, c)
	elems := []element{
		{kindSlot, "hologram.panel"}, {kindThemeSlot, "ghost.view"}, {kindThemeSlot, "ghost.*"},
		{kindSlot, "task.panel"}, {kindSlot, "dashboard.widget"},
		{kindSlot, "task.actions"},
		{kindThemeSlot, "board.card"}, {kindThemeSlot, "dashboard.widget.*"},
		{kindThemeSlot, "nav.sidebar"}, {kindThemeSlot, "auth.screen"},
	}
	findings := strings.Join(audit(elems, c, nil, "none"), "\n")
	for _, name := range []string{"hologram.panel", "ghost.view", "ghost.*"} {
		if !strings.Contains(findings, strconv.Quote(name)) {
			t.Errorf("a declaration without a lookup must fail for %q; findings: %s", name, findings)
		}
	}
	for _, name := range []string{"task.panel", "task.actions", "dashboard.widget", "board.card", "dashboard.widget.*", "nav.sidebar", "auth.screen"} {
		if strings.Contains(findings, strconv.Quote(name)) {
			t.Errorf("real slot lookup %q must pass the audit; findings: %s", name, findings)
		}
	}
	t.Logf("declarations rejected:\n%s", findings)
}

// audit returns one finding per unconsumed element without a gap line and per
// gap line that is stale, duplicated or names nothing.
func audit(elems []element, cons *consumers, gaps []gap, gapsPath string) []string {
	declared := make(map[element]bool, len(elems))
	for _, e := range elems {
		declared[e] = true
	}
	listed := map[element]int{}
	var findings []string
	for _, g := range gaps {
		where := fmt.Sprintf("%s:%d: ", gapsPath, g.line)
		switch {
		case listed[g.element] != 0:
			findings = append(findings, where+g.String()+fmt.Sprintf(" is listed already on line %d", listed[g.element]))
		case !declared[g.element]:
			findings = append(findings, where+g.String()+" is not declared by the schema or provider.go: delete the line")
		case cons.consumed(g.element):
			findings = append(findings, where+g.String()+" has a consumer now: delete the line")
		}
		if listed[g.element] == 0 {
			listed[g.element] = g.line
		}
	}
	for _, e := range elems {
		if listed[e] == 0 && !cons.consumed(e) {
			findings = append(findings, e.String()+" has no consumer: "+hint(e))
		}
	}
	return findings
}

func hint(e element) string {
	switch e.kind {
	case kindSlot, kindThemeSlot:
		return "no web/src code renders it"
	case kindAPI:
		return "no extension route (middleware.extensionRouteCapabilities) or HasAPI call checks it"
	case kindCapability:
		return "no server code or web/src code reads capabilities." + e.name
	case kindContract:
		return "nothing outside its own declaration calls or reads it"
	}
	return ""
}

// schemaElements lists the slots, theme slots, API rights and capability
// fields a manifest may declare.
func schemaElements(raw []byte) ([]element, error) {
	var doc struct {
		Properties struct {
			Capabilities struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"capabilities"`
		} `json:"properties"`
		Defs struct {
			Slot struct {
				Enum []string `json:"enum"`
			} `json:"slot"`
			ThemeSlot struct {
				AnyOf []struct {
					Enum    []string `json:"enum"`
					Pattern string   `json:"pattern"`
				} `json:"anyOf"`
			} `json:"themeSlot"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}
	caps := doc.Properties.Capabilities.Properties
	if len(doc.Defs.Slot.Enum) == 0 || len(caps) == 0 {
		return nil, fmt.Errorf("schema has no $defs.slot enum or capabilities properties: the test reads the wrong shape")
	}
	var out []element
	for _, s := range doc.Defs.Slot.Enum {
		out = append(out, element{kindSlot, s})
	}
	for _, a := range doc.Defs.ThemeSlot.AnyOf {
		for _, s := range a.Enum {
			out = append(out, element{kindThemeSlot, s})
		}
		if a.Pattern != "" {
			prefix, err := literalPrefix(a.Pattern)
			if err != nil {
				return nil, fmt.Errorf("$defs.themeSlot pattern: %w", err)
			}
			out = append(out, element{kindThemeSlot, prefix + "*"})
		}
	}
	fields := make([]string, 0, len(caps))
	for f := range caps {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	for _, f := range fields {
		out = append(out, element{kindCapability, f})
	}

	var api struct {
		Items struct {
			Pattern string `json:"pattern"`
		} `json:"items"`
	}
	if err := json.Unmarshal(caps["api"], &api); err != nil || api.Items.Pattern == "" {
		return nil, fmt.Errorf("capabilities.api.items.pattern missing")
	}
	rights, err := expandAPIPattern(api.Items.Pattern)
	if err != nil {
		return nil, err
	}
	for _, r := range rights {
		out = append(out, element{kindAPI, r})
	}
	return out, nil
}

// literalPrefix returns the fixed text a ^-anchored pattern starts with:
// `^dashboard\.widget\.[a-z]…$` → "dashboard.widget.".
func literalPrefix(pattern string) (string, error) {
	rest, ok := strings.CutPrefix(pattern, "^")
	if !ok {
		return "", fmt.Errorf("%q is not ^-anchored", pattern)
	}
	var b strings.Builder
	for i := 0; i < len(rest); i++ {
		ch := rest[i]
		if ch == '\\' && i+1 < len(rest) && strings.ContainsRune(`.-/`, rune(rest[i+1])) {
			b.WriteByte(rest[i+1])
			i++
			continue
		}
		if strings.ContainsRune(`\\[](){}*+?|^$.`, rune(ch)) {
			break
		}
		b.WriteByte(ch)
	}
	if b.Len() == 0 {
		return "", fmt.Errorf("%q has no literal prefix to look for", pattern)
	}
	return b.String(), nil
}

var (
	productAlt = regexp.MustCompile(`^\(([a-z_|]+)\):\(([a-z_|]+)\)$`)
	literalAlt = regexp.MustCompile(`^[a-z_]+:[a-z_]+$`)
)

// expandAPIPattern turns `^((a|b):(read|write)|c:rotate)$` into the finite
// list of rights it accepts, and checks each against the pattern itself.
func expandAPIPattern(pattern string) ([]string, error) {
	body, ok := strings.CutPrefix(pattern, "^(")
	if ok {
		body, ok = strings.CutSuffix(body, ")$")
	}
	if !ok {
		return nil, fmt.Errorf("capabilities.api pattern %q is not ^(…)$: teach expandAPIPattern the new shape", pattern)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, alt := range splitTopLevel(body) {
		if m := productAlt.FindStringSubmatch(alt); m != nil {
			for _, l := range strings.Split(m[1], "|") {
				for _, r := range strings.Split(m[2], "|") {
					out = append(out, l+":"+r)
				}
			}
			continue
		}
		if literalAlt.MatchString(alt) {
			out = append(out, alt)
			continue
		}
		return nil, fmt.Errorf("capabilities.api alternative %q is neither (a|b):(c|d) nor a literal: teach expandAPIPattern the new shape", alt)
	}
	for _, r := range out {
		if !re.MatchString(r) {
			return nil, fmt.Errorf("expanded right %q does not match %q", r, pattern)
		}
	}
	return out, nil
}

func splitTopLevel(s string) []string {
	var out []string
	depth, start := 0, 0
	for i, c := range s {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
		case '|':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// contractElements lists the Provider interface methods and the Hooks struct
// fields of provider.go: the entry points the core promises to call.
func contractElements(path string) ([]element, error) {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return nil, err
	}
	var out []element
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		switch typ := ts.Type.(type) {
		case *ast.InterfaceType:
			if ts.Name.Name == "Provider" {
				for _, m := range typ.Methods.List {
					for _, name := range m.Names {
						out = append(out, element{kindContract, "Provider." + name.Name + "()"})
					}
				}
			}
		case *ast.StructType:
			if ts.Name.Name == "Hooks" {
				for _, fld := range typ.Fields.List {
					for _, name := range fld.Names {
						out = append(out, element{kindContract, "Hooks." + name.Name})
					}
				}
			}
		}
		return false
	})
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no Provider interface or Hooks struct found", path)
	}
	return out, nil
}

// checkCapabilityFieldsMapped: every capabilities field of the schema has a Go
// field in extensions.Capabilities, otherwise the server cannot even read it.
func checkCapabilityFieldsMapped(elems []element) error {
	have := goCapabilityFields()
	var missing []string
	for _, e := range elems {
		if e.kind == kindCapability {
			if _, ok := have[e.name]; !ok {
				missing = append(missing, e.name)
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("capabilities fields %v have no field in extensions.Capabilities", missing)
	}
	return nil
}

// goCapabilityFields maps the json name of each extensions.Capabilities field
// to its Go name.
func goCapabilityFields() map[string]string {
	out := map[string]string{}
	t := reflect.TypeOf(extensions.Capabilities{})
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		out[name] = f.Name
	}
	return out
}

func collectConsumers(t *testing.T, root string, contract []element) *consumers {
	t.Helper()
	c := &consumers{
		slotUses:     map[string]bool{},
		themeUses:    map[string]bool{},
		apiRights:    map[string]bool{},
		capFields:    map[string]bool{},
		contractUsed: map[string]bool{},
	}
	collectWeb(t, root, c)
	for _, right := range middleware.ExtensionRouteCapabilities() {
		c.apiRights[right] = true
	}
	collectGo(t, root, c, contract)
	return c
}

func collectWeb(t *testing.T, root string, c *consumers) {
	t.Helper()
	dir := filepath.Join(root, "web", "src")
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, dir+string(filepath.Separator)))
		if d.IsDir() {
			switch d.Name() {
			case "__tests__", "test-utils", "test", "mocks", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if (!strings.HasSuffix(rel, ".ts") && !strings.HasSuffix(rel, ".tsx")) ||
			strings.Contains(rel, ".test.") || strings.Contains(rel, ".spec.") || strings.HasSuffix(rel, ".d.ts") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, _, code, slots, themes := scanTSConsumers(string(src))
		for _, slot := range slots {
			c.slotUses[slot] = true
		}
		for _, theme := range themes {
			c.themeUses[theme] = true
		}
		c.webCode = append(c.webCode, code)
		return nil
	})
	if err != nil {
		t.Fatalf("scan web/src: %v", err)
	}
	if len(c.webCode) == 0 || len(c.slotUses) == 0 {
		t.Fatal("web/src yielded no code or slot lookups: the scan reads the wrong directory or all slot hosts are gone")
	}
}

// scanTS returns the '…' and "…" string literals of TypeScript source, the
// static head of each template literal (the text before its first ${…}) and
// the code with comments, strings, regex and template literals masked. The rest of a
// template literal is prose or a code sample, not a slot lookup.
func scanTS(src string) (strs, heads []string, code string) {
	strs, heads, code, _, _ = scanTSConsumers(src)
	return
}

func scanTSConsumers(src string) (strs, heads []string, code string, slots, themes []string) {
	var b strings.Builder
	var templates []string
	wantExpr := true // a slash here starts a regex; after a value it is division
	var controlParens []bool
	lastWord := ""
	var lastByte byte
	n := len(src)
	for i := 0; i < n; i++ {
		ch := src[i]
		switch {
		case ch == '/' && i+1 < n && src[i+1] == '/':
			for i < n && src[i] != '\n' {
				i++
			}
			b.WriteByte('\n')
		case ch == '/' && i+1 < n && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return strs, heads, b.String(), nil, nil
			}
			i += end + 3
			b.WriteByte(' ')
		case ch == '/' && wantExpr && lastByte != '<' && i+1 < n && src[i+1] != '>':
			if end := regexLiteralEnd(src, i); end >= 0 {
				b.WriteString("@r@")
				i = end
				wantExpr = false
			} else {
				b.WriteByte(ch)
				wantExpr = true
			}
			lastWord, lastByte = "", '/'
		case ch == '"' || ch == '\'':
			var lit strings.Builder
			j := i + 1
			for ; j < n && src[j] != ch && src[j] != '\n'; j++ {
				if src[j] == '\\' && j+1 < n {
					j++
				}
				lit.WriteByte(src[j])
			}
			strs = append(strs, lit.String())
			i = j
			// Markers preserve literal positions without making prose inside a
			// string look like executable calls. '@s0@' is not a TS identifier.
			fmt.Fprintf(&b, "@s%d@", len(strs)-1)
			wantExpr, lastWord, lastByte = false, "", ch
		case ch == '`':
			depth := 0
			j := i + 1
			headEnd := -1
			for ; j < n; j++ {
				if src[j] == '\\' {
					j++
					continue
				}
				if depth == 0 && src[j] == '`' {
					break
				}
				if src[j] == '$' && j+1 < n && src[j+1] == '{' {
					if depth == 0 && headEnd < 0 {
						headEnd = j
					}
					depth++
				} else if src[j] == '}' && depth > 0 {
					depth--
				}
			}
			if headEnd < 0 {
				headEnd = min(j, n)
			}
			heads = append(heads, src[i+1:headEnd])
			templates = append(templates, src[i+1:min(j, n)])
			i = j
			fmt.Fprintf(&b, "@t%d@", len(templates)-1)
			wantExpr, lastWord, lastByte = false, "", ch
		case jsIdentifierByte(ch) && (ch < '0' || ch > '9'):
			j := i + 1
			for j < n && jsIdentifierByte(src[j]) {
				j++
			}
			lastWord = src[i:j]
			b.WriteString(lastWord)
			i = j - 1
			switch lastWord {
			case "return", "throw", "case", "delete", "void", "typeof", "instanceof", "in", "of", "yield", "await", "else", "do", "break", "continue", "debugger":
				wantExpr = true
			default:
				wantExpr = false
			}
			lastByte = src[i]
		case ch == '(':
			control := lastWord == "if" || lastWord == "while" || lastWord == "for" || lastWord == "with" || lastWord == "switch" || lastWord == "catch"
			controlParens = append(controlParens, control)
			b.WriteByte(ch)
			wantExpr, lastWord, lastByte = true, "", ch
		case ch == ')':
			wantExpr = false
			if len(controlParens) > 0 {
				wantExpr = controlParens[len(controlParens)-1]
				controlParens = controlParens[:len(controlParens)-1]
			}
			b.WriteByte(ch)
			lastWord, lastByte = "", ch
		default:
			b.WriteByte(ch)
			if !strings.ContainsRune(" \t\r\n", rune(ch)) {
				if (ch == '+' || ch == '-') && i+1 < n && src[i+1] == ch {
					// Prefix ++ wants a value; postfix ++ ends one.
					b.WriteByte(ch)
					i++
				} else {
					wantExpr = ch != ']' && ch != '.' && (ch < '0' || ch > '9')
				}
				lastWord, lastByte = "", ch
			}
		}
	}
	code = b.String()
	slots, themes = slotLookups(code, strs, templates)
	return
}

func jsIdentifierByte(ch byte) bool {
	return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '$' || ch >= 128
}

// Regex literals cannot cross a line. Escapes and character classes protect
// embedded slashes, so /[/]/ and /\// both end at their final slash.
func regexLiteralEnd(src string, start int) int {
	inClass := false
	for i := start + 1; i < len(src); i++ {
		switch src[i] {
		case '\n', '\r':
			return -1
		case '\\':
			i++
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if !inClass {
				for i+1 < len(src) && jsIdentifierByte(src[i+1]) {
					i++
				}
				return i
			}
		}
	}
	return -1
}

var (
	slotLookupCall = regexp.MustCompile(`\buseSlotExtensions\s*\(\s*@s(\d+)@\s*\)`)
	uiContextCall  = regexp.MustCompile(`\bapi(?:\s*<[^()\n]+>)?\s*\(\s*@t(\d+)@\s*,\s*\{\s*method\s*:\s*@s(\d+)@\s*,\s*body\s*:\s*\{([^{}]*)`)
	contextSlot    = regexp.MustCompile(`\bslot\s*:\s*@s(\d+)@`)
	themeLookup    = regexp.MustCompile(`\b(?:resolveSlotView|resolveSlotOverride|useDashboardSlotContext)\s*\(\s*@s(\d+)@\s*[,)]`)
	themeJSX       = regexp.MustCompile(`<Slot\b[^>]*\bname\s*=\s*@s(\d+)@`)
	themeFamily    = regexp.MustCompile(`\buseDashboardSlotContext\s*\(\s*@t(\d+)@\s*\)`)
)

// slotLookups recognises extension contributor subscriptions and iframe context
// requests, plus theme resolver calls and the Slot host component.
// Other strings (including SlotId unions, SLOT_IDS arrays, contribution maps,
// arbitrary {slot: ...} objects and examples) are not consumers. A new host
// interface must be taught here with a positive and negative fixture.
func slotLookups(code string, strs, templates []string) (slots, themes []string) {
	literal := func(values []string, index string) string {
		i, err := strconv.Atoi(index)
		if err != nil || i < 0 || i >= len(values) {
			return ""
		}
		return values[i]
	}
	for _, m := range slotLookupCall.FindAllStringSubmatch(code, -1) {
		if name := literal(strs, m[1]); name != "" {
			slots = append(slots, name)
		}
	}
	for _, m := range uiContextCall.FindAllStringSubmatch(code, -1) {
		path := literal(templates, m[1])
		if !strings.HasPrefix(path, "/api/v1/ext/") || !strings.HasSuffix(path, "/ui-context") || literal(strs, m[2]) != "POST" {
			continue
		}
		if slot := contextSlot.FindStringSubmatch(m[3]); slot != nil {
			if name := literal(strs, slot[1]); name != "" {
				slots = append(slots, name)
			}
		}
	}
	for _, re := range []*regexp.Regexp{themeLookup, themeJSX} {
		for _, m := range re.FindAllStringSubmatch(code, -1) {
			if name := literal(strs, m[1]); name != "" {
				themes = append(themes, name)
			}
		}
	}
	for _, m := range themeFamily.FindAllStringSubmatch(code, -1) {
		if prefix, _, ok := strings.Cut(literal(templates, m[1]), "${"); ok && prefix != "" {
			themes = append(themes, prefix+"*")
		}
	}
	return
}

// goFile is one parsed non-test Go file of the server.
type goFile struct {
	rel  string
	file *ast.File
}

func collectGo(t *testing.T, root string, c *consumers, contract []element) {
	t.Helper()
	var files []goFile
	fset := token.NewFileSet()
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" || d.Name() == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			files = append(files, goFile{filepath.ToSlash(rel), f})
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", top, err)
		}
	}

	goToJSON := map[string]string{}
	for js, gn := range goCapabilityFields() {
		goToJSON[gn] = js
	}
	prefixes := map[string]string{} // `"api:"` literal → api
	for js := range goCapabilityFields() {
		prefixes[strconv.Quote(js+":")] = js
	}

	for _, gf := range files {
		enumerator := enumeratorFiles[gf.rel]
		for _, decl := range gf.file.Decls {
			ast.Inspect(decl, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					name := ""
					switch fn := x.Fun.(type) {
					case *ast.SelectorExpr:
						name = fn.Sel.Name
					case *ast.Ident:
						name = fn.Name
					}
					if name == "HasAPI" && len(x.Args) == 1 {
						if lit, ok := x.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							if v, err := strconv.Unquote(lit.Value); err == nil {
								c.apiRights[v] = true
							}
						}
					}
				case *ast.SelectorExpr:
					if inner, ok := x.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "Capabilities" && !enumerator {
						if js, ok := goToJSON[x.Sel.Name]; ok {
							c.capFields[js] = true
						}
					}
				case *ast.IndexExpr:
					// p.Capabilities["api"]: the consented capabilities by group.
					if sel, ok := x.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "Capabilities" && !enumerator {
						if lit, ok := x.Index.(*ast.BasicLit); ok && lit.Kind == token.STRING {
							if v, err := strconv.Unquote(lit.Value); err == nil {
								c.capFields[v] = true
							}
						}
					}
				case *ast.BasicLit:
					// "events:" + name: a consent lookup by group.
					if js, ok := prefixes[x.Value]; ok && !enumerator {
						c.capFields[js] = true
					}
				}
				return true
			})
		}
	}

	used := typedContractConsumers(t, root)
	for _, e := range contract {
		c.contractUsed[e.name] = used[e.name]
	}
}

var gapLine = regexp.MustCompile(`^(\S+) (\S+) — (\S.*)$`)

// parseGaps reads "<kind> <name> — <reason>" lines; # starts a comment.
func parseGaps(path string) ([]gap, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	kinds := []string{kindSlot, kindThemeSlot, kindAPI, kindCapability, kindContract}
	var out []gap
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		m := gapLine.FindStringSubmatch(text)
		if m == nil {
			return nil, fmt.Errorf("%s:%d: want \"<kind> <name> — <reason>\", got %q", path, line, text)
		}
		if !slices.Contains(kinds, m[1]) {
			return nil, fmt.Errorf("%s:%d: kind %q is not one of %v", path, line, m[1], kinds)
		}
		out = append(out, gap{element{m[1], m[2]}, m[3], line})
	}
	return out, sc.Err()
}

// diffJSON names the JSON paths where two documents differ (at most 20).
func diffJSON(a, b []byte) []string {
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		return []string{"first document: " + err.Error()}
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		return []string{"second document: " + err.Error()}
	}
	var out []string
	var walk func(path string, x, y any)
	walk = func(path string, x, y any) {
		if len(out) >= 20 {
			return
		}
		switch xv := x.(type) {
		case map[string]any:
			yv, ok := y.(map[string]any)
			if !ok {
				out = append(out, path+": object vs "+short(y))
				return
			}
			keys := map[string]bool{}
			for k := range xv {
				keys[k] = true
			}
			for k := range yv {
				keys[k] = true
			}
			sorted := make([]string, 0, len(keys))
			for k := range keys {
				sorted = append(sorted, k)
			}
			sort.Strings(sorted)
			for _, k := range sorted {
				p := path + "." + k
				xk, inX := xv[k]
				yk, inY := yv[k]
				switch {
				case !inY:
					out = append(out, p+": only in the server schema")
				case !inX:
					out = append(out, p+": only in the SDK schema")
				default:
					walk(p, xk, yk)
				}
			}
		case []any:
			yv, ok := y.([]any)
			if !ok || len(xv) != len(yv) || !reflect.DeepEqual(xv, yv) {
				out = append(out, path+": "+short(x)+" vs "+short(y))
			}
		default:
			if !reflect.DeepEqual(x, y) {
				out = append(out, path+": "+short(x)+" vs "+short(y))
			}
		}
	}
	walk("$", va, vb)
	return out
}

func short(v any) string {
	raw, _ := json.Marshal(v)
	s := string(raw)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

func readFile(t *testing.T, root, rel string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Every per-value deprecated annotation must be enforced by the parser, not
// merely exempted from the consumer audit. Keeping the old enum/pattern also
// lets v1 manifests pass structural validation on their next update.
func deprecatedElements(t *testing.T, raw []byte) []element {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	defs := doc["$defs"].(map[string]any)
	caps := doc["properties"].(map[string]any)["capabilities"].(map[string]any)["properties"].(map[string]any)
	var out []element
	for _, group := range []struct {
		kind, field string
		schema      any
	}{
		{kindSlot, "ui", defs["slot"]},
		{kindAPI, "api", caps["api"].(map[string]any)["items"]},
	} {
		all, _ := group.schema.(map[string]any)["allOf"].([]any)
		for _, value := range all {
			rule := value.(map[string]any)
			then, _ := rule["then"].(map[string]any)
			if then["deprecated"] != true {
				continue
			}
			names := rule["if"].(map[string]any)["enum"].([]any)
			for _, name := range names {
				n := name.(string)
				payload, _ := json.Marshal(map[string]any{"id": "retired-test", "version": "1", "runtime": "external", "capabilities": map[string]any{group.field: []string{n}}})
				m, err := extensions.ParseManifest(payload)
				if err != nil {
					t.Fatal(err)
				}
				if slices.Contains(extensions.DeclaredCapabilities(m.Capabilities), group.field+":"+n) || len(m.Warnings) == 0 {
					t.Errorf("deprecated %s %q is not dropped with a warning", group.kind, n)
				}
				out = append(out, element{group.kind, n})
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("deprecated annotations disappeared")
	}
	return out
}
