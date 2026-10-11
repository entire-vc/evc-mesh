// External HTTP/WebSocket boundary fixture. Only listens on loopback; no live
// credentials, internal store patches, or real project writes are used.
import http from "node:http";
import { createHash } from "node:crypto";

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
let mode = "normal", ordered = initialOrder, taskCalls = [];
const sockets = new Set();
const server = http.createServer(async (req, res) => {
  res.setHeader("Access-Control-Allow-Origin", "http://127.0.0.1:3019");
  res.setHeader("Access-Control-Allow-Credentials", "true");
  res.setHeader("Access-Control-Allow-Headers", "Authorization, Content-Type");
  if (req.method === "OPTIONS") { res.writeHead(204); return res.end(); }
  const url = new URL(req.url, "http://127.0.0.1");
  let status = 200, body = {};
  if (url.pathname === "/__fixture") {
    if (req.method === "POST") {
      let raw = ""; for await (const part of req) raw += part;
      const next = JSON.parse(raw);
      mode = next.mode ?? "normal"; taskCalls = [];
      ordered = next.reordered ? [...tasks.slice(315, 319), ...initialOrder.filter((task) => !tasks.slice(315, 319).includes(task))] : initialOrder;
      if (next.event) {
        const payload = Buffer.from(JSON.stringify({ type: "context_update", data: {}, project_id: project.id }));
        const frame = Buffer.concat([Buffer.from([0x81, payload.length]), payload]);
        for (const socket of sockets) socket.write(frame);
      }
    }
    body = { mode, taskCalls };
  } else if (url.pathname === "/api/v1/auth/refresh") body = { tokens: { access_token: "fixture-only-token" } };
  else if (url.pathname === "/api/v1/auth/me") body = user;
  else if (url.pathname === "/api/v1/workspaces") body = [workspace];
  else if (url.pathname.endsWith("/projects")) body = { items: [project, otherProject], total_count: 2 };
  else if (url.pathname.endsWith("/statuses")) body = url.pathname.includes("/other-project/") ? statuses.map((s) => ({ ...s, project_id: otherProject.id })) : statuses;
  else if (url.pathname.endsWith("/tasks")) {
    const page = Number(url.searchParams.get("page") ?? 1), size = Number(url.searchParams.get("page_size") ?? 200);
    const ids = (url.searchParams.get("status") ?? "").split(",").filter(Boolean);
    if (url.pathname.includes("/other-project/")) {
      const rows = ids.length && !ids.includes("backlog") ? [] : [{ ...tasks[300], id: "other-task", project_id: otherProject.id, title: "Other project's only task" }];
      body = { items: rows, page, page_size: size, total_count: rows.length, has_more: false };
      res.writeHead(200, { "Content-Type": "application/json" }); return res.end(JSON.stringify(body));
    }
    taskCalls.push({ status: url.searchParams.get("status"), page, list_revision: url.searchParams.get("list_revision") });
    if (mode === "slow") await new Promise((r) => setTimeout(r, 1500));
    if (mode === "error") { status = 503; body = { message: "Fixture tasks unavailable" }; }
    else {
      const rows = ids.length ? ordered.filter((task) => ids.includes(task.status_id)) : ordered;
      body = { items: rows.slice((page - 1) * size, page * size), page, page_size: size,
        total_count: rows.length, has_more: page * size < rows.length, list_revision: 47 };
    }
  } else if (url.pathname.endsWith("/custom-fields")) body = [];
  else if (url.pathname.endsWith("/members/me")) body = { role: "owner" };
  else if (url.pathname.endsWith("/members")) body = { members: [], count: 0 };
  else if (url.pathname.endsWith("/agents")) body = { items: [] };
  else if (url.pathname.endsWith("/views")) body = { views: [], count: 0 };
  else if (url.pathname === "/api/v1/notifications") body = { items: [], unread_count: 0 };
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
