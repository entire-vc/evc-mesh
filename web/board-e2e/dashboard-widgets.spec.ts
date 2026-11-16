import { test, expect, type Page, type Route } from "@playwright/test";
import { mkdirSync, readFileSync } from "node:fs";
// External dashboard widgets (dashboard.widget, protocol v2) on the real dashboard page. The dashboards API,
// the workspace's extensions, Mesh's ui-context and the extension's own origin are routed here; the widget
// page is the reference extension's real one (refext-widget.html, kept in sync by TestWidgetPageGolden).
// The network log proves the lazy, six-at-a-time loading; the screenshots go to $DASHBOARD_WIDGET_SHOTS
// (in CI: dashboard-widget-shots, an artifact).
const API = "http://127.0.0.1:8019";
const MESH = "http://127.0.0.1:3019";
const EXT = "http://127.0.0.1:8029";
const BOOT_MS = 60_000;
/** The widget's page answers this late: a frame holds its load slot until mesh.app-ready. */
const FRAME_DELAY_MS = 1_500;
const SHOTS = process.env.DASHBOARD_WIDGET_SHOTS ?? (process.env.CI ? "dashboard-widget-shots" : undefined);
const WIDGET_HTML = readFileSync(new URL("./refext-widget.html", import.meta.url), "utf8");
const TYPE = "reference-ext.status";

type Instance = { id: string; widget_type: string; x: number; y: number; w: number; h: number; settings?: Record<string, unknown> };
type Event = { at: number; kind: "start" | "done"; instance: string };

const cors = { "Access-Control-Allow-Origin": MESH, "Access-Control-Allow-Credentials": "true", "Access-Control-Allow-Headers": "Authorization, Content-Type", "Access-Control-Allow-Methods": "GET, POST, PUT, OPTIONS" };
const json = (route: Route, body: unknown, status = 200) => route.fulfill({ status, headers: cors, contentType: "application/json", body: JSON.stringify(body) });

const referenceExt = {
  id: "reference-ext", name: "Reference extension", version: "1.0.0", kind: "integration", category: "integrations",
  manifest: {
    capabilities: { ui: ["dashboard.widget"] },
    widgets: [{ id: "status", title: "Reference widget", category: "integrations", scope: "workspace", render: { frame: { path: "/mesh/ui/widget", min_refresh_s: 30 } } }],
  },
  connection: { settings: {} },
  status: { enabled: true, source: "workspace", consented_capabilities: ["ui:dashboard.widget"], health: { ok: true, message: "" } },
};

const grid = (n: number, w: number, h: number, perRow: number): Instance[] =>
  Array.from({ length: n }, (_, i) => ({ id: `w${i + 1}`, widget_type: TYPE, x: (i % perRow) * w, y: Math.floor(i / perRow) * h, w, h, settings: { label: `Widget ${i + 1}` } }));

/** Routes one dashboard with `widgets` and records, per instance, when its load started (first ui-context) and
 *  when its page was served (the frame then sends mesh.app-ready and gives its slot back). */
async function stand(page: Page, widgets: Instance[], scheme: "light" | "dark" = "light") {
  const events: Event[] = [];
  const contexts: string[] = [];
  const frames: string[] = [];
  const t0 = Date.now();
  const doc = { id: "dash-ext", name: "Extension widgets", icon: "", placement: "top", position: 0, version: 1, shared: false, can_edit: true, widgets };
  await page.addInitScript((t) => localStorage.setItem("theme", t), scheme);
  await page.route(/\/api\/v1\/workspaces\/[^/]+\/dashboards(\?|$)/, (r) => json(r, { dashboards: [doc] }));
  await page.route(/\/api\/v1\/workspaces\/[^/]+\/dashboards\/[^/?]+(\?|$)/, (r) => json(r, doc));
  await page.route(/\/api\/v1\/workspaces\/[^/]+\/extensions(\?|$)/, (r) => json(r, { extensions: [referenceExt] }));
  await page.route(`${API}/api/v1/ext/reference-ext/ui-context`, async (r) => {
    if (r.request().method() === "OPTIONS") return r.fulfill({ status: 204, headers: cors });
    const body = r.request().postDataJSON() as { instance_id: string; dashboard_id: string; widget_id: string; slot: string };
    expect(body).toMatchObject({ slot: "dashboard.widget", dashboard_id: "dash-ext", widget_id: "status" });
    if (!contexts.includes(body.instance_id)) events.push({ at: Date.now() - t0, kind: "start", instance: body.instance_id });
    contexts.push(body.instance_id);
    return json(r, {
      token: `token-${body.instance_id}`, expires_at: new Date(Date.now() + 60_000).toISOString(),
      frame_url: `${EXT}/mesh/ui/widget?i=${encodeURIComponent(body.instance_id)}`, origin: EXT, title: "Reference widget",
    });
  });
  await page.route(`${EXT}/**`, async (r) => {
    const url = new URL(r.request().url());
    const instance = url.searchParams.get("i") ?? "";
    expect(url.searchParams.get("token")).toBe(`token-${instance}`);
    frames.push(instance);
    await new Promise((res) => setTimeout(res, FRAME_DELAY_MS));
    events.push({ at: Date.now() - t0, kind: "done", instance });
    await r.fulfill({
      status: 200, contentType: "text/html; charset=utf-8",
      headers: { "Content-Security-Policy": `frame-ancestors ${MESH}`, "Cache-Control": "no-store" },
      body: WIDGET_HTML.replaceAll("__INSTANCE__", instance).replaceAll("__DASHBOARD__", doc.id),
    });
  });
  return { events, contexts, frames };
}

/** The most loads at once: starts minus served pages, over the event log in arrival order. */
const peak = (events: Event[]) => events.reduce((acc, e) => ({ now: acc.now + (e.kind === "start" ? 1 : -1), max: Math.max(acc.max, acc.now + (e.kind === "start" ? 1 : -1)) }), { now: 0, max: 0 }).max;
const log = (events: Event[]) => events.map((e) => `+${e.at}ms ${e.kind} ${e.instance}`).join("\n");

const open = async (page: Page) => {
  await page.goto("/w/board-fixture/d/dash-ext");
  await expect(page.locator("[data-widget-instance]").first()).toBeVisible({ timeout: BOOT_MS });
};
const ready = (page: Page) => page.locator('[data-testid="external-widget"][data-app-ready="true"]');
const freeze = (page: Page) =>
  page.addStyleTag({ content: "*,*::before,*::after{animation:none!important;transition:none!important;caret-color:transparent!important}" });

test.describe("dashboard.widget loading (1440)", () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test("ten visible frames load at most six at a time, each once", async ({ page }, info) => {
    test.setTimeout(BOOT_MS + 60_000);
    const net = await stand(page, grid(10, 3, 2, 4));
    await open(page);
    await expect(ready(page)).toHaveCount(10, { timeout: 30_000 });
    await info.attach("network-log.txt", { body: log(net.events), contentType: "text/plain" });
    console.log(`[dashboard-widgets] visible grid:\n${log(net.events)}`);
    const starts = net.events.filter((e) => e.kind === "start");
    expect(starts).toHaveLength(10);
    // the first burst is exactly six: the seventh load starts only after a page was served
    const firstDone = net.events.findIndex((e) => e.kind === "done");
    expect(net.events.slice(0, firstDone).filter((e) => e.kind === "start")).toHaveLength(6);
    expect(peak(net.events)).toBe(6);
    // every frame document was requested once: no reloads on theme, settings or visibility
    expect([...net.frames].sort()).toEqual(grid(10, 3, 2, 4).map((w) => w.id).sort());
    // the real reference page drew inside each frame with its own instance
    const last = page.frameLocator('[data-widget-instance="w10"] iframe');
    await expect(last.getByTestId("instance")).toHaveText("w10");
    await expect(last.getByTestId("settings")).toHaveText('{"label":"Widget 10"}');
  });

  test("frames below the fold are not requested until scrolled to", async ({ page }, info) => {
    test.setTimeout(BOOT_MS + 60_000);
    const net = await stand(page, grid(10, 12, 4, 1));
    await open(page);
    await expect(ready(page).first()).toBeVisible({ timeout: 30_000 });
    await page.waitForTimeout(FRAME_DELAY_MS * 2);
    const before = net.events.filter((e) => e.kind === "start").map((e) => e.instance);
    await info.attach("network-log-before-scroll.txt", { body: log(net.events), contentType: "text/plain" });
    console.log(`[dashboard-widgets] stacked, before scroll:\n${log(net.events)}`);
    expect(before.length).toBeGreaterThan(0);
    expect(before.length).toBeLessThan(5);
    expect(before).toEqual(["w1", "w2", "w3", "w4"].slice(0, before.length));
    expect(net.frames).not.toContain("w10");
    await page.locator('[data-widget-instance="w10"]').scrollIntoViewIfNeeded();
    await expect(page.locator('[data-widget-instance="w10"] [data-testid="external-widget"][data-app-ready="true"]')).toBeVisible({ timeout: 30_000 });
    await info.attach("network-log-after-scroll.txt", { body: log(net.events), contentType: "text/plain" });
    console.log(`[dashboard-widgets] stacked, after scroll:\n${log(net.events)}`);
    expect(net.frames).toContain("w10");
    expect(peak(net.events)).toBeLessThanOrEqual(6);
  });
});

for (const [width, scheme] of [[1440, "light"], [1440, "dark"], [393, "light"], [393, "dark"]] as const) {
  test(`reference widget on the dashboard ${width} ${scheme}`, async ({ page }) => {
    test.setTimeout(BOOT_MS + 60_000);
    await page.setViewportSize({ width, height: 900 });
    await stand(page, [
      { id: "w1", widget_type: TYPE, x: 0, y: 0, w: 6, h: 5, settings: { label: "Sprint 47" } },
      { id: "w2", widget_type: TYPE, x: 6, y: 0, w: 6, h: 5, settings: { label: "Release train" } },
    ], scheme);
    await open(page);
    await expect(ready(page)).toHaveCount(2, { timeout: 30_000 });
    const frame = page.frameLocator('[data-widget-instance="w1"] iframe');
    await expect(frame.getByTestId("mode")).toHaveText(scheme);
    // The frame fills its cell: a frame squeezed to its minimum hides the widget's own buttons.
    for (const id of ["w1", "w2"]) {
      const cell = await page.locator(`[data-widget-instance="${id}"]`).boundingBox();
      const box = await page.locator(`[data-widget-instance="${id}"] iframe`).boundingBox();
      expect(box!.height, `${id} frame height in a ${cell!.height}px cell`).toBeGreaterThan(cell!.height * 0.7);
    }
    await freeze(page);
    if (!SHOTS) return;
    mkdirSync(SHOTS, { recursive: true });
    await page.addStyleTag({ content: "[data-sonner-toaster]{display:none!important}" });
    await page.screenshot({ path: `${SHOTS}/dashboard-widget-${width}-${scheme}.png`, fullPage: true });
  });
}
