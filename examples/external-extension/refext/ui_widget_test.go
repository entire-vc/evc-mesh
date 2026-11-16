package refext

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

// The widget page is client-side. Its side of the v2 contract is run under node
// against a minimal DOM stub: the theme arrives in mesh.context and changes on
// mesh.theme, the instance settings arrive and follow mesh.widget.settings,
// mesh.open carries the task of the settings, and mesh.revoked turns the page
// into a stub. Messages from another window or origin change nothing.
const widgetHarness = `
const posted = [];
const nodes = {};
const mk = (id) => ({ id, dataset: {}, children: [], textContent: "", className: "", handlers: {},
  append(...c) { this.children.push(...c); }, addEventListener(ev, fn) { this.handlers[ev] = fn; },
  set innerHTML(_) { this.children = []; } });
const tid = (t) => (nodes[t] ??= mk(t));
const props = {};
const root = { dataset: {}, style: { setProperty: (k, v) => { props[k] = v; }, colorScheme: "" } };
const buttons = ["open-own", "open-foreign"].map((t) => { const b = tid(t); b.dataset = { kind: "task", id: "" }; return b; });
global.document = {
  documentElement: root, body: {},
  querySelector: (sel) => tid(sel.replace(/^\[data-testid="|"\]$/g, "")),
  getElementById: (id) => tid("#" + id),
  createElement: () => mk(""),
  querySelectorAll: () => buttons,
};
global.getComputedStyle = () => ({ backgroundColor: "rgb(10, 10, 10)" });
let onMessage;
const parent = { postMessage: (m, o) => posted.push([m, o]) };
global.window = { parent, addEventListener: (ev, fn) => { if (ev === "message") onMessage = fn; } };
global.setTimeout = () => 0; global.clearTimeout = () => {};
eval(process.env.SCRIPT);
const O = "https://mesh.example";
const msg = (data, o = O, s = parent) => onMessage({ origin: o, source: s, data });
const out = { boot: posted.map((p) => p[0].type) };
msg({ type: "mesh.context", version: 2, slot: "dashboard.widget", expires_at: "2099-01-01T00:00:00Z",
  widget: { type: "ref", instance_id: "i1", dashboard_id: "d1", settings: { title: "A", task_id: "own-task", foreign_task_id: "foreign-task" }, size: { w: 2, h: 2 } },
  theme: { mode: "light", tokens: { "--background": "0 0% 100%" } } });
out.ctx = { mode: tid("mode").textContent, bg: props["--background"], settings: tid("settings").textContent, scheme: root.style.colorScheme };
msg({ type: "mesh.theme", mode: "dark", tokens: { "--background": "222 14% 9%" } });
out.dark = { mode: tid("mode").textContent, bg: props["--background"], scheme: root.style.colorScheme, dataMode: root.dataset.meshMode };
msg({ type: "mesh.widget.settings", settings: { title: "B" } });
out.settings = tid("settings").textContent;
msg({ type: "mesh.widget.visibility", visible: false }); out.hidden = tid("visible").textContent;
msg({ type: "mesh.widget.refresh" }); out.loads = tid("loads").textContent;
posted.length = 0;
tid("open-own").handlers.click(); tid("open-foreign").handlers.click(); tid("configure").handlers.click();
out.clicks = posted.map((p) => p[0]);
// foreign window / origin: ignored
msg({ type: "mesh.theme", mode: "light", tokens: { "--background": "1 1% 1%" } }, "https://evil.example");
msg({ type: "mesh.theme", mode: "light", tokens: { "--background": "1 1% 1%" } }, O, {});
out.foreign = props["--background"];
// a token that is not a CSS variable name is not applied
msg({ type: "mesh.theme", mode: "dark", tokens: { "color: red": "x", "--ok": "1" } });
out.bad = { injected: "color: red" in props, ok: props["--ok"] };
msg({ type: "mesh.revoked", reason: "conflict" });
out.revoked = { stub: tid("#app").children.length === 1 && tid("#app").children[0].dataset.testid };
msg({ type: "mesh.theme", mode: "light", tokens: { "--background": "9 9% 9%" } });
out.afterRevoke = props["--background"];
console.log(JSON.stringify(out));
`

func TestWidgetPageContract(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("node is required in CI: the widget contract is only exercised here")
		}
		t.Skip("node not installed")
	}
	var page bytes.Buffer
	if err = widgetPage.Execute(&page, map[string]any{"Ctx": Context{DashboardID: "d1", InstanceID: "i1"}, "MeshOrigin": "https://mesh.example"}); err != nil {
		t.Fatal(err)
	}
	script := regexp.MustCompile(`(?s)<script>(.*)</script>`).FindSubmatch(page.Bytes())
	if script == nil {
		t.Fatal("no script in widget page")
	}
	cmd := exec.Command(node, "-e", widgetHarness)
	cmd.Env = []string{"SCRIPT=" + string(script[1])}
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, raw)
	}
	var got struct {
		Boot                             []string
		Ctx                              struct{ Mode, Bg, Settings, Scheme string }
		Dark                             struct{ Mode, Bg, Scheme, DataMode string }
		Settings, Hidden, Loads, Foreign string
		Clicks                           []map[string]any
		Bad                              struct {
			Injected bool
			Ok       string
		}
		Revoked     struct{ Stub string }
		AfterRevoke string
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("harness output %q: %v", raw, err)
	}
	if want := []string{"mesh.ready", "mesh.resize", "mesh.app-ready"}; len(got.Boot) != 3 || got.Boot[0] != want[0] || got.Boot[1] != want[1] || got.Boot[2] != want[2] {
		t.Fatalf("boot messages %v, want %v", got.Boot, want)
	}
	if got.Ctx.Mode != "light" || got.Ctx.Bg != "0 0% 100%" || got.Ctx.Scheme != "light" || got.Ctx.Settings != `{"title":"A","task_id":"own-task","foreign_task_id":"foreign-task"}` {
		t.Fatalf("theme and settings from mesh.context: %+v", got.Ctx)
	}
	if got.Dark.Mode != "dark" || got.Dark.Bg != "222 14% 9%" || got.Dark.Scheme != "dark" || got.Dark.DataMode != "dark" {
		t.Fatalf("mesh.theme did not switch the widget to dark: %+v", got.Dark)
	}
	if got.Settings != `{"title":"B"}` {
		t.Fatalf("mesh.widget.settings: %q", got.Settings)
	}
	if got.Hidden != "false" || got.Loads != "2" {
		t.Fatalf("visibility %q, loads %q (a refresh reloads once)", got.Hidden, got.Loads)
	}
	if len(got.Clicks) != 3 || got.Clicks[0]["type"] != "mesh.open" || got.Clicks[0]["kind"] != "task" || got.Clicks[0]["id"] != "own-task" ||
		got.Clicks[1]["id"] != "foreign-task" || got.Clicks[2]["type"] != "mesh.widget.configure" {
		t.Fatalf("clicks: %v", got.Clicks)
	}
	if got.Foreign != "222 14% 9%" {
		t.Fatalf("a message from another origin or window changed the theme: %q", got.Foreign)
	}
	if got.Bad.Injected || got.Bad.Ok != "1" {
		t.Fatalf("token names are not filtered: %+v", got.Bad)
	}
	if got.Revoked.Stub != "revoked" || got.AfterRevoke != "222 14% 9%" {
		t.Fatalf("mesh.revoked: stub=%q, a later theme changed the page to %q", got.Revoked.Stub, got.AfterRevoke)
	}
}

func TestWidgetScreen(t *testing.T) {
	m := newFakeMesh(t)
	now := t0
	s := newServer()
	s.UI = m.verifier(&now)
	get := func(token string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, PathUIWidget+"?token="+token, http.NoBody))
		return rec
	}
	withIDs := m.token(t, func(c jwt.MapClaims, _ *jwt.Token) {
		c["dashboard_id"], c["instance_id"] = "d1", "i1"
	})
	rec := get(withIDs)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "i1") || !strings.Contains(rec.Body.String(), "applyMeshTheme") {
		t.Fatalf("widget screen: %d %s", rec.Code, rec.Body)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); csp != "frame-ancestors "+m.srv.URL {
		t.Fatalf("CSP: %q", csp)
	}
	// A token without the dashboard and the instance is not a widget token.
	if rec := get(m.token(t, nil)); rec.Code != http.StatusBadRequest {
		t.Fatalf("widget screen without ids: want 400, got %d", rec.Code)
	}
}

// refextWidgetGolden is the widget page as the dashboard e2e serves it
// (web/board-e2e/dashboard-widgets.spec.ts): the real template, with placeholders
// the spec fills per instance. REFEXT_UPDATE_GOLDEN=1 rewrites it after a change
// to widgetPage; otherwise a stale copy fails here, before the e2e shoots an old page.
const refextWidgetGolden = "../../../web/board-e2e/refext-widget.html"

func TestWidgetPageGolden(t *testing.T) {
	var buf bytes.Buffer
	err := widgetPage.Execute(&buf, map[string]any{
		"Ctx":        &Context{WorkspaceID: "__WORKSPACE__", DashboardID: "__DASHBOARD__", InstanceID: "__INSTANCE__"},
		"MeshOrigin": "http://127.0.0.1:3019", "Base": PathUIProject, "Roles": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("REFEXT_UPDATE_GOLDEN") == "1" {
		if werr := os.WriteFile(refextWidgetGolden, buf.Bytes(), 0o644); werr != nil {
			t.Fatal(werr)
		}
	}
	got, err := os.ReadFile(refextWidgetGolden)
	if err != nil {
		t.Fatalf("golden: %v (REFEXT_UPDATE_GOLDEN=1 writes it)", err)
	}
	if !bytes.Equal(got, buf.Bytes()) {
		t.Fatalf("%s is stale: run REFEXT_UPDATE_GOLDEN=1 go test ./examples/external-extension/refext -run TestWidgetPageGolden", refextWidgetGolden)
	}
}
