// External HTTP/WebSocket boundary fixture. Only listens on loopback; no live
// credentials, internal store patches, or real project writes are used.
import http from "node:http";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const project = { id: "pagination-project", name: "ContentHub fixture", slug: "contenthub", workspace_id: "pagination-workspace", settings: {} };
const otherProject = { ...project, id: "other-project", name: "Other project", slug: "other" };
const workspace = { id: project.workspace_id, name: "Board regression", slug: "board-fixture", settings: {} };
const user = { id: "fixture-user", name: "Board reviewer", email: "reviewer@example.test", role: "owner" };
const statuses = [
  ["backlog", "Backlog", "#94a3b8"], ["todo", "Todo", "#3b82f6"], ["in_progress", "In Progress", "#eab308"],
  ["review", "Review", "#a855f7"], ["done", "Done", "#22c55e"], ["cancelled", "Cancelled", "#64748b"],
].map(([id, name, color], position) => ({ id, name, slug: id, category: id, color, position, project_id: project.id }));
const tasks = Array.from({ length: 331 }, (_, i) => ({
  id: `fixture-task-${i}`, project_id: project.id,
  status_id: i < 282 ? "done" : i < 315 ? "backlog" : i < 319 ? "todo" : i < 321 ? "in_progress" : "cancelled",
  title: i === 281 ? "Done from page two" : `Content task ${String(i).padStart(3, "0")}`,
  parent_task_id: i === 280 ? "fixture-task-0" : null,
  labels: [], assignee_type: "unassigned", assignee_id: null, priority: "medium", position: i,
  created_at: "2026-10-01T10:00:00Z", updated_at: "2026-10-01T10:00:00Z", custom_fields: {},
  context: {}, human_gate: false, due_date: null, subtask_count: i === 0 ? 1 : 0, comment_count: 0,
}));
// Newest-updated first: the API's default list order.
const initialOrder = [...tasks.slice(282), ...tasks.slice(0, 282)];
// Documents for the docs-shots spec: a three-level tree, one long page, one with a deep path.
const docs = [
  ["handbook", null, "Handbook", 0], ["onboarding", "handbook", "Onboarding", 0], ["day-one", "onboarding", "Day one checklist", 0],
  ["tooling", "onboarding", "Tooling", 1], ["runbooks", null, "Runbooks", 1], ["deploys", "runbooks", "Deploys and rollbacks", 0],
  ["incidents", "runbooks", "Incident response with a deliberately long title that has to wrap on a phone", 1], ["notes", null, "Notes", 2],
].map(([id, parent_id, title, position]) => ({
  id, project_id: project.id, parent_id, slug: id, title, storage_key: `documents/${id}.md`, position,
  created_by: user.id, created_by_type: "user", created_by_name: user.name, updated_by: user.id, updated_by_name: user.name,
  created_at: "2026-10-01T10:00:00Z", updated_at: "2026-10-02T11:30:00Z", version: 3, source_kind: "own",
  body: id === "deploys"
    ? "# Deploys and rollbacks\n\n" + Array.from({ length: 30 }, (_, i) => `Step ${i + 1}: check the dashboard, then continue with the next stage of the rollout.`).join("\n\n")
    : `# ${title}\n\nA short page about ${title.toLowerCase()}.\n\n- first point\n- second point`,
}));
// Theme settings spec: one installed theme whose workspace settings carry card_fields; the selection is kept here.
const graphite = {
  id: "graphite", name: "Graphite", version: "1.0.0", publisher: "fixture", description: "Dark and dense", source: "builtin", trust_level: "builtin",
  modes: ["light", "dark"], has_css: false, has_views: false, contrast_warnings: [],
  tokens: { light: { primary: "222 47% 20%", background: "0 0% 100%", card: "0 0% 100%", border: "214 32% 91%", foreground: "222 47% 11%" }, dark: { primary: "210 40% 96%", background: "222 47% 11%", card: "222 47% 13%", border: "217 33% 20%", foreground: "210 40% 98%" } },
  settings: { workspace: { type: "object", properties: { accent: { type: "string", title: "Accent colour", "x-widget": "color", "x-token": "primary" }, card_fields: { type: "array", title: "Card fields", items: { type: "string" }, "x-widget": "field-picker" } } } },
};
const forge = { ...graphite, id: "forge", name: "Forge", version: "1.0.0", source: "repo", trust_level: "community", description: "From a repository", settings: {} };
const PNG = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==";
let themePuts = 0, failThemes = 0, themesDelay = 0, iconUrl = null;
const slate = { ...graphite, id: "slate", name: "Slate", description: "Calm and light", settings: {} };
// The Go binary's own builtin themes (internal/themes/builtin): their manifests, stylesheet and views bundle are served
// as the real server serves them, so the spec exercises the shipped theme, not a stand-in.
const builtin = (id, file) => readFileSync(fileURLToPath(new URL(`../../internal/themes/builtin/${id}/${file}`, import.meta.url)), "utf8");
const sha = (text) => createHash("sha256").update(text).digest("hex");
const studioManifest = JSON.parse(builtin("board-studio", "mesh-extension.json"));
const defaultManifest = JSON.parse(builtin("mesh-default", "mesh-extension.json"));
const studioCss = `@layer mesh-theme {\n${builtin("board-studio", "theme.css")}\n}\n`, studioViews = builtin("board-studio", "views.js");
const assets = { [sha(studioCss)]: ["css", studioCss], [sha(studioViews)]: ["js", studioViews] };
const infoOf = (m, extra = {}) => ({
  id: m.id, name: m.name, version: m.version, publisher: m.publisher, description: m.description, source: "builtin", trust_level: "builtin",
  modes: m.theme.modes, has_css: !!m.theme.css, has_views: !!m.theme.views, slots: m.theme.slots, contrast_warnings: [],
  settings: { instance: m.schemas.instance, workspace: m.schemas.workspace }, tokens: m.theme.tokens ?? {}, ...extra,
});
const meshDefault = infoOf(defaultManifest), boardStudio = infoOf(studioManifest);
// Tokens as themes.BuildTokensCSS renders them: the theme's own values, then the settings bound to a token.
const tokensCss = (m, settings) => {
  const over = {};
  for (const [name, f] of Object.entries(m.schemas.workspace.properties)) if (f["x-token"] && settings[name] != null) over[f["x-token"]] = settings[name];
  const block = (sel, vals) => { const all = { ...vals, ...over }; const keys = Object.keys(all).sort(); return keys.length ? `${sel}{${keys.map((k) => `--${k}:${all[k]};`).join("")}}\n` : ""; };
  return block(":root", m.theme.tokens?.light ?? {}) + block(".dark", m.theme.tokens?.dark ?? m.theme.tokens?.light ?? {});
};
// Instance admin screen: the allow-list table and the journal, at the size of a real instance.
const ext = (id, name, kind, source, trust, extra = {}) => ({ id, name, kind, source, version: "1.0.0", trust_level: trust, allowed: !["community", "custom"].includes(trust), allow_explicit: false, has_instance_form: false, enabled: false, settings_source: "none", enabled_workspaces: 0, ...extra });
let adminAllowed = {}, adminAuto = false;
const adminItems = () => [
  ext("github", "GitHub", "integration", "builtin", "builtin", { has_instance_form: true, settings_source: "admin", enabled: true, enabled_workspaces: 6, version: "2.3.0" }),
  ext("gitlab", "GitLab", "integration", "builtin", "builtin", { has_instance_form: true, settings_source: "env", enabled: true, enabled_workspaces: 4, version: "2.1.0" }),
  ext("telegram", "Telegram", "integration", "builtin", "builtin", { has_instance_form: true, enabled_workspaces: 2 }),
  ext("team-relay", "Team Relay", "integration", "builtin", "builtin", { has_instance_form: true, settings_source: "admin", enabled: true, enabled_workspaces: 3 }),
  ext("spark", "Spark", "integration", "catalog", "official", { has_instance_form: true, enabled_workspaces: 1, version: "0.9.2" }),
  ext("agent-chat", "Agent chat", "plugin", "builtin", "builtin", { enabled_workspaces: 5 }),
  ext("keep", "Keep — a very long extension name that must wrap instead of pushing the columns", "integration", "repo", "custom", { has_instance_form: true, enabled_workspaces: 0, version: "0.3.1" }),
  ext("standup-bot", "Standup bot", "plugin", "catalog", "community", { enabled_workspaces: 0 }),
  ext("graphite", "Graphite", "theme", "builtin", "builtin", { enabled_workspaces: 1 }),
  ext("forge", "Forge", "theme", "repo", "community", { enabled_workspaces: 0 }),
].map((it) => (it.id in adminAllowed ? { ...it, allowed: adminAllowed[it.id], allow_explicit: true } : it));
const adminJournal = Array.from({ length: 60 }, (_, i) => {
  const at = new Date(Date.UTC(2026, 9, 27, 12) - i * 3 * 3600e3).toISOString();
  if (i % 4 === 3) return { id: `j${i}`, actor_id: "u-ann", actor_name: "Ann Admin", extension_id: ["standup-bot", "keep"][i % 2], action: "allow_changed", details: { allowed: i % 8 === 3 }, created_at: at };
  if (i % 9 === 4) return { id: `j${i}`, actor_id: "u-ann", actor_name: "Ann Admin", extension_id: "gitlab", action: "settings_changed", details: { keys: ["base_url", "token"] }, created_at: at };
  if (i % 13 === 5) return { id: `j${i}`, actor_id: "u-bob", actor_name: "Bob Builder", extension_id: "forge", action: "updated", details: { from: "1.0.0", to: "1.1.0" }, created_at: at };
  return { id: `j${i}`, actor_id: "u-bob", actor_name: "Bob Builder", extension_id: "graphite", action: "theme_selected", details: { level: i % 2 ? "workspace" : "instance" }, created_at: at };
});

const baseStatus = new Map(tasks.map((t) => [t.id, t.status_id]));
const readBody = async (req) => { let raw = ""; for await (const part of req) raw += part; return raw ? JSON.parse(raw) : {}; };
const broadcast = (event) => {
  const payload = Buffer.from(JSON.stringify(event));
  const frame = Buffer.concat([Buffer.from([0x81, payload.length]), payload]);
  for (const socket of sockets) socket.write(frame);
};
// Extensions catalog (U4): every installed theme is listed as a kind=theme extension next to one connected integration.
const extSummary = (t) => ({
  id: t.id, name: t.name, version: t.version, kind: "theme", runtime: "builtin", publisher: t.publisher, description: t.description,
  category: "documents", source: t.source, trust_level: t.trust_level, blocked: false, blocked_reason: "", allowed: true, manifest: { capabilities: {} },
});
const gitlabExt = {
  id: "gitlab", name: "GitLab", version: "1.0.0", kind: "integration", runtime: "builtin", publisher: "Entire VC", description: "Merge requests and pipelines on tasks",
  category: "development", source: "builtin", trust_level: "builtin", blocked: false, blocked_reason: "", allowed: true,
  manifest: { capabilities: {}, settings: { instance: { type: "object", properties: { base_url: { type: "string", title: "Base URL" }, token: { type: "string", title: "Access token", "x-secret": true } }, required: ["base_url"] } } },
};
let installed = [graphite, forge, meshDefault], instTheme = { theme_id: null, settings: {}, version: 1, contrast_warnings: [] };
let role = "owner", wsTheme = { theme_id: null, settings: {}, version: 1, contrast_warnings: [] };
let broadcastMoves = false, mode = "normal", ordered = initialOrder, taskCalls = [], writes = [];
const HOST = "127.0.0.1";
const sockets = new Set();
const server = http.createServer(async (req, res) => {
  res.setHeader("Access-Control-Allow-Origin", `http://${HOST}:3019`);
  res.setHeader("Access-Control-Allow-Credentials", "true");
  res.setHeader("Access-Control-Allow-Headers", "Authorization, Content-Type");
  res.setHeader("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS");
  if (req.method === "OPTIONS") { res.writeHead(204); return res.end(); }
  const url = new URL(req.url, `http://${HOST}`);
  let status = 200, body = {};
  if (url.pathname === "/__fixture") {
    if (req.method === "POST") {
      let raw = ""; for await (const part of req) raw += part;
      const next = JSON.parse(raw);
      mode = next.mode ?? "normal"; taskCalls = []; writes = []; broadcastMoves = next.broadcastMoves === true;
      role = next.role ?? "owner";
      if (next.resetTheme) {
        wsTheme = { theme_id: null, settings: {}, version: 1, contrast_warnings: [] };
        instTheme = { ...wsTheme }; installed = [graphite, forge, meshDefault]; adminAllowed = {}; adminAuto = false; themePuts = 0; failThemes = 0; themesDelay = 0; iconUrl = null;
      }
      if (next.installedEmpty) installed = [meshDefault];
      if (next.studio) {
        // The control theme chosen for the workspace, with whatever settings the spec brings.
        if (!installed.some((t) => t.id === "board-studio")) installed = [...installed, boardStudio];
        wsTheme = { theme_id: "board-studio", settings: next.studio === true ? {} : next.studio, version: wsTheme.version + 1, contrast_warnings: [] };
      }
      for (const t of tasks) t.status_id = baseStatus.get(t.id);
      // two people with work on the board (the person strip needs more than one lane), or nobody
      tasks.forEach((t, i) => {
        const who = next.assignees && i >= 315 && i < 321 ? (i % 3 === 0 ? ["ada", "Ada Lovelace"] : ["grace", "Grace Hopper"]) : null;
        Object.assign(t, who ? { assignee_id: who[0], assignee_type: "user", assignee_name: who[1] } : { assignee_id: null, assignee_type: "unassigned", assignee_name: undefined });
      });
      for (const t of tasks) if (next.resetTasks) { t.priority = "medium"; t.due_date = null; }
      if (next.failThemes != null) failThemes = next.failThemes;
      if (next.themesDelay != null) themesDelay = next.themesDelay;
      ordered = next.reordered ? [...tasks.slice(315, 319), ...initialOrder.filter((task) => !tasks.slice(315, 319).includes(task))] : initialOrder;
      if (next.event) broadcast({ type: "context_update", data: {}, project_id: project.id });
    }
    body = { mode, taskCalls, themePuts, writes };
  } else if (url.pathname === "/api/v1/auth/refresh") body = { tokens: { access_token: "fixture-token" } };
  else if (url.pathname === "/api/v1/auth/me") body = user;
  else if (url.pathname === "/api/v1/workspaces") body = [{ ...workspace, icon_url: iconUrl }];
  else if (url.pathname.endsWith("/projects")) body = { items: [project, otherProject], total_count: 2 };
  else if (url.pathname.endsWith("/statuses")) body = url.pathname.includes("/other-project/") ? statuses.map((s) => ({ ...s, project_id: otherProject.id })) : statuses;
  else if (url.pathname === "/api/v1/me/tasks") {
    // Dashboard "My tasks" widget: five assigned tasks, none, or a failing API.
    if (mode === "error") { status = 503; body = { message: "Fixture tasks unavailable" }; }
    else {
      const day = (n) => new Date(Date.now() + n * 864e5).toISOString();
      const rows = mode === "empty" ? [] : tasks.slice(315, 320).map((task, i) => ({ ...task, title: `Assigned task ${i + 1}`, priority: ["urgent", "high", "medium", "low", "medium"][i], due_date: [day(-2), day(1), day(3), day(20), null][i] }));
      body = { items: rows, page: 1, page_size: 5, total_count: rows.length, has_more: false };
    }
  }
  else if (url.pathname.endsWith("/tasks")) {
    const page = Number(url.searchParams.get("page") ?? 1), size = Number(url.searchParams.get("page_size") ?? 200);
    const ids = (url.searchParams.get("status") ?? "").split(",").filter(Boolean);
    if (url.pathname.includes("/other-project/")) {
      const rows = ids.length && !ids.includes("backlog") ? [] : [{ ...tasks[300], id: "other-task", project_id: otherProject.id, title: "Other project's only task" }];
      body = { items: rows, page, page_size: size, total_count: rows.length, has_more: false };
      res.writeHead(200, { "Content-Type": "application/json" }); return res.end(JSON.stringify(body));
    }
    taskCalls.push({ tag: url.searchParams.get("e2e"), status: url.searchParams.get("status"), page, list_revision: url.searchParams.get("list_revision") });
    if (mode === "slow") await new Promise((r) => setTimeout(r, 1500));
    if (mode === "error") { status = 503; body = { message: "Fixture tasks unavailable" }; }
    else {
      const rows = ids.length ? ordered.filter((task) => ids.includes(task.status_id)) : ordered;
      body = { items: rows.slice((page - 1) * size, page * size), page, page_size: size,
        total_count: rows.length, has_more: page * size < rows.length, list_revision: 47 };
    }
  } else if (url.pathname.endsWith("/documents")) body = { items: docs, total_count: docs.length, page: 1, page_size: 200, has_more: false };
  else if (url.pathname.endsWith("/team")) body = { agents: [], humans: [] };
  else if (url.pathname.startsWith("/api/v1/me/ui-state/")) body = { state: { expanded: [] } };
  else if (/^\/api\/v1\/documents\/[^/]+\/(watch|comments|attachments)/.test(url.pathname)) body = url.pathname.endsWith("/watch") ? { watching: false } : [];
  else if (url.pathname.startsWith("/api/v1/documents/")) body = docs.find((d) => d.id === url.pathname.split("/").at(-1)) ?? {};
  else if (url.pathname.endsWith("/custom-fields")) body = [];
  else if (url.pathname === "/api/v1/themes" && (failThemes > 0 || themesDelay > 0)) {
    if (themesDelay > 0) await new Promise((r) => setTimeout(r, themesDelay));
    if (failThemes > 0) { failThemes -= 1; status = 503; body = { message: "Themes unavailable" }; }
    else body = { default: "mesh-default", themes: installed, builtin_available: [] };
  }
  else if (url.pathname === "/api/v1/themes") body = { default: "mesh-default", themes: installed, builtin_available: [...(installed.some((t) => t.id === "slate") ? [] : ["slate"]), ...(installed.some((t) => t.id === "board-studio") ? [] : ["board-studio"])] };
  else if (url.pathname === "/api/v1/instance/theme") {
    if (req.method === "PUT") {
      let raw = ""; for await (const part of req) raw += part;
      const next = JSON.parse(raw);
      instTheme = { theme_id: next.theme_id ?? null, settings: next.settings ?? {}, version: instTheme.version + 1, contrast_warnings: [] };
    }
    body = instTheme;
  }
  else if (/^\/api\/v1\/theme\/assets\/[0-9a-f]{64}\.(js|css)$/.test(url.pathname)) {
    const asset = assets[url.pathname.split("/").at(-1).split(".")[0]];
    if (!asset) status = 404;
    else { res.writeHead(200, { "Content-Type": asset[0] === "js" ? "text/javascript" : "text/css", "Cache-Control": "no-store" }); return res.end(asset[1]); }
  }
  else if (url.pathname === "/api/v1/extensions/instance/overview") body = { auto_update: adminAuto, items: adminItems() };
  else if (url.pathname === "/api/v1/extensions/instance/journal") {
    const q = url.searchParams, since = q.get("since"), until = q.get("until");
    const entries = adminJournal.filter((e) => (!q.get("extension") || e.extension_id === q.get("extension")) && (!q.get("action") || e.action === q.get("action"))
      && (!q.get("actor") || e.actor_id === q.get("actor")) && (!since || e.created_at >= since) && (!until || e.created_at < until));
    body = { entries, ...(q.get("facets") === "1" ? { facets: {
      actions: [...new Set(adminJournal.map((e) => e.action))].sort(), extensions: [...new Set(adminJournal.map((e) => e.extension_id))].sort(),
      actors: [{ id: "u-ann", name: "Ann Admin" }, { id: "u-bob", name: "Bob Builder" }] } } : {}) };
  }
  else if (/^\/api\/v1\/extensions\/[^/]+\/allowed$/.test(url.pathname) && req.method === "PUT") {
    adminAllowed[url.pathname.split("/").at(-2)] = (await readBody(req)).allowed === true;
    body = {};
  }
  else if (url.pathname === "/api/v1/extensions/instance/auto-update" && req.method === "PUT") { adminAuto = (await readBody(req)).auto_update === true; body = {}; }
  else if (url.pathname === "/api/v1/extensions/gitlab/instance-settings") body = { extension_id: "gitlab", settings: { base_url: "https://git.example.com" }, enabled: true, source: "env", updated_at: "2026-10-03T10:00:00Z" };
  else if (url.pathname === "/api/v1/extensions") body = { extensions: [gitlabExt, ...installed.map(extSummary)] };
  // the instance administrator (instance-admin spec) also sees the repository updates list: none here
  else if (url.pathname === "/api/v1/extensions/updates") body = { updates: [] };
  else if (/^\/api\/v1\/workspaces\/[^/]+\/extensions$/.test(url.pathname)) {
    body = { extensions: [{ id: "gitlab", name: "GitLab", version: "1.0.0", kind: "integration", category: "development", manifest: { capabilities: {} },
      connection: { settings: {} }, status: { enabled: true, source: "workspace", health: { ok: true, message: "" } } }] };
  }
  // no Spark catalog and no instance administrator in this stand
  else if (url.pathname === "/api/v1/extensions/catalog" || url.pathname.endsWith("/catalog-card")) { status = 404; body = { error: "not found" }; }
  else if (url.pathname.startsWith("/api/v1/extensions/instance/")) { status = 403; body = { error: "forbidden" }; }
  else if (/^\/api\/v1\/extensions\/[^/]+\/update$/.test(url.pathname) && req.method === "POST") {
    installed = installed.map((t) => (t.id === "forge" ? { ...t, version: "1.1.0" } : t));
    body = {};
  }
  else if (/^\/api\/v1\/workspaces\/[^/]+\/icon$/.test(url.pathname) && req.method === "PUT") {
    for await (const part of req) void part;
    iconUrl = PNG;
    body = { ...workspace, icon_url: iconUrl };
  }
  else if (url.pathname === "/api/v1/instance/themes" && req.method === "POST") {
    const asked = await readBody(req);
    if (asked.id === "board-studio") { if (!installed.some((t) => t.id === "board-studio")) installed = [...installed, boardStudio]; }
    else if (!installed.some((t) => t.id === "slate")) installed = [...installed, slate];
    body = { extension_id: "ext-slate", version: "1.0.0", contrast_warnings: [] };
  }
  else if (/^\/api\/v1\/instance\/themes\/[^/]+$/.test(url.pathname) && req.method === "DELETE") {
    const id = url.pathname.split("/").at(-1);
    installed = installed.filter((t) => t.id !== id);
    res.writeHead(204, { "Cache-Control": "no-store" }); return res.end();
  }
  else if (url.pathname === "/api/v1/theme") {
    const studio = wsTheme.theme_id === "board-studio";
    const dflt = wsTheme.theme_id === "mesh-default";
    body = studio
      ? { theme_id: "board-studio", source: "workspace", tokens_css: tokensCss(studioManifest, wsTheme.settings), theme_css_url: `/api/v1/theme/assets/${sha(studioCss)}.css`, views_url: `/api/v1/theme/assets/${sha(studioViews)}.js`, settings: wsTheme.settings, modes: ["dark", "light"], hash: `fixture-${wsTheme.version}` }
      : { theme_id: wsTheme.theme_id ?? "mesh-default", tokens_css: dflt ? tokensCss(defaultManifest, wsTheme.settings) : "", theme_css_url: "", views_url: "", settings: wsTheme.settings, hash: `fixture-${wsTheme.version}` };
  }
  else if (/^\/api\/v1\/workspaces\/[^/]+\/theme$/.test(url.pathname)) {
    if (req.method === "PUT") {
      if (role !== "owner" && role !== "admin") { status = 403; body = { message: "forbidden" }; }
      else {
        let raw = ""; for await (const part of req) raw += part;
        const next = JSON.parse(raw);
        themePuts += 1;
        // any custom accent is reported as low contrast: shown to the user, never a reason to refuse the save
        const warn = next.settings?.accent ? [{ mode: "light", ratio: 2.1, message: "primary on background: 2.1" }] : [];
        wsTheme = { theme_id: next.theme_id ?? null, settings: next.settings ?? {}, version: wsTheme.version + 1, contrast_warnings: warn };
        body = wsTheme;
      }
    } else body = wsTheme;
  }
  else if (url.pathname.endsWith("/members/me")) body = { role };
  else if (url.pathname.endsWith("/members")) body = { members: [], count: 0 };
  else if (url.pathname.endsWith("/agents")) body = { items: [] };
  else if (url.pathname.endsWith("/views")) body = { views: [], count: 0 };
  else if (url.pathname === "/api/v1/notifications") body = { items: [], unread_count: 0 };
  else if (/^\/api\/v1\/tasks\/[^/]+\/move$/.test(url.pathname) && req.method === "POST") {
    // A move is kept; everyone connected is told only when a spec asks (broadcastMoves), since the fixture keeps no positions and a refetch would undo an in-column move.
    const task = tasks.find((t) => t.id === url.pathname.split("/").at(-2)), next = await readBody(req);
    if (task) { task.status_id = next.status_id ?? task.status_id; writes.push({ move: task.id, status_id: task.status_id }); if (broadcastMoves) broadcast({ type: "status_change", data: { task_id: task.id }, project_id: project.id }); }
    body = task ?? {};
  }
  else if (/^\/api\/v1\/tasks\/[^/]+$/.test(url.pathname) && req.method === "PATCH") {
    const task = tasks.find((t) => t.id === url.pathname.split("/").at(-1)), next = await readBody(req);
    if (task) { Object.assign(task, next, { updated_at: new Date().toISOString() }); writes.push({ patch: task.id, ...next }); }
    body = task ?? {};
  }
  else if (url.pathname.startsWith("/api/v1/tasks/")) body = tasks.find((task) => task.id === url.pathname.split("/").at(-1)) ?? {};
  else if (url.pathname === "/api/v1/runtime-flags") body = {};
  res.writeHead(status, { "Content-Type": "application/json", "Cache-Control": "no-store" });
  res.end(JSON.stringify(body));
});
server.on("upgrade", (req, socket) => {
  if (!req.url.startsWith("/ws?")) return socket.destroy();
  const accept = createHash("sha1").update(req.headers["sec-websocket-key"] + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").digest("base64");
  socket.write(`HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ${accept}\r\n\r\n`);
  sockets.add(socket); socket.on("close", () => sockets.delete(socket)); socket.on("error", () => sockets.delete(socket));
});
server.listen(8019, "127.0.0.1", () => console.log("Board fixture listening on 127.0.0.1:8019"));
