package refext

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"testing"
)

// The section page is client-side: a click must render the section and tell
// Mesh the new path. Run its script under node against a minimal DOM stub.
const domHarness = `
const posted = [];
const nodes = {};
const mk = (id) => ({ id, dataset: {}, children: [], attrs: {}, textContent: "",
  append(...c) { this.children.push(...c); }, setAttribute(k, v) { this.attrs[k] = v; },
  set innerHTML(_) { this.children = []; } });
const buttons = JSON.parse(process.env.BUTTONS).map((b) => {
  const el = mk(""); el.dataset = { go: b.go, replace: b.replace }; el.attrs = {};
  el.addEventListener = (ev, fn) => { if (ev === "click") el.click = fn; };
  return el;
});
global.document = {
  getElementById: (id) => (nodes[id] ??= mk(id)),
  createElement: () => mk(""),
  querySelectorAll: () => buttons,
};
global.location = { pathname: process.env.PATHNAME, hash: "" };
let onMessage;
const parent = { postMessage: (m, o) => posted.push([m, o]) };
global.window = { parent, addEventListener: (ev, fn) => { if (ev === "message") onMessage = fn; } };
global.setTimeout = () => 0; global.clearTimeout = () => {};
eval(process.env.SCRIPT);
const out = { initial: nodes.body.dataset.section, clicks: [], context: [], rerendered: null };
for (const path of ["/overview", "/reports", "/reports#monthly", ""]) {
  onMessage({ origin: "https://mesh.example", source: parent, data: { type: "mesh.context", path, expires_at: "2099-01-01T00:00:00Z" } });
  out.context.push({ path, shown: nodes.body.dataset.section });
}
// a renewal context with the same path must not re-render the section
{
  const before = nodes.body.children;
  onMessage({ origin: "https://mesh.example", source: parent, data: { type: "mesh.context", path: nodes.body.dataset.section, expires_at: "2099-01-01T00:00:00Z" } });
  out.rerendered = nodes.body.children !== before;
}
for (const b of buttons) {
  posted.length = 0; b.click();
  out.clicks.push({ go: b.dataset.go, shown: nodes.body.dataset.section, msg: posted[0] });
}
console.log(JSON.stringify(out));
`

func TestProjectSectionClicks(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("node is required in CI: the section clicks are only exercised here")
		}
		t.Skip("node not installed")
	}
	var page bytes.Buffer
	if err = projectPage.Execute(&page, map[string]any{"Ctx": Context{ProjectID: "p1"}, "MeshOrigin": "https://mesh.example", "Base": PathUIProject, "Roles": ""}); err != nil {
		t.Fatal(err)
	}
	script := regexp.MustCompile(`(?s)<script>(.*)</script>`).FindSubmatch(page.Bytes())
	if script == nil {
		t.Fatal("no script in project page")
	}
	cmd := exec.Command(node, "-e", domHarness)
	cmd.Env = []string{
		"SCRIPT=" + string(script[1]), "PATHNAME=" + PathUIProject + "/reports",
		`BUTTONS=[{"go":"/overview","replace":"0"},{"go":"/reports#weekly","replace":"0"},{"go":"/reports#monthly","replace":"0"},{"go":"/reports#monthly","replace":"1"}]`,
	}
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, raw)
	}
	var got struct {
		Initial    string
		Rerendered bool
		Context    []struct{ Path, Shown string }
		Clicks     []struct {
			Go, Shown string
			Msg       []any
		}
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("harness output %q: %v", raw, err)
	}
	if got.Initial != "/reports#weekly" {
		t.Fatalf("deep link /reports opens %q, want weekly", got.Initial)
	}
	// back/forward: the address changed without a click, Mesh tells the frame by mesh.context.path
	for i, want := range []string{"/overview", "/reports#weekly", "/reports#monthly", "/overview"} {
		if got.Context[i].Shown != want {
			t.Fatalf("mesh.context path %q shows %q, want %q", got.Context[i].Path, got.Context[i].Shown, want)
		}
	}
	if got.Rerendered {
		t.Fatal("a mesh.context with the unchanged path re-rendered the section")
	}
	replace := []bool{false, false, false, true}
	for i, c := range got.Clicks {
		msg, _ := c.Msg[0].(map[string]any)
		if c.Shown != c.Go || msg["type"] != "mesh.state" || msg["path"] != c.Go || msg["replace"] != replace[i] || c.Msg[1] != "https://mesh.example" {
			t.Fatalf("click %d (%s): shown=%q msg=%v", i, c.Go, c.Shown, c.Msg)
		}
	}
}
