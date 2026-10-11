// Read-only production acceptance. Uses a NEW browser context and native login,
// never a copied refresh cookie. Logs only counts, timings and task-list metadata.
import { readFile } from "node:fs/promises";
import { homedir } from "node:os";
import { chromium, expect } from "@playwright/test";

const base = "https://mesh.entire.host";
const project = "feceae7f-5037-4fdd-bf76-d149ede06281";
const text = await readFile(`${homedir()}/.config/agents/keys.env`, "utf8");
const field = (name) => text.split("\n").find((line) => line.startsWith(`${name}=`))?.slice(name.length + 1).trim().replace(/^['"]|['"]$/g, "");
const browser = await chromium.launch({ channel: process.env.BOARD_BROWSER_CHANNEL });
try {
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, serviceWorkers: "block" });
  const login = await context.request.post(`${base}/api/v1/auth/login`, {
    data: { email: field("MESH_FLEET_ADMIN_EMAIL") || "fleet-admin@entire.vc", password: field("MESH_FLEET_ADMIN_PASSWORD") },
  });
  if (!login.ok()) throw new Error(`Fleet login HTTP ${login.status()}`);
  const auth = await login.json();
  const headers = { Authorization: `Bearer ${auth.tokens.access_token}` };
  const api = async (path) => {
    const response = await context.request.get(`${base}/api/v1${path}`, { headers });
    if (!response.ok()) throw new Error(`Read-only API HTTP ${response.status()}`);
    return response.json();
  };
  const statuses = await api(`/projects/${project}/statuses`);
  const page = await context.newPage();
  // Count what the Board itself asks for: the regression this guards against
  // (!1104) walked every page of the project on every load and WS refresh.
  const taskRequests = [];
  page.on("request", (r) => { if (/\/projects\/[^/]+\/tasks(\?|$)/.test(r.url())) taskRequests.push(r.url()); });
  await page.addInitScript((id) => localStorage.setItem(`mesh_board_filters_${id}`, JSON.stringify({ showClosed: true, showSubtasks: true })), project);
  const started = Date.now();
  await page.goto(`${base}/w/ws-006901c7/p/contenthub`);
  await page.locator('[data-testid="board-column"]').first().waitFor();
  console.log(`Board rendered in ${Date.now() - started} ms with ${taskRequests.length} task-list requests`);
  for (const url of taskRequests) expect(new URL(url).searchParams.get("list_revision")).toBeNull();
  for (const status of statuses) {
    const data = await api(`/projects/${project}/tasks?status=${status.id}&page_size=200&include_description=false`);
    const closed = status.category === "done" || status.category === "cancelled";
    const expected = closed ? data.items.length : data.total_count;
    if (!closed) expect(data.has_more).toBe(false);
    const column = page.locator(`[data-testid="board-column"][data-status-id="${status.id}"]`);
    const rendered = await column.locator('[data-testid="task-card"]').count();
    console.log(`${status.name}: API total=${data.total_count}; Board cards=${rendered}`);
    await expect(column.locator('[data-testid="task-card"]')).toHaveCount(expected, { timeout: 15000 });
    await expect(column.locator("..").getByTestId("column-count")).toHaveText(String(data.total_count));
  }
  console.log("PASS: open columns complete, closed columns show the newest slice and the server total.");
  await context.close();
} finally {
  await browser.close();
}
