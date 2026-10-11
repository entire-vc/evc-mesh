// Read-only production acceptance. Uses a NEW browser context and native login,
// never a copied refresh cookie. Logs only counts and task-list metadata.
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
  await page.addInitScript((id) => localStorage.setItem(`mesh_board_filters_${id}`, JSON.stringify({ showClosed: true, showSubtasks: true })), project);
  await page.goto(`${base}/w/ws-006901c7/p/contenthub`);
  await page.getByRole("button", { name: "Closed", exact: true }).waitFor();
  const tasks = [];
  let revision, total;
  for (let n = 1; ; n++) {
    const data = await api(`/projects/${project}/tasks?page_size=200&include_description=false&page=${n}${n > 1 ? `&list_revision=${revision}` : ""}`);
    revision ??= data.list_revision; total ??= data.total_count;
    expect(data.list_revision).toBe(revision); expect(data.total_count).toBe(total);
    tasks.push(...data.items);
    if (!data.has_more) break;
  }
  await api(`/projects/${project}/tasks?page_size=1&include_description=false&list_revision=${revision}`);
  expect(new Set(tasks.map((task) => task.id)).size).toBe(total);
  console.log(`Full API: ${total} unique tasks; list_revision=${revision}`);
  for (const status of statuses) {
    const expected = tasks.filter((task) => task.status_id === status.id).length;
    const column = page.locator(`[data-testid="board-column"][data-status-id="${status.id}"]`);
    const rendered = await column.locator('[data-testid="task-card"]').count();
    console.log(`${status.name}: API=${expected}; Board=${rendered}`);
    await expect(column.locator('[data-testid="task-card"]')).toHaveCount(expected, { timeout: 15000 });
    await expect(column.locator("..").getByText(String(expected), { exact: true })).toBeVisible();
  }
  console.log("PASS: live Board counts and all cards match the full API with Closed/Subtasks enabled.");
  await context.close();
} finally {
  await browser.close();
}
