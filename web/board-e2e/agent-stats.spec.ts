// mock: external boundary — analytics and dashboard HTTP APIs. The actual router, controls, panels, clipboard,
// CSV download and dashboard persistence paths run unchanged. Backend visibility is covered by U3/U7/U8.
import { test, expect, type Page, type Route } from "@playwright/test";
import { readFileSync } from "node:fs";
import { prodQuotaFixture, prodUsageFixture, usageFixture, quotaFixture } from "../src/core/agent-usage/__tests__/fixtures";
import type { UsageResponse } from "../src/core/agent-usage/types";

const START = "/w/board-fixture/stats?m=cost&g=model&p=7d";
const cors = { "Access-Control-Allow-Origin": "http://127.0.0.1:3019", "Access-Control-Allow-Credentials": "true" };
const json = (r: Route, body: unknown, status = 200) => r.fulfill({ status, headers: cors, contentType: "application/json", body: JSON.stringify(body) });

async function stand(page: Page, member = false) {
  const requests: URL[] = [];
  let fail = false, empty = false;
  let doc = { id: "stats-home", name: "Statistics home", placement: "top", position: 0, version: 1, can_edit: true, widgets: [] as Record<string, unknown>[] };
  let reads = 0;
  await page.route("**/api/v1/workspaces/*/dashboards", (r) => json(r, { dashboards: [doc] }));
  await page.route("**/api/v1/workspaces/*/dashboards/stats-home", (r) => { reads++; return json(r, doc); });
  await page.route("**/api/v1/workspaces/*/dashboards/stats-home/layout", (r) => {
    const body = r.request().postDataJSON();
    expect(body.version).toBe(doc.version);
    doc = { ...doc, version: doc.version + 1, widgets: body.widgets.map((w: Record<string, unknown>, i: number) => ({ ...w, id: `saved-${i}` })) };
    return json(r, doc);
  });
  await page.route("**/api/v1/workspaces/*/analytics/usage?**", async (r) => {
    const q = new URL(r.request().url()); requests.push(q);
    if (fail) return json(r, { message: "Fixture analytics unavailable" }, 503);
    if (q.searchParams.get("format") === "csv") return r.fulfill({ headers: { ...cors, "Content-Disposition": 'attachment; filename="usage.csv"' }, contentType: "text/csv", body: "model,value,turns\nOpus,30,3\nUnknown model,,2\nTotal,30,5\n" });
    const d = usageFixture();
    d.query.metric = (q.searchParams.get("metric") ?? "cost") as UsageResponse["query"]["metric"];
    d.query.group_by = (q.searchParams.get("group_by") ?? "model") as UsageResponse["query"]["group_by"];
    d.query.stack_by = q.searchParams.get("stack_by") as UsageResponse["query"]["stack_by"];
    if (member) d.capabilities = { ...d.capabilities, quota: false, group_by: ["model", "agent", "project", "provider"] };
    if (d.query.group_by === "account") { d.rows[0]!.key = "pool/team"; d.rows[0]!.label = "Pool team"; d.rows[1]!.label = "Unattributed"; }
    if (d.query.stack_by === "project") {
      d.rows[0] = { ...d.rows[0]!, stack_key: "c6e35032-36d5-4045-b30d-6cf9e35c3dee", stack_label: "Mesh — project analytics with a long readable name" };
      d.rows[1] = { ...d.rows[1]!, stack_key: "unknown-project" };
    }
    if (q.searchParams.has("from") || q.searchParams.get("period") === "1d") {
      d.query.step = "hour";
      d.buckets = Array.from({ length: 24 }, (_, h) => `2026-11-13T${String(h).padStart(2, "0")}:00:00+03:00`);
      d.series = [{ key: "opus", label: "Opus", values: Array.from({ length: 24 }, () => 1) }];
      d.coverage = { by_bucket: Array.from({ length: 24 }, () => ({ turns: 2, turns_known: 1 })) };
    }
    if (empty) { d.rows = []; d.series = []; d.totals = { value: null, turns: 0, turns_unknown: 0 }; }
    return json(r, d);
  });
  await page.route("**/api/v1/me/analytics/usage?**", (r) => {
    const q = new URL(r.request().url()); requests.push(q);
    const d = usageFixture();
    d.query = { ...d.query, metric: "tokens", group_by: "workspace" };
    d.rows = [
      { key: "ws-a", label: "Alpha workspace", value: 100, turns: 1, turns_unknown: 0, tokens_in: 80, tokens_out: 20, link: { kind: "workspace", id: "ws-a" } },
      { key: "ws-b", label: "Beta workspace", value: 200, turns: 1, turns_unknown: 0, tokens_in: 170, tokens_out: 30, link: { kind: "workspace", id: "ws-b" } },
    ];
    d.series = [{ key: "ws-a", label: "Alpha workspace", values: [40, 60] }, { key: "ws-b", label: "Beta workspace", values: [80, 120] }];
    d.totals = { value: 300, turns: 2, turns_unknown: 0, tokens_in: 250, tokens_out: 50 };
    d.capabilities = { ...d.capabilities, group_by: [...d.capabilities.group_by, "workspace"], quota: false };
    return json(r, d);
  });
  await page.route("**/api/v1/workspaces/*/analytics/quota?**", (r) => {
    const q = new URL(r.request().url()); requests.push(q);
    return json(r, quotaFixture());
  });
  return { requests, fail: (v: boolean) => { fail = v; }, empty: (v: boolean) => { empty = v; }, saved: () => doc, reads: () => reads };
}

async function select(page: Page, testId: string, value: string) {
  await page.getByTestId(testId).filter({ visible: true }).selectOption(value);
}
async function view(page: Page, width: number, value: string, name: string) {
  if (width === 1440) await page.getByRole("radio", { name, exact: true }).click();
  else await select(page, "view-select", value);
}
async function noOverflow(page: Page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(await page.evaluate(() => innerWidth));
}

for (const width of [1440, 393]) test.describe(`agent statistics ${width}`, () => {
  test.use({ viewport: { width, height: 900 }, locale: "en-US", permissions: ["clipboard-read", "clipboard-write"] });

  test("TokenLedger manifest icon and sidebar navigation", async ({ page }, info) => {
    await stand(page);
    await page.route(/\/api\/v1\/workspaces\/[^/]+\/extensions(\?|$)/, (r) => json(r, {
      extensions: [{
        id: "tokenledger", name: "TokenLedger", status: { enabled: true },
        manifest: { runtime: "external", capabilities: { ui: ["sidebar.item"] },
          external: { ui: { "sidebar.item": { path: "/ui/", title: "TokenLedger", icon: "ChartLine" } } } },
      }],
    }));
    await page.route("**/api/v1/ext/tokenledger/ui-context", (r) => {
      if (r.request().method() === "OPTIONS") return r.fulfill({ status: 204, headers: cors });
      expect(r.request().postDataJSON()).toMatchObject({ slot: "sidebar.item" });
      return json(r, { token: "fixture-context", expires_at: new Date(Date.now() + 60_000).toISOString(),
        frame_url: "http://127.0.0.1:8029/ui/", origin: "http://127.0.0.1:8029", title: "TokenLedger" });
    });
    await page.route("http://127.0.0.1:8029/**", (r) => r.fulfill({
      contentType: "text/html", body: "<!doctype html><html><body><h1>TokenLedger screen</h1></body></html>",
    }));
    await page.goto(START);
    if (width === 393) await page.getByLabel("Toggle sidebar", { exact: true }).click();
    const link = page.getByTestId("app-sidebar").getByRole("link", { name: "TokenLedger", exact: true });
    await expect(link).toBeVisible();
    await expect(link.locator("svg")).toHaveClass(/lucide-chart-line/);
    await link.click();
    await expect(page).toHaveURL(/\/w\/board-fixture\/x\/tokenledger$/);
    await expect(page.frameLocator('iframe[title="TokenLedger"]').getByRole("heading", { name: "TokenLedger screen" })).toBeVisible();
    if (width === 393) await page.getByLabel("Toggle sidebar", { exact: true }).click();
    await expect(link).toBeVisible();
    await expect(link).toHaveClass(/bg-sidebar-accent/);
    await noOverflow(page);
    await info.attach(`tokenledger-sidebar-${width}`, { body: await page.screenshot(), contentType: "image/png" });
  });

  test("cost, breakdown, row chip, price tooltip, shared link, CSV and saved widget", async ({ page }, info) => {
    const net = await stand(page);
    await page.goto(START);
    const total = page.getByTestId(width === 1440 ? "usage-total-row" : "sticky-total");
    await expect(total).toContainText("$30.00");
    await page.locator("button[data-bucket]").first().hover();
    await expect(page.getByRole("tooltip")).toContainText("API price as of");
    await expect(page.getByRole("tooltip")).toContainText("2026");
    await expect(page.getByRole("tooltip")).toContainText("unknown-model: no price");
    await page.mouse.move(0, 0);
    await noOverflow(page);
    await info.attach(`cost-${width}`, { body: await page.screenshot({ fullPage: true }), contentType: "image/png" });
    const download = page.waitForEvent("download");
    await page.getByTestId("download-csv").click();
    const csv = await download;
    expect(readFileSync((await csv.path())!, "utf8")).toBe("model,value,turns\nOpus,30,3\nUnknown model,,2\nTotal,30,5\n");
    expect(net.requests.some((q) => q.searchParams.get("format") === "csv" && q.searchParams.get("metric") === "cost")).toBe(true);
    await select(page, "group-select", "account");
    const row = width === 1440 ? page.locator('tr[data-row="pool/team"]') : page.getByTestId("usage-card").filter({ hasText: "Pool team" });
    await expect(row).toContainText("Pool team");
    if (width === 1440) await row.getByRole("button", { name: "Pool team", exact: true }).click();
    else { await row.getByRole("button").first().click(); await page.getByRole("menuitem", { name: "Only this", exact: true }).click(); }
    await expect(page.getByTestId("filter-chip").filter({ visible: true })).toContainText("pool/team");
    await page.getByTestId("copy-link").click();
    const link = await page.evaluate(() => navigator.clipboard.readText());
    expect(new URL(link).searchParams.get("f.account")).toBe("pool/team");
    await page.goto(link);
    await expect(page.getByTestId("filter-chip").filter({ visible: true })).toContainText("pool/team");
    await expect(total).toContainText("$30.00");
    await total.scrollIntoViewIfNeeded(); await expect(total).toBeVisible(); await noOverflow(page);
    await page.getByTestId("add-to-dashboard").click();
    await expect.poll(() => net.saved().widgets.length).toBe(1);
    const settings = net.saved().widgets[0]!.settings;
    expect(settings).toMatchObject({ m: "cost", g: "account", p: "7d", "f.account": ["pool/team"] });
    const before = net.reads();
    await page.goto("/w/board-fixture/d/stats-home");
    await expect(page.getByTestId("agent-usage-card")).toBeVisible();
    await expect.poll(() => net.reads()).toBeGreaterThan(before);
    await expect(page.getByTestId("agent-usage-widget")).toContainText("Pool team");
    const requestsBeforeReload = net.requests.length;
    const readsBeforeReload = net.reads();
    await page.reload();
    await expect.poll(() => net.reads()).toBeGreaterThan(readsBeforeReload);
    await expect(page.getByTestId("agent-usage-widget")).toContainText("Pool team");
    await expect.poll(() => net.requests.slice(requestsBeforeReload).some((q) =>
      q.searchParams.get("metric") === "cost" && q.searchParams.get("group_by") === "account"
      && q.searchParams.get("period") === "7d" && q.searchParams.get("account") === "pool/team"
    )).toBe(true);
  });

  test("saved pool widget shows project names and unknown-name fallback after reload", async ({ page }, info) => {
    const net = await stand(page);
    await page.goto("/w/board-fixture/stats?m=cost&g=account&s=project&p=7d");
    const projectName = "Mesh — project analytics with a long readable name";
    const table = page.getByTestId("usage-table");
    await expect(table.getByText(projectName).filter({ visible: true })).toBeVisible();
    await expect(table.getByText("unknown-project").filter({ visible: true })).toBeVisible();
    await expect(table).not.toContainText("c6e35032-36d5-4045-b30d-6cf9e35c3dee");
    await page.getByTestId("add-to-dashboard").click();
    await expect.poll(() => net.saved().widgets.length).toBe(1);
    expect(net.saved().widgets[0]!.settings).toMatchObject({ g: "account", s: "project" });
    await page.goto("/w/board-fixture/d/stats-home");
    await page.reload();
    const widget = page.getByTestId("agent-usage-widget");
    await expect(widget.getByText(projectName).filter({ visible: true })).toBeVisible();
    await expect(widget.getByText("unknown-project").filter({ visible: true })).toBeVisible();
    await expect(widget).not.toContainText("c6e35032-36d5-4045-b30d-6cf9e35c3dee");
    expect(net.requests.some((q) => q.searchParams.get("group_by") === "account" && q.searchParams.get("stack_by") === "project")).toBe(true);
    await noOverflow(page);
    await info.attach(`stacked-project-widget-${width}`, { body: await page.screenshot({ fullPage: true }), contentType: "image/png" });
  });

  test("bucket drilldown, hourly day and cache tokens stacked by agent", async ({ page }) => {
    const net = await stand(page); await page.goto(START);
    await page.locator("button[data-bucket]").last().click();
    await expect(page.locator("button[data-bucket]")).toHaveCount(24);
    expect(net.requests.some((q) => q.searchParams.get("from") === "2026-11-13")).toBe(true);
    await view(page, width, "tokens", "Tokens");
    await select(page, "tokens-select", "cache_read"); await select(page, "stack-select", "agent");
    await expect.poll(() => net.requests.some((q) => q.searchParams.get("metric") === "tokens" && q.searchParams.get("tokens") === "cache_read" && q.searchParams.get("stack_by") === "agent")).toBe(true);
    await noOverflow(page);
  });

  test("quota lines, missing source and who spent the same period", async ({ page }, info) => {
    const net = await stand(page); await page.goto(START);
    await view(page, width, "quota", "Subscription limits");
    await expect(page.getByTestId("quota-near")).toHaveText("1 of 1"); // M = pools with the weekly window: kimi/alex has none (Doc §3.2 v4)
    await expect(page.getByText("No weekly window; monthly 33%", { exact: true }).filter({ visible: true })).toBeVisible();
    await expect(page.locator('svg[aria-label]')).toBeVisible();
    await noOverflow(page);
    await info.attach(`limits-${width}`, { body: await page.screenshot({ fullPage: true }), contentType: "image/png" });
    if (width === 1440) await page.locator('tr[data-row="claude/bob-team"]').getByRole("button", { name: "Who spent it" }).click();
    else { await page.getByTestId("quota-card").filter({ hasText: "claude/bob-team" }).getByRole("button").first().click(); await page.getByRole("menuitem", { name: "Who spent it" }).click(); }
    await expect.poll(() => net.requests.some((q) => q.searchParams.get("metric") === "tokens" && q.searchParams.get("group_by") === "agent" && q.searchParams.get("account") === "claude/bob-team" && q.searchParams.get("period") === "7d")).toBe(true);
    await expect(page.getByTestId("filter-chip").filter({ visible: true })).toContainText("claude/bob-team");
  });

  test("names keep their width, the narrow total sits on the screen edge, settings use the standard select", async ({ page }) => {
    await stand(page); await page.goto(START);
    await expect(page.getByTestId(width === 1440 ? "usage-total-row" : "sticky-total")).toContainText("$30.00");
    // Every settings select is the standard Select (no system arrow), in both layouts.
    const appearances = await page.locator("[data-testid=stats-controls] select, [data-testid=stats-chips] select").evaluateAll((els) => els.map((e) => getComputedStyle(e).appearance));
    expect(appearances.length).toBeGreaterThan(0);
    expect(new Set(appearances)).toEqual(new Set(["none"]));
    if (width === 1440) {
      const table = (await page.getByTestId("usage-table").locator("table").boundingBox())!;
      const name = (await page.getByTestId("usage-table").locator("thead th").first().boundingBox())!;
      expect(name.width / table.width).toBeGreaterThan(0.2);
    } else {
      // A short window so the page scrolls even on the spec's small fixture: the total sits on the screen
      // edge both at the top and at the very end of the page, with nothing under it.
      await page.setViewportSize({ width, height: 560 });
      const edge = () => page.evaluate(() => {
        const main = document.querySelector("main")!;
        const bar = document.querySelector("[data-testid=sticky-total]")!.getBoundingClientRect();
        return { scrolls: main.scrollHeight > main.clientHeight, gap: Math.abs(innerHeight - bar.bottom) };
      });
      await expect.poll(async () => (await edge()).gap).toBeLessThan(1); // sub-pixel layout: 559.5 of 560 is flush
      expect((await edge()).scrolls).toBe(true);
      await page.evaluate(() => { const m = document.querySelector("main")!; m.scrollTop = m.scrollHeight; });
      await expect.poll(async () => (await edge()).gap).toBeLessThan(1); // sub-pixel layout: 559.5 of 560 is flush
    }
  });

  test("member hides admin controls, errors retry and empty periods have a way out", async ({ page }) => {
    const net = await stand(page, true); net.fail(true); await page.goto(START);
    await expect(page.getByRole("alert").filter({ hasText: "Fixture analytics unavailable" })).toBeVisible();
    net.fail(false); await page.getByRole("button", { name: "Retry", exact: true }).click();
    await expect(page.getByTestId("coverage-strip")).toBeVisible();
    await expect(page.getByRole("radio", { name: "Subscription limits" })).toHaveCount(0);
    expect(await page.locator('option[value="quota"],option[value="account"]').count()).toBe(0);
    net.empty(true); await page.goto(START.replace("p=7d", "p=30d"));
    await expect(page.getByTestId("stats-empty")).toBeVisible(); await noOverflow(page);
  });

  test("workspace breakdown uses /me and persists in the user's dashboard", async ({ page }, info) => {
    const net = await stand(page);
    await page.goto("/w/board-fixture/stats?m=tokens&g=workspace&p=7d");
    await expect(page.getByTestId("usage-table")).toContainText("Alpha workspace");
    await expect(page.getByTestId("usage-table")).toContainText("Beta workspace");
    expect(net.requests.some((q) => q.pathname === "/api/v1/me/analytics/usage" && q.searchParams.get("group_by") === "workspace")).toBe(true);
    await noOverflow(page);
    await info.attach(`workspace-${width}`, { body: await page.screenshot({ fullPage: true }), contentType: "image/png" });
    await page.getByTestId("add-to-dashboard").click();
    await expect.poll(() => net.saved().widgets.length).toBe(1);
    expect(net.saved().widgets[0]!.settings).toMatchObject({ m: "tokens", g: "workspace" });
    await page.goto("/w/board-fixture/d/stats-home");
    await expect(page.getByTestId("agent-usage-widget")).toContainText("Alpha workspace");
    await expect(page.getByTestId("agent-usage-widget")).toContainText("Beta workspace");
    await noOverflow(page);
  });
});

/** Prod-shaped answers: eight models folded to `top` with an uncounted `__other__`, a rolling limits period. */
async function ruStand(page: Page) {
  const requests: URL[] = [];
  await page.route("**/api/v1/workspaces/*/analytics/usage?**", (r) => {
    const q = new URL(r.request().url()); requests.push(q);
    const d = prodUsageFixture(Number(q.searchParams.get("top") ?? 5));
    d.query.metric = (q.searchParams.get("metric") ?? "cost") as UsageResponse["query"]["metric"];
    d.query.group_by = (q.searchParams.get("group_by") ?? "model") as UsageResponse["query"]["group_by"];
    return json(r, d);
  });
  await page.route("**/api/v1/workspaces/*/analytics/quota?**", (r) => { requests.push(new URL(r.request().url())); return json(r, prodQuotaFixture()); });
  return requests;
}

for (const width of [1440, 393]) test.describe(`agent statistics against the mockup, ru ${width}`, () => {
  test.use({ viewport: { width, height: 900 }, locale: "ru-RU", timezoneId: "Europe/Moscow" });

  test("«≈ $»: sort caption, «Остальные» with «Раскрыть», «Таблица целиком», updated stamp", async ({ page }, info) => {
    const net = await ruStand(page);
    await page.goto(START);
    if (width === 1440) {
      await expect(page.getByTestId("usage-sort-caption")).toHaveText("Сортировка: стоимость. Показаны 5 из 8, «Остальные» ниже");
      await expect(page.locator('tr[data-row="__other__"]')).toContainText("Остальные (3)");
      await expect(page.getByTestId("usage-updated")).toHaveText(/^Обновлено \d\d:\d\d МСК$/);
    } else {
      await expect(page.getByTestId("usage-sort-short")).toHaveText("по стоимости");
      await expect(page.getByTestId("usage-card").filter({ hasText: "Остальные (3)" })).toBeVisible();
      await expect(page.getByTestId("sticky-total").getByRole("button", { name: "Таблица целиком" })).toBeVisible();
    }
    await noOverflow(page);
    await info.attach(`ru-cost-${width}`, { body: await page.screenshot({ fullPage: true }), contentType: "image/png" });
    if (width === 1440) await page.locator('tr[data-row="__other__"]').getByRole("button", { name: "Раскрыть" }).click();
    else await page.getByTestId("sticky-total").getByRole("button", { name: "Таблица целиком" }).click();
    await expect(page).toHaveURL(/[?&]n=20(&|$)/);
    await expect(page.locator(width === 1440 ? "tbody tr[data-row]" : "[data-testid=usage-card]")).toHaveCount(8);
    await expect(page.locator('tr[data-row="__other__"]')).toHaveCount(0);
    expect(net.some((q) => q.searchParams.get("top") === "20")).toBe(true);
    await noOverflow(page);
  });

  test("«Токены» by agent: the coverage caption is whole, «Other» is «Остальные»", async ({ page }, info) => {
    await ruStand(page);
    await page.goto("/w/board-fixture/stats?m=tokens&g=agent&p=7d");
    const label = page.getByTestId("coverage-label");
    await expect(label).toHaveText("известны");
    expect(await label.evaluate((el) => el.scrollWidth <= el.clientWidth && getComputedStyle(el).textOverflow !== "ellipsis")).toBe(true);
    expect((await label.boundingBox())!.x).toBeGreaterThanOrEqual(0);
    if (width === 1440) await expect(page.locator('tr[data-row="__other__"] td').first()).toHaveText("Остальные (3)");
    else await expect(page.getByTestId("usage-card").filter({ hasText: "Остальные (3)" })).toBeVisible();
    await expect(page.getByText("Other", { exact: true })).toHaveCount(0);
    await noOverflow(page);
    await info.attach(`ru-tokens-${width}`, { body: await page.screenshot({ fullPage: true }), contentType: "image/png" });
  });

  test("«Лимиты подписок»: Russian reasons, a caption for the day step, the axis from the period's first day", async ({ page }, info) => {
    await ruStand(page);
    await page.goto("/w/board-fixture/stats?m=quota&p=7d&w=week");
    await expect(page.getByText(/^нет окна «неделя»: 5 ч — 9\s%, месяц — 13\s%$/).filter({ visible: true })).toBeVisible();
    await expect(page.getByText("источник лимита не отдал данные", { exact: true }).filter({ visible: true }).first()).toBeVisible();
    await expect(page.getByTestId("quota-legend")).toContainText("за день");
    await expect(page.getByTestId("quota-legend")).not.toContainText("на конец часа");
    // 1440 reads «14 сб … 20 пт» as its mockup; 393 keeps the day numbers. «Сейчас» is a vertical line at both.
    const days = (await page.locator("[data-testid=quota-chart] svg > text").allTextContents()).filter((s) => /^\d{1,2}( |$)/.test(s));
    expect(days.length).toBeGreaterThan(3);
    for (const d of days) expect(d).toMatch(width === 1440 ? /^\d{1,2} \p{L}{2}$/u : /^\d{1,2}$/);
    expect(days[0]).toMatch(/^14( |$)/);
    expect(days.some((d) => /^13( |$)/.test(d))).toBe(false);
    const now = page.locator("[data-testid=quota-chart] [data-testid=quota-now]");
    await expect(now.locator("line")).toHaveCount(1);
    await expect(now.locator("text")).toHaveText("сейчас");
    await noOverflow(page);
    await info.attach(`ru-limits-${width}`, { body: await page.screenshot({ fullPage: true }), contentType: "image/png" });
  });
});
