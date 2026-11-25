import { test as base, expect, type Page } from "@playwright/test";
import { mkdirSync, readFileSync } from "node:fs";
import type { WireMemoryAnalytics } from "../src/core/dashboard/widgets/analytics/memory-wire";
import { emptyJournalPayload, emptyQuery, mainPayload, page1, record, row } from "./fixtures";

// Acceptance, on the network boundary: the harness draws the real widget on the vite dev
// server and the API answers here. The four AC checks — the agent filter leaves one row and changes the
// tiles, a period change changes the numbers, an empty journal shows «нет данных» with its reason (never
// 0), the legend holds the line item — plus the 393 layout («Фильтры · N», tiles two-up, the table as
// agent cards) and 1440/393 screenshots. Zero console errors is part of every test.

const SHOTS = "e2e-memory/shots";
mkdirSync(SHOTS, { recursive: true });

/** The one /api/v1 route: the memory endpoints answered, anything else 404s and the test sees it. */
async function routeApi(page: Page, main: (p: URLSearchParams) => WireMemoryAnalytics) {
  await page.route("**/api/v1/**", (route) => {
    const url = new URL(route.request().url());
    const p = url.searchParams;
    if (url.pathname.endsWith("/analytics/memory")) return route.fulfill({ json: main(p) });
    if (url.pathname.endsWith("/analytics/memory/export")) {
      return route.fulfill({ body: "date,reads\n", headers: { "content-type": "text/csv", "content-disposition": 'attachment; filename="memory.csv"' } });
    }
    if (url.pathname.endsWith("/analytics/memory/empty-queries")) return route.fulfill({ json: page1([emptyQuery]) });
    if (url.pathname.includes("/analytics/memory/")) return route.fulfill({ json: page1([record]) });
    return route.fulfill({ status: 404, json: { error: `unmocked ${url.pathname}` } });
  });
}

const test = base.extend<{ errors: string[] }>({
  errors: [async ({ page }, use) => {
    const errors: string[] = [];
    page.on("console", (m) => m.type() === "error" && errors.push(m.text()));
    page.on("pageerror", (e) => errors.push(String(e)));
    await use(errors);
    expect(errors.join("\n"), "console errors").toBe("");
  }, { auto: true }],
});

test.beforeEach(async ({ page }) => {
  await routeApi(page, mainPayload);
  await page.goto("/e2e-memory/memory-harness.html");
  await expect(page.getByTestId("memory-kpis")).toBeVisible();
});

test("1440: the six tiles, the legend holds the line item, both agents in the table", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await expect(page.locator('[data-kpi="useful_rate"]')).toContainText("60%");
  await expect(page.locator('[data-testid="memory-kpis"] [data-kpi]')).toHaveCount(6);
  // the legend names the operations the days used (§7: bars by op) and the line of the right axis
  await expect(page.locator('[data-testid="memory-legend"] [data-legend="useful-line"]')).toBeVisible();
  await expect(page.locator('[data-testid="memory-legend"] [data-legend]')).toHaveText(["recall", "get memory", "полезный recall, %"]);
  // the line itself is drawn (an SVG polyline reports itself hidden to Playwright; its points are the proof)
  const line = page.locator('[data-testid="memory-useful-line"]');
  await expect(line).toHaveCount(1);
  await expect(line).toHaveAttribute("points", /.+/);
  await expect(page.locator('[data-testid="memory-chart"] [data-point-label]')).toHaveCount(7); // a week: every point labelled
  await expect(page.locator('[data-testid="memory-table"] tr[data-row]')).toHaveCount(2);
  await expect(page.locator('[data-quality="superseded_active"]')).toContainText("8");
  await expect(page.locator('[data-quality="superseded_served_rate"]')).toContainText("5%");
  await page.screenshot({ path: `${SHOTS}/memory-1440.png`, fullPage: true });
});

test("1440: the pinned «Total» row is opaque, rows scrolling under it do not show through", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  const foot = page.locator('[data-testid="memory-table-scroll"] tfoot');
  await expect(foot).toBeVisible();
  // the footer is pinned to the bottom of the table's own scroll; a translucent fill lets the last rows read through it
  const alpha = await foot.evaluate((el) => {
    const probe = document.createElement("canvas").getContext("2d")!;
    probe.fillStyle = getComputedStyle(el).backgroundColor;
    probe.fillRect(0, 0, 1, 1);
    return probe.getImageData(0, 0, 1, 1).data[3];
  });
  expect(alpha).toBe(255);
});

test("the agent filter leaves one row and changes the tiles", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await expect(page.locator('[data-testid="memory-table"] tr[data-row]')).toHaveCount(2);
  await page.getByTestId("memory-filter-agent").click();
  await page.getByRole("menuitemcheckbox", { name: "Liam" }).click();
  await page.keyboard.press("Escape");
  await expect(page.locator('[data-testid="memory-table"] tr[data-row]')).toHaveCount(1);
  await expect(page.locator('[data-testid="memory-table"] tr[data-row="a1"]')).toBeVisible();
  await expect(page.locator('[data-kpi="useful_rate"]')).toContainText("70%"); // the tiles re-read the agent's numbers
});

test("a period change changes the numbers", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  const reads = page.locator('[data-kpi="reads"]');
  await expect(reads).toContainText("84"); // 7 days × 12
  await page.getByRole("button", { name: "30 дн." }).click();
  await expect(reads).toContainText("360"); // 30 days × 12
});

test("an empty journal is «нет данных» with its reason, never 0", async ({ page }) => {
  await routeApi(page, emptyJournalPayload); // this test's route answers first
  await page.goto("/e2e-memory/memory-harness.html");
  const reads = page.locator('[data-kpi="reads"]');
  await expect(reads).toContainText("нет данных");
  await expect(reads).toContainText("журнал чтения ещё не ведётся");
  await expect(reads).toHaveAttribute("data-nodata", "");
  for (const id of ["useful_rate", "miss_rate", "dead30", "tokens_est", "writes"]) {
    await expect(page.locator(`[data-kpi="${id}"]`)).toContainText("нет данных");
  }
  await expect(page.getByTestId("memory-journal-note")).toBeVisible();
});

test("a row click filters every section and CSV keeps that selection", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.locator('[data-testid="memory-table"] [data-row="a2"]').getByRole("button", { name: "Maya", exact: true }).click();
  await expect(page.locator('[data-testid="memory-table"] tr[data-row]')).toHaveCount(1);
  await expect(page.locator('[data-kpi="useful_rate"]')).toContainText("70%");
  const requested = page.waitForRequest((r) => new URL(r.url()).pathname.endsWith("/memory/export"));
  const downloaded = page.waitForEvent("download");
  await page.getByRole("button", { name: "CSV", exact: true }).click();
  const request = await requested;
  expect(new URL(request.url()).searchParams.get("agent_id")).toBe("a2");
  expect(new URL(request.url()).searchParams.get("caller_kind")).toBe("agent_tool");
  expect((await downloaded).suggestedFilename()).toBe("memory.csv");
});

test("393: «Фильтры · N» opens the panel, the tiles stack two-up, the table becomes agent cards", async ({ page }) => {
  await page.setViewportSize({ width: 393, height: 852 });
  const toggle = page.getByTestId("memory-filters-toggle");
  await expect(toggle).toBeVisible();
  await expect(page.getByTestId("memory-filters")).toBeHidden();
  await toggle.click();
  await expect(page.getByTestId("memory-filters")).toBeVisible();
  await toggle.click();
  await expect(page.getByTestId("memory-filters")).toBeHidden();
  // one card per agent instead of the wide table
  await expect(page.getByTestId("memory-cards")).toBeVisible();
  await expect(page.getByTestId("memory-table")).toBeHidden();
  await expect(page.locator('[data-testid="memory-cards"] [data-card]')).toHaveCount(3); // two agents + the total
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  const tiles = page.locator('[data-testid="memory-kpis"] [data-kpi]');
  const first = await tiles.nth(0).boundingBox();
  const second = await tiles.nth(1).boundingBox();
  const third = await tiles.nth(2).boundingBox();
  expect(first && second && third).toBeTruthy();
  expect(first!.y).toBe(second!.y);
  expect(third!.y).toBeGreaterThan(first!.y);
  // the chart across the frame and readable: ≥180 px tall, the line's percentages in the tooltips only
  const chart = page.getByTestId("memory-chart");
  await expect(chart).toHaveAttribute("data-narrow", "");
  const box = await chart.boundingBox();
  const frame = await page.getByTestId("memory-legend").locator("..").boundingBox();
  const section = await chart.locator("xpath=ancestor::section[1]").boundingBox();
  const widget = await page.getByTestId("memory-kpis").boundingBox();
  expect(box!.height).toBeGreaterThanOrEqual(180);
  // the drawing fills its frame, and its section spans the widget as wide as the tiles: no side column at 393
  expect(Math.abs(box!.width - frame!.width)).toBeLessThanOrEqual(1);
  expect(Math.abs(section!.width - widget!.width)).toBeLessThanOrEqual(1);
  await expect(chart.locator("[data-point-label]")).toHaveCount(0);
  await expect(chart.locator("circle title").first()).toHaveText(/полезный recall, %: \d+\s?%$/);
  await page.screenshot({ path: `${SHOTS}/memory-393.png`, fullPage: true });
});

for (const group of ["kind", "scope"]) {
  test(`${group}: only writes in every view`, async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await routeApi(page, (p) => ({
      ...mainPayload(p),
      rows: [row("fact", "fact", { reads: null, hits: null, useful: null, hit_rate: null, useful_rate: null, writes: 30, with_reason: 12, reasons: { reads: "not_applicable" } })],
      totals: row("total", "total", { reads: null, writes: 30, with_reason: 12 }),
    }));
    await page.getByTestId("memory-filter-group").filter({ visible: true }).click();
    await page.getByRole("group", { name: "Разрез", exact: true }).getByRole("menuitemradio", { name: group === "kind" ? "Kind" : "Scope", exact: true }).click();
    await expect(page.getByTestId("memory-writes-note")).toBeVisible();
    await expect(page.locator("[data-kpi]")).toHaveCount(1);
    await expect(page.locator('[data-kpi="writes"]')).toContainText("30");
    await expect(page.getByTestId("memory-chart")).toHaveCount(0);
    await expect(page.getByTestId("memory-table")).toContainText("12");
    await page.getByRole("button", { name: "Топ-N", exact: true }).click();
    await expect(page.getByTestId("memory-top")).toContainText("по записям");
    await page.getByRole("button", { name: "График", exact: true }).click();
    await expect(page.getByTestId("memory-chart")).toHaveCount(0);
    await expect(page.getByTestId("memory-table")).toBeVisible();
  });
}

// the U9 acceptance findings on prod (dashboard f4c1083e) — the detail line contradicted itself, the
// 393 tiles and agent cards cut or broke their text, Escape left a filter list open, the default preset hid the table.

const catalog = JSON.parse(readFileSync("src/core/dashboard/core-widgets.json", "utf8")) as { types: { id: string; size: { default: { h: number } } }[] };
const presetRows = catalog.types.find((w) => w.id === "memory-usage")!.size.default.h;
const agents = (n: number) => Array.from({ length: n }, (_, i) => row(i === 0 ? "a1" : i === 1 ? "a2" : `a${i + 1}`, ["Liam", "Maya", "Riker", "Verne", "Linus", "Khan"][i]!));

/** Rows of the agent table fully inside the scrolling frame without scrolling it. */
async function rowsInFrame(page: Page): Promise<{ header: boolean; rows: number }> {
  return page.evaluate(() => {
    const frame = document.querySelector('[data-testid="harness-frame"]')!.getBoundingClientRect();
    const inside = (el: Element) => { const r = el.getBoundingClientRect(); return r.height > 0 && r.top >= frame.top && r.bottom <= frame.bottom; };
    const head = document.querySelector('[data-testid="memory-table"] thead');
    return { header: !!head && inside(head), rows: [...document.querySelectorAll('[data-testid="memory-table"] tr[data-row]')].filter(inside).length };
  });
}

for (const [rows, shows] of [[presetRows, true], [8, false]] as const) {
  test(`1440: a ${rows}-row frame ${shows ? "shows" : "does not show"} the table header and ≥3 agents without scrolling`, async ({ page }) => {
    await routeApi(page, (p) => ({ ...mainPayload(p), rows: agents(6) }));
    await page.setViewportSize({ width: 1440, height: 1000 });
    // the dashboard at 1440 with the sidebar: the widget ~1150 px wide, the tiles in three columns
    await page.goto(`/e2e-memory/memory-harness.html?frame=${rows}&width=1150`);
    await expect(page.locator('[data-testid="memory-table"] tr[data-row]')).toHaveCount(6);
    const seen = await rowsInFrame(page);
    if (shows) {
      expect(seen.header).toBe(true);
      expect(seen.rows).toBeGreaterThanOrEqual(3);
      await page.screenshot({ path: `${SHOTS}/a9cdedd8-1440-default-frame.png` });
    } else {
      // control: the old 12x8 preset reproduces the finding, so the check above can fail
      expect(seen.rows).toBeLessThan(3);
    }
  });
}

test("393: the Reads split wraps whole, tile labels are not cut, an agent card keeps its reason readable", async ({ page }) => {
  await routeApi(page, (p) => ({
    ...mainPayload(p),
    rows: [row("a1", "Liam"), row("a2", "Maya", { hit_rate: null, useful_rate: null, reasons: { hit_rate: "no_data", useful_rate: "no_data" } })],
  }));
  await page.setViewportSize({ width: 393, height: 852 });
  await page.goto("/e2e-memory/memory-harness.html");
  const reads = page.locator('[data-kpi="reads"]');
  await expect(reads).toContainText("get memory");
  // text that overflows its box is cut (an ellipsis or a clip)
  const cut = (sel: string) => page.locator(sel).evaluateAll((els) => els.filter((el) => el.scrollWidth > el.clientWidth + 1).map((el) => el.textContent));
  expect(await cut('[data-kpi="reads"] p')).toEqual([]);
  expect(await cut("[data-kpi] > p:first-child")).toEqual([]);
  const split = reads.locator("p", { hasText: "get memory" });
  await expect(split).toContainText("recall 56 · get memory 28");
  expect(await split.evaluate((el) => getComputedStyle(el).textOverflow)).not.toBe("ellipsis");
  // the no-data reason in a card: a line wide enough for several words, not one word per line
  const reason = page.locator('[data-card="a2"] dd').filter({ hasText: "нет данных" }).first();
  await expect(reason).toBeVisible();
  const box = await reason.boundingBox();
  expect(box!.width).toBeGreaterThanOrEqual(120);
  expect(box!.height).toBeLessThanOrEqual(40);
  await page.screenshot({ path: `${SHOTS}/a9cdedd8-393.png`, fullPage: true });
});

test("393: Agent → tick an agent → Escape closes the list, focus is back on Agent and Reset takes the click", async ({ page }) => {
  await page.setViewportSize({ width: 393, height: 852 });
  await page.getByTestId("memory-filters-toggle").click();
  const agent = page.getByTestId("memory-filter-agent");
  await agent.click();
  await page.getByRole("menuitemcheckbox", { name: "Maya" }).click();
  await expect(page.getByRole("menuitemcheckbox").first()).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("menuitemcheckbox")).toHaveCount(0);
  await expect(agent).toBeFocused();
  await page.getByRole("button", { name: "Сбросить" }).click();
  await expect(agent).toContainText("все");
});

test("1440: «top records» before the journal names its date, never «not kept yet»", async ({ page }) => {
  await page.route("**/api/v1/**/analytics/memory/top-records*", (route) =>
    route.fulfill({ json: { ...page1([record]), coverage: { detail_since: "2026-11-19T00:00:00Z", reason: "no_journal_before" } } }),
  );
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.getByTestId("memory-table").getByRole("button", { name: "топ записей" }).first().click();
  const note = page.getByTestId("memory-detail").getByRole("note");
  await expect(note).toContainText("журнал чтения ведётся с");
  await expect(note).not.toContainText("ещё не ведётся");
  await page.screenshot({ path: `${SHOTS}/a9cdedd8-1440-top-records.png` });
});
