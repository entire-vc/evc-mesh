package refext

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWKSPath is where Mesh publishes the keys of the iframe context token.
const JWKSPath = "/api/v1/.well-known/jwks.json"

// jwksTTL is how long a fetched key set is used before it is fetched again.
// A token with an unknown kid triggers an earlier refetch (keys rotate).
const jwksTTL = 5 * time.Minute

// jwksMinRefetch is the shortest interval between two fetches of the key set.
const jwksMinRefetch = 30 * time.Second

// Context is what Mesh tells a screen about the person who opened it.
type Context struct {
	UserID      string
	Email       string
	WorkspaceID string
	ProjectID   string // task.panel only
	TaskID      string // task.panel only
	DashboardID string // dashboard.widget only
	InstanceID  string // dashboard.widget only
	Roles       []string
}

// ContextVerifier checks the 60-second context token Mesh puts on a screen
// URL (docs/extensions/ui-iframe.md): ES256, key by kid from the JWKS,
// iss = the Mesh origin, aud = the extension id, exp not passed.
type ContextVerifier struct {
	MeshOrigin  string // e.g. https://mesh.example.com, no trailing slash
	ExtensionID string
	HTTP        *http.Client

	now func() time.Time

	mu        sync.Mutex
	keys      map[string]*ecdsa.PublicKey
	fetchedAt time.Time
}

func (v *ContextVerifier) clock() time.Time {
	if v.now != nil {
		return v.now()
	}
	return time.Now()
}

// Verify returns the context of a valid token.
func (v *ContextVerifier) Verify(ctx context.Context, token string) (*Context, error) {
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return v.key(ctx, kid)
	},
		jwt.WithValidMethods([]string{"ES256"}),
		jwt.WithIssuer(v.MeshOrigin),
		jwt.WithAudience(v.ExtensionID),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(v.clock),
	)
	if err != nil {
		return nil, err
	}
	str := func(k string) string { s, _ := claims[k].(string); return s }
	c := &Context{
		UserID: str("sub"), Email: str("email"), WorkspaceID: str("workspace_id"),
		ProjectID: str("project_id"), TaskID: str("task_id"),
		DashboardID: str("dashboard_id"), InstanceID: str("instance_id"),
	}
	if roles, ok := claims["roles"].([]any); ok {
		for _, r := range roles {
			if s, ok := r.(string); ok {
				c.Roles = append(c.Roles, s)
			}
		}
	}
	if c.UserID == "" || c.WorkspaceID == "" {
		return nil, errors.New("token lacks sub or workspace_id")
	}
	return c, nil
}

func (v *ContextVerifier) key(ctx context.Context, kid string) (*ecdsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	age := v.clock().Sub(v.fetchedAt)
	if k := v.keys[kid]; k != nil && age < jwksTTL {
		return k, nil
	}
	// An unknown kid refetches, but not more than once per jwksMinRefetch: a
	// stream of forged kids must not turn into a stream of requests to Mesh.
	if v.keys != nil && age < jwksMinRefetch {
		return nil, fmt.Errorf("unknown key %q", kid)
	}
	if err := v.fetchLocked(ctx); err != nil {
		return nil, err
	}
	if k := v.keys[kid]; k != nil {
		return k, nil
	}
	return nil, fmt.Errorf("unknown key %q", kid)
}

func (v *ContextVerifier) fetchLocked(ctx context.Context) error {
	client := v.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.MeshOrigin+JWKSPath, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch jwks: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch jwks: status %d", resp.StatusCode)
	}
	var doc struct {
		Keys []struct {
			Kty, Crv, Alg, Kid, X, Y string
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return fmt.Errorf("decode jwks: %w", err)
	}
	keys := map[string]*ecdsa.PublicKey{}
	for _, k := range doc.Keys {
		if k.Kty != "EC" || k.Crv != "P-256" || k.Kid == "" {
			continue
		}
		x, xerr := base64.RawURLEncoding.DecodeString(k.X)
		y, yerr := base64.RawURLEncoding.DecodeString(k.Y)
		if xerr != nil || yerr != nil || len(x) != 32 || len(y) != 32 {
			continue
		}
		// Uncompressed point 0x04||X||Y; the parser rejects points off the curve.
		pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	v.keys, v.fetchedAt = keys, v.clock()
	return nil
}

// screen serves the two iframe screens. The page is framed only by Mesh
// (frame-ancestors), renews its token through mesh.ready before it expires and
// paints itself with the Mesh theme (mesh.context, mesh.theme).
func (s *Server) screen(w http.ResponseWriter, r *http.Request) {
	ctx, err := s.UI.Verify(r.Context(), r.URL.Query().Get("token"))
	if err != nil {
		http.Error(w, "invalid context token", http.StatusUnauthorized)
		return
	}
	if r.URL.Path == PathUITask && ctx.TaskID == "" {
		http.Error(w, "task screen needs a task", http.StatusBadRequest)
		return
	}
	isProject := r.URL.Path == PathUIProject || strings.HasPrefix(r.URL.Path, PathUIProject+"/")
	if isProject && ctx.ProjectID == "" {
		http.Error(w, "project screen needs a project", http.StatusBadRequest)
		return
	}
	isWidget := r.URL.Path == PathUIWidget
	if isWidget && (ctx.DashboardID == "" || ctx.InstanceID == "") {
		http.Error(w, "widget screen needs a dashboard and an instance", http.StatusBadRequest)
		return
	}
	page := screenPage
	switch {
	case isProject:
		page = projectPage
	case isWidget:
		page = widgetPage
	}
	w.Header().Set("Content-Security-Policy", "frame-ancestors "+s.UI.MeshOrigin)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = page.Execute(w, map[string]any{
		"Ctx": ctx, "MeshOrigin": s.UI.MeshOrigin, "Base": PathUIProject, "Roles": strings.Join(ctx.Roles, ", "),
	})
}

var screenPage = template.Must(template.New("screen").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>Reference extension</title>
<style>
  :root { color-scheme: light dark; }
  body { font: 14px/1.4 var(--font-sans, system-ui, sans-serif); margin: 0; padding: 16px;
         background: hsl(var(--background, 0 0% 100%)); color: hsl(var(--foreground, 0 0% 10%)); }
  .muted { color: hsl(var(--muted-foreground, 0 0% 45%)); }
</style></head>
<body>
<h1>Hello{{if .Ctx.Email}}, {{.Ctx.Email}}{{end}}</h1>
<p>Workspace {{.Ctx.WorkspaceID}}{{if .Roles}} ({{.Roles}}){{end}}</p>
{{if .Ctx.TaskID}}<p>Task {{.Ctx.TaskID}} in project {{.Ctx.ProjectID}}</p>{{end}}
<p class="muted">Mesh theme <code data-testid="mode"></code></p>
<script>
  const mesh = {{.MeshOrigin}};
  let renew;
  // The Mesh theme arrives in mesh.context and, on a switch, in mesh.theme: the same helper as the widget's.
  function applyMeshTheme(theme) {
    const root = document.documentElement;
    for (const [name, value] of Object.entries((theme && theme.tokens) || {})) {
      if (/^--[a-z0-9-]+$/i.test(name) && typeof value === "string") root.style.setProperty(name, value);
    }
    const mode = theme && theme.mode === "dark" ? "dark" : "light";
    root.dataset.meshMode = mode;
    root.style.colorScheme = mode;
    document.querySelector('[data-testid="mode"]').textContent = mode;
  }
  window.addEventListener("message", (e) => {
    if (e.origin !== mesh || e.source !== window.parent || !e.data) return;
    if (e.data.type === "mesh.theme") applyMeshTheme(e.data);
    if (e.data.type !== "mesh.context") return;
    if (e.data.theme) applyMeshTheme(e.data.theme);
    // Send e.data.token with every call to this service; ask for a new one before it expires.
    clearTimeout(renew);
    const left = Date.parse(e.data.expires_at) - Date.now();
    renew = setTimeout(() => window.parent.postMessage({ type: "mesh.ready" }, mesh), Math.max(left - 10000, 1000));
  });
  window.parent.postMessage({ type: "mesh.ready" }, mesh);
  window.parent.postMessage({ type: "mesh.resize", height: document.body.scrollHeight }, mesh);
</script>
</body></html>
`))

// projectPage is the project.view screen: two sections (/overview, /reports
// with a #weekly or #monthly hash) that switch without reloading the frame and
// tell Mesh through mesh.state, so the Mesh address follows. The frame opens
// at the section named in its own address (a reload or a copied link).
var projectPage = template.Must(template.New("project").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>Reference extension: project</title>
<style>
  body { font: 14px system-ui, sans-serif; margin: 0; padding: 16px; max-width: 720px; }
  nav { display: flex; flex-wrap: wrap; gap: 8px; margin: 12px 0; }
  button { font: inherit; padding: 8px 12px; min-height: 40px; cursor: pointer; }
  button[aria-current="true"] { font-weight: 600; outline: 2px solid currentColor; }
  section { border: 1px solid #ccc; border-radius: 6px; padding: 12px 16px; margin: 12px 0; }
  code { overflow-wrap: anywhere; }
</style></head>
<body>
<h1 data-testid="title">Reference project{{if .Ctx.Email}} · {{.Ctx.Email}}{{end}}</h1>
<p>Workspace <code data-testid="workspace">{{.Ctx.WorkspaceID}}</code>,
   project <code data-testid="project">{{.Ctx.ProjectID}}</code></p>
<p>Frame marker <code data-testid="marker" id="marker"></code> (changes only when the frame reloads)</p>
<nav aria-label="Sections">
  <button data-go="/overview" data-replace="0">Overview</button>
  <button data-go="/reports#weekly" data-replace="0">Reports: weekly</button>
  <button data-go="/reports#monthly" data-replace="0">Reports: monthly</button>
  <button data-go="/reports#monthly" data-replace="1">Replace with monthly</button>
</nav>
<section id="body" aria-live="polite"></section>
<script>
  const mesh = {{.MeshOrigin}};
  const base = {{.Base}};
  const marker = Math.random().toString(36).slice(2, 10);
  document.getElementById("marker").textContent = marker;
  const bodies = {
    "/overview": ["Overview", "Synthetic project overview: 3 open items, 0 blockers."],
    "/reports#weekly": ["Weekly report", "Synthetic weekly report: week 44, 12 items closed."],
    "/reports#monthly": ["Monthly report", "Synthetic monthly report: October, 48 items closed."],
  };
  function current() {
    let p = location.pathname.startsWith(base) ? location.pathname.slice(base.length) : "";
    if (p === "" || p === "/") p = "/overview";
    if (p === "/reports" && !location.hash) return "/reports#weekly";
    return p + location.hash;
  }
  function show(section) {
    const [title, text] = bodies[section] || bodies["/overview"];
    const el = document.getElementById("body");
    el.dataset.section = section;
    el.innerHTML = "";
    const h = document.createElement("h2"); h.textContent = title; h.dataset.testid = "section-title";
    const t = document.createElement("p"); t.textContent = text; t.dataset.testid = "section-text";
    const s = document.createElement("p"); s.append("Section: ");
    const c = document.createElement("code"); c.dataset.testid = "section"; c.textContent = section; s.append(c);
    el.append(h, t, s);
    document.querySelectorAll("button[data-go]").forEach((b) => b.setAttribute("aria-current", String(b.dataset.go === section && b.dataset.replace === "0")));
  }
  document.querySelectorAll("button[data-go]").forEach((b) => b.addEventListener("click", () => {
    show(b.dataset.go);
    window.parent.postMessage({ type: "mesh.state", path: b.dataset.go, replace: b.dataset.replace === "1" }, mesh);
  }));
  show(current());
  let renew;
  window.addEventListener("message", (e) => {
    if (e.origin !== mesh || e.source !== window.parent || !e.data || e.data.type !== "mesh.context") return;
    // The address moved without a click here (back/forward, a link): context.path is the section to show.
    if (typeof e.data.path === "string") {
      let next = e.data.path === "" || e.data.path === "/" ? "/overview" : e.data.path;
      if (next === "/reports") next = "/reports#weekly";
      if (next !== document.getElementById("body").dataset.section) show(next);
    }
    clearTimeout(renew);
    const left = Date.parse(e.data.expires_at) - Date.now();
    renew = setTimeout(() => window.parent.postMessage({ type: "mesh.ready" }, mesh), Math.max(left - 10000, 1000));
  });
  window.parent.postMessage({ type: "mesh.ready" }, mesh);
  window.parent.postMessage({ type: "mesh.app-ready" }, mesh);
</script>
</body></html>
`))

// widgetPage is the dashboard.widget screen (protocol v2): it paints itself with
// the Mesh theme (applyMeshTheme), follows mesh.theme, shows the instance
// settings, redraws on mesh.widget.settings, reloads on mesh.widget.refresh,
// pauses on mesh.widget.visibility, turns into a stub on mesh.revoked, and asks
// Mesh to open a task or a document (mesh.open). It also posts a mesh.resize,
// which Mesh ignores in this slot: the dashboard cell sets the size.
var widgetPage = template.Must(template.New("widget").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>Reference widget</title>
<style>
  :root { color-scheme: light dark; }
  body { font: 14px/1.4 var(--font-sans, system-ui, sans-serif); margin: 0; padding: 12px;
         background: hsl(var(--background, 0 0% 100%)); color: hsl(var(--foreground, 0 0% 10%)); }
  h1 { font-size: 15px; margin: 0 0 8px; }
  dl { display: grid; grid-template-columns: max-content 1fr; gap: 2px 12px; margin: 8px 0; }
  dt { color: hsl(var(--muted-foreground, 0 0% 45%)); }
  dd { margin: 0; overflow-wrap: anywhere; }
  .row { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 10px; }
  button { font: inherit; min-height: 40px; padding: 6px 12px; cursor: pointer; border-radius: var(--radius, 6px);
           border: 1px solid hsl(var(--border, 0 0% 85%)); background: hsl(var(--card, 0 0% 100%)); color: hsl(var(--card-foreground, 0 0% 10%)); }
  .stub { color: hsl(var(--muted-foreground, 0 0% 45%)); }
</style></head>
<body>
<main id="app">
<h1 data-testid="title">Reference widget</h1>
<dl>
  <dt>Mode</dt><dd data-testid="mode"></dd>
  <dt>Background</dt><dd data-testid="bg"></dd>
  <dt>Settings</dt><dd data-testid="settings"></dd>
  <dt>Visible</dt><dd data-testid="visible"></dd>
  <dt>Loads</dt><dd data-testid="loads">0</dd>
  <dt>Instance</dt><dd data-testid="instance">{{.Ctx.InstanceID}}</dd>
</dl>
<div class="row">
  <button data-testid="open-own" data-kind="task" data-id="">Open my task</button>
  <button data-testid="open-foreign" data-kind="task" data-id="">Open another task</button>
  <button data-testid="configure">Settings</button>
</div>
</main>
<script>
  const mesh = {{.MeshOrigin}};
  const $ = (t) => document.querySelector('[data-testid="' + t + '"]');
  let renew, loads = 0, revoked = false;

  // applyMeshTheme(tokens): the one helper a widget needs to look like Mesh.
  // tokens are the CSS variables of the Mesh theme (name -> value); they land on :root,
  // and the colour scheme follows mode so form controls and scrollbars match.
  function applyMeshTheme(theme) {
    const root = document.documentElement;
    for (const [name, value] of Object.entries((theme && theme.tokens) || {})) {
      if (/^--[a-z0-9-]+$/i.test(name) && typeof value === "string") root.style.setProperty(name, value);
    }
    const mode = theme && theme.mode === "dark" ? "dark" : "light";
    root.dataset.meshMode = mode;
    root.style.colorScheme = mode;
    $("mode").textContent = mode;
    $("bg").textContent = getComputedStyle(document.body).backgroundColor || "";
  }
  function showSettings(s) { $("settings").textContent = JSON.stringify(s || {}); }
  function load() { loads += 1; $("loads").textContent = String(loads); }
  function revokedStub() {
    revoked = true;
    clearTimeout(renew);
    document.getElementById("app").innerHTML = "";
    const p = document.createElement("p"); p.className = "stub"; p.dataset.testid = "revoked";
    p.textContent = "Access to this widget was removed.";
    document.getElementById("app").append(p);
  }
  function send(m) { window.parent.postMessage(m, mesh); }

  window.addEventListener("message", (e) => {
    if (e.origin !== mesh || e.source !== window.parent || !e.data || revoked) return;
    const m = e.data;
    switch (m.type) {
      case "mesh.context": {
        if (m.theme) applyMeshTheme(m.theme);
        if (m.widget) {
          showSettings(m.widget.settings);
          const own = m.task_id || (m.widget.settings && m.widget.settings.task_id) || "";
          $("open-own").dataset.id = own;
          $("open-foreign").dataset.id = (m.widget.settings && m.widget.settings.foreign_task_id) || "";
        }
        clearTimeout(renew);
        const left = Date.parse(m.expires_at) - Date.now();
        renew = setTimeout(() => send({ type: "mesh.ready" }), Math.max(left - 10000, 1000));
        break;
      }
      case "mesh.theme": applyMeshTheme(m); break;
      case "mesh.widget.settings": showSettings(m.settings); break;
      case "mesh.widget.refresh": load(); break;
      case "mesh.widget.visibility": $("visible").textContent = String(m.visible === true); break;
      case "mesh.revoked": revokedStub(); break;
    }
  });
  document.querySelectorAll("button[data-kind]").forEach((b) => b.addEventListener("click", () => {
    if (b.dataset.id) send({ type: "mesh.open", kind: b.dataset.kind, id: b.dataset.id });
  }));
  $("configure").addEventListener("click", () => send({ type: "mesh.widget.configure" }));
  load();
  send({ type: "mesh.ready" });
  send({ type: "mesh.resize", height: document.body.scrollHeight });
  send({ type: "mesh.app-ready" });
</script>
</body></html>
`))
