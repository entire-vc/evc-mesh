#!/usr/bin/env node
// Exercise the production chunk graph through a real browser. A supplied dist
// replaces frontend files only; authentication and workspace discovery use API.
import { chromium } from "@playwright/test";
import { readFile, mkdir } from "node:fs/promises";
import { resolve, extname } from "node:path";
import assert from "node:assert/strict";

const base = process.env.SESSION_PROBE_URL ?? "https://mesh.entire.host";
const dist = process.env.SESSION_PROBE_DIST;
const output = process.env.SESSION_PROBE_OUTPUT;
const browser = await chromium.launch({ channel: process.env.SESSION_PROBE_CHANNEL ?? "chrome" });
try {
  const context = await browser.newContext({ serviceWorkers: "block" });
  const login = await context.request.post(`${base}/api/v1/auth/login`, {
    data: { email: process.env.SESSION_PROBE_EMAIL ?? "hugh@entire.vc", password: process.env.SESSION_PROBE_PASSWORD },
  });
  assert(login.ok(), `login failed: ${login.status()}`);
  const token = (await login.json()).tokens.access_token;
  const workspaces = await context.request.get(`${base}/api/v1/workspaces`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  assert(workspaces.ok(), `workspace discovery failed: ${workspaces.status()}`);
  const body = await workspaces.json();
  const workspace = (Array.isArray(body) ? body : body.items)[0];
  assert(workspace?.slug, "no accessible workspace");
  const status = "Deploy drift check: the oldest undeployed commit carries migrations and must ship before the watchdog pages the on-call again. FULL STATUS END";
  const title = "Sessions: full status and task title remain reachable after mobile tap, including this tail. FULL TITLE END";
  if (dist) {
    const root = resolve(dist);
    await context.route(`${base}/**`, async (route) => {
      const path = new URL(route.request().url()).pathname;
      if (path.startsWith("/api/")) return route.continue();
      const file = resolve(root, `.${path.startsWith("/assets/") ? path : "/index.html"}`);
      assert(file.startsWith(`${root}/`), "asset path escapes dist");
      const contentType = { ".js": "text/javascript", ".css": "text/css", ".html": "text/html" }[extname(file)];
      await route.fulfill({ body: await readFile(file), contentType });
    });
  }
  // A deterministic long-text fixture avoids depending on current heartbeats.
  // No server data is written, and the normal page polling/data flow still runs.
  await context.route("**/agents/status", (route) => route.fulfill({ json: {
    agents: [{ id: "bundle-probe", name: "Bundle probe", status: "busy", agent_type: "claude",
      is_stale: false, last_heartbeat_at: null, seconds_since_heartbeat: null,
      heartbeat_message: status, current_task_id: "probe-task", current_task_title: title }],
    stale_count: 0, working_count: 1, total_count: 1,
  } }));
  await context.route("**/analytics?*", (route) => route.fulfill({ json: { cost_metrics: {
    total_cost: 0, total_tokens_in: 0, total_tokens_out: 0,
    session_count: 0, reported_session_count: 0, by_agent: [], top_tasks: [],
  } } }));
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  for (const width of [1440, 393]) {
    await page.setViewportSize({ width, height: width === 393 ? 852 : 900 });
    await page.goto(`${base}/w/${workspace.slug}/sessions`);
    const card = page.locator(".mesh-mobile-sessions");
    await card.getByText("Bundle probe", { exact: true }).waitFor();
    await card.getByText("No cost data reported yet for this period.", { exact: false }).waitFor();
    assert(await card.locator("svg").count() > 0, "session icons missing");
    for (const name of width === 393 ? [status, title] : [status]) {
      const button = card.getByRole("button", { name, exact: true });
      assert.equal(await button.getAttribute("aria-expanded"), "false");
      await button.click();
      assert.equal(await button.getAttribute("aria-expanded"), "true");
      assert.equal(await button.locator(".line-clamp-2").count(), 0);
    }
    if (width === 1440) {
      assert.equal(await card.getByRole("link", { name: title, exact: true }).getAttribute("href"), "/t/probe-task");
    }
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "horizontal overflow");
    if (output) {
      await mkdir(output, { recursive: true });
      await page.screenshot({ path: `${output}/sessions-${width}.png`, fullPage: true });
    }
    console.log(`PASS Sessions ${width}: icons, status/title expansion, no overflow`);
  }
  assert.deepEqual(errors, [], "browser runtime errors");
  console.log("SESSION-BUNDLE: PASS");
} finally {
  await browser.close();
}
