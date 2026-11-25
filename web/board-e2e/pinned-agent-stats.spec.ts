// mock: external boundary — the analytics HTTP API (the loopback fixture serves the workspace, project and agent).
// U5: the statistics widget pinned to a project's «Statistics» tab and to an agent's page. Every usage read carries the
// pinned id, «Subscription limits» is never asked, «Open in statistics» lands on /stats with the same filter.
import { test, expect, type Page, type Route } from "@playwright/test";
import { usageFixture } from "../src/core/agent-usage/__tests__/fixtures";
import type { UsageResponse } from "../src/core/agent-usage/types";

const API = "http://127.0.0.1:8019";
const PROJECT = { id: "pagination-project", name: "ContentHub fixture", url: "/w/board-fixture/p/contenthub/stats" };
const AGENT = { id: "exec-agent", name: "Vera", url: "/w/board-fixture/team/agent/vera?tab=stats" };
const cors = { "Access-Control-Allow-Origin": "http://127.0.0.1:3019", "Access-Control-Allow-Credentials": "true" };
const json = (r: Route, body: unknown, status = 200) => r.fulfill({ status, headers: cors, contentType: "application/json", body: JSON.stringify(body) });

async function stand(page: Page) {
  // exec: the fixture's agent Vera exists in the team, the agent list and the org chart.
  await page.request.post(`${API}/__fixture`, { data: { mode: "normal", role: "owner", exec: "main" } });
  const usage: URL[] = [];
  const quota: URL[] = [];
  await page.route("**/api/v1/workspaces/*/analytics/usage?**", (r) => {
    const q = new URL(r.request().url()); usage.push(q);
    const d = usageFixture();
    d.query.metric = (q.searchParams.get("metric") ?? "cost") as UsageResponse["query"]["metric"];
    d.query.group_by = (q.searchParams.get("group_by") ?? "model") as UsageResponse["query"]["group_by"];
    if (d.query.group_by === "agent") { d.rows[0] = { ...d.rows[0]!, key: AGENT.id, label: AGENT.name, link: { kind: "agent", id: AGENT.id } }; }
    if (d.query.group_by === "project") { d.rows[0] = { ...d.rows[0]!, key: PROJECT.id, label: PROJECT.name, link: { kind: "project", id: PROJECT.id } }; }
    return json(r, d);
  });
  await page.route("**/api/v1/workspaces/*/analytics/quota?**", (r) => { quota.push(new URL(r.request().url())); return json(r, {}); });
  return { usage, quota };
}

async function noOverflow(page: Page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(await page.evaluate(() => innerWidth));
}

for (const width of [1440, 393]) test.describe(`pinned agent statistics ${width}`, () => {
  test.use({ viewport: { width, height: 900 }, locale: "en-US" });

  test("project tab shows only this project and opens /stats with its filter", async ({ page }, info) => {
    const net = await stand(page);
    await page.goto(PROJECT.url);
    await expect(page.getByRole("heading", { level: 1 })).toHaveText(PROJECT.name);
    // The tab is active in the strip, or (a narrow strip) the «…» trigger carries it.
    await expect(page.locator(".mesh-project-tabs").getByRole("button", { name: /^Statistics/ })).toBeVisible();
    const widget = page.getByTestId("pinned-agent-usage");
    await expect(widget).toContainText("$30.00");
    await expect(widget).toContainText(AGENT.name);
    expect(net.usage.length).toBeGreaterThan(0);
    // The first read may be the permissions probe (tokens by model): it carries the pin too.
    for (const q of net.usage) expect(q.searchParams.getAll("project_id")).toEqual([PROJECT.id]);
    const costReads = net.usage.filter((q) => q.searchParams.get("metric") === "cost");
    expect(costReads.length).toBeGreaterThan(0);
    for (const q of costReads) expect(q.searchParams.get("group_by")).toBe("agent");
    expect(net.quota).toHaveLength(0);
    await expect(widget.getByText("Subscription limits")).toHaveCount(0);
    await noOverflow(page);
    await info.attach(`project-${width}`, { body: await page.screenshot({ fullPage: true }), contentType: "image/png" });

    const before = net.usage.length;
    await widget.getByRole("button", { name: "Open in statistics" }).click();
    await expect(page).toHaveURL(/\/w\/board-fixture\/stats\?/);
    expect(new URL(page.url()).searchParams.getAll("f.project")).toEqual([PROJECT.id]);
    await expect(page.getByTestId("filter-chip").filter({ visible: true })).toContainText(PROJECT.name);
    await expect(page.getByTestId(width === 1440 ? "usage-total-row" : "sticky-total")).toContainText("$30.00");
    // The same slice may come from the read cache; any read the page does make keeps the filter.
    expect(net.usage.slice(before).every((q) => q.searchParams.getAll("project_id").join() === PROJECT.id)).toBe(true);
    await noOverflow(page);
    // A cold load of the same URL (no read cache) must ask the server for this project only.
    const cold = net.usage.length;
    await page.reload();
    await expect.poll(() => net.usage.length).toBeGreaterThan(cold);
    for (const q of net.usage.slice(cold)) expect(q.searchParams.getAll("project_id")).toEqual([PROJECT.id]);
    await expect(page.getByTestId("filter-chip").filter({ visible: true })).toContainText(PROJECT.name);
  });

  test("agent page shows only this agent and opens /stats with its filter", async ({ page }, info) => {
    const net = await stand(page);
    await page.goto(AGENT.url);
    await expect(page.getByRole("tab", { name: "Statistics" })).toHaveAttribute("aria-selected", "true");
    const widget = page.getByTestId("pinned-agent-usage");
    await expect(widget).toContainText("$30.00");
    await expect(widget).toContainText(PROJECT.name);
    expect(net.usage.length).toBeGreaterThan(0);
    for (const q of net.usage) {
      expect(q.searchParams.getAll("agent_id")).toEqual([AGENT.id]);
      expect(q.searchParams.get("project_id")).toBeNull();
    }
    const costReads = net.usage.filter((q) => q.searchParams.get("metric") === "cost");
    expect(costReads.length).toBeGreaterThan(0);
    for (const q of costReads) expect(q.searchParams.get("group_by")).toBe("project");
    expect(net.quota).toHaveLength(0);
    await noOverflow(page);
    // The dialog body scrolls: the breakdown table and its total are reachable below the chart.
    const table = widget.getByTestId("usage-table");
    await table.scrollIntoViewIfNeeded();
    await expect(table).toBeInViewport();
    await expect(table).toContainText(PROJECT.name);
    await info.attach(`agent-${width}`, { body: await page.screenshot({ fullPage: true }), contentType: "image/png" });

    const before = net.usage.length;
    await widget.getByRole("button", { name: "Open in statistics" }).click();
    await expect(page).toHaveURL(/\/w\/board-fixture\/stats\?/);
    expect(new URL(page.url()).searchParams.getAll("f.agent")).toEqual([AGENT.id]);
    await expect(page.getByTestId("filter-chip").filter({ visible: true })).toContainText(AGENT.name);
    await expect(page.getByTestId(width === 1440 ? "usage-total-row" : "sticky-total")).toContainText("$30.00");
    expect(net.usage.slice(before).every((q) => q.searchParams.getAll("agent_id").join() === AGENT.id)).toBe(true);
    await noOverflow(page);
    // A cold load of the same URL (no read cache) must ask the server for this agent only.
    const cold = net.usage.length;
    await page.reload();
    await expect.poll(() => net.usage.length).toBeGreaterThan(cold);
    for (const q of net.usage.slice(cold)) expect(q.searchParams.getAll("agent_id")).toEqual([AGENT.id]);
    await expect(page.getByTestId("filter-chip").filter({ visible: true })).toContainText(AGENT.name);
  });
});
