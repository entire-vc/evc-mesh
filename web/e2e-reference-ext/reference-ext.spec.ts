import fs from "node:fs";
import path from "node:path";
import { test, expect, type BrowserContext, type Page, type Response } from "@playwright/test";
import { loginWithRetry } from "../e2e/auth-helper";
import { isFixtureOrigin, assertAddress, assertCsp, assertExactTabs, assertFrameText, assertHistoryDelta, assertMarkerEqual, assertNoHScroll, assertRealReload, assertTabs } from "./asserts";

/**
 * Scenarios 1-4 of Mesh Doc e2e-reference-ext-project-view and S5-S7 (an internal ticket: real reload,
 * mesh.state push/replace, history and A/B back/forward), version SCENARIO_VERSION.
 * Published Mesh + published fixture; identity = CI variables E2E_REFEXT_EMAIL / E2E_REFEXT_PASSWORD
 * (masked, never printed). The refresh cookie is one-shot: ONE login and ONE context per viewport;
 * the context keeps the rotated cookie, so the one real page.reload() in S5 re-authenticates with the
 * browser's own cookie jar (no second login, no goto/login retry). Other moves are SPA moves.
 * The iframe URL carries a live token: it is never logged, stored in a trace/HAR or screenshot name.
 */
const SCENARIO_VERSION = "reference-ext-project-view@2";
const FIXTURE = process.env.E2E_REFEXT_ORIGIN ?? "https://reference-extension.example.invalid";
const WS = "reference-ext-qa";
const WS_ID = "00000000-0000-4000-8000-000000000001";
const A = { slug: "refext-qa-a", id: "00000000-0000-4000-8000-000000000002" };
const B = { slug: "refext-qa-b", id: "00000000-0000-4000-8000-000000000003" };
const EXT_PATH = (p: string) => `/w/${WS}/p/${p}/x/reference-ext/reports#monthly`;

test.describe.configure({ mode: "serial" });

let context: BrowserContext;
let page: Page;
let outDir: string;
let step = 0;
const pageErrors: string[] = [];
const consoleErrors: string[] = [];
let expectedB409Logs = 0;
const badResponses: string[] = [];
let uiContextStatuses: number[] = [];
let frameResponse: { status: number; csp?: string; xfo?: string } | undefined;
const walk: string[] = [];

const log = (line: string) => {
  walk.push(line);
  fs.writeFileSync(path.join(outDir, "walk.log"), walk.join("\n") + "\n");
};
async function shot(name: string) {
  step += 1;
  await page.screenshot({ path: path.join(outDir, `${String(step).padStart(2, "0")}-${name}.png`) });
  log(`shot ${String(step).padStart(2, "0")}-${name}`);
}
async function spaNav(to: string) {
  await page.evaluate((u) => {
    history.pushState({}, "", u);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }, to);
}
const hostAddress = () => page.evaluate(() => location.pathname + location.hash);
const historyLen = () => page.evaluate(() => history.length);
const frameBody = () => page.frameLocator('[data-testid="external-frame"] iframe');
const marker = async () => (await frameBody().getByTestId("marker").innerText()).trim();
/** Host + iframe must not overflow horizontally before a screenshot is taken. */
async function checkedShot(name: string) {
  assertNoHScroll(await page.evaluate(() => ({ scrollWidth: document.documentElement.scrollWidth, clientWidth: document.documentElement.clientWidth })), `host before ${name}`);
  const fr = page.locator('[data-testid="external-frame"] iframe');
  if ((await fr.count()) > 0) {
    assertNoHScroll(await (await fr.elementHandle())!.contentFrame().then((f) => f!.evaluate(() => ({ scrollWidth: document.documentElement.scrollWidth, clientWidth: document.documentElement.clientWidth }))), `iframe before ${name}`);
  }
  await shot(name);
}
/** Names of the tabs/menu items the user can actually see: hidden or collapsed nodes do not count. */
async function tabNames(): Promise<string[]> {
  return page.evaluate(() => {
    const visible = (e: Element) => {
      const r = e.getBoundingClientRect();
      const st = getComputedStyle(e);
      return r.width > 0 && r.height > 0 && st.visibility !== "hidden" && st.display !== "none";
    };
    return [...document.querySelectorAll('[role="tab"], [role="menuitem"], header a, header button, nav a')]
      .filter(visible)
      .map((e) => ((e as HTMLElement).innerText ?? "").trim())
      .filter(Boolean);
  });
}
/** The visible project tab strip only (`.mesh-project-tabs`), so the set can be compared exactly. */
async function stripTabs(): Promise<string[]> {
  return page.evaluate(() =>
    [...document.querySelectorAll('.mesh-project-tabs button, .mesh-project-tabs a, .mesh-project-tabs [role="tab"]')]
      .filter((e) => {
        const r = e.getBoundingClientRect();
        return r.width > 0 && r.height > 0 && getComputedStyle(e).visibility !== "hidden";
      })
      .map((e) => ((e as HTMLElement).innerText ?? "").trim())
      .filter(Boolean),
  );
}
/** Opens the mobile overflow (⋮) menu and proves it is open: a visible menu item must appear. */
async function openOverflow() {
  const more = page.locator('.mesh-project-tabs button[title*="more views" i]').first();
  await expect(more, "the mobile overflow button must exist").toBeVisible();
  // The strip can re-render right after project B loads and close a menu opened too early: click again only while no item is visible.
  const item = page.locator('[role="menuitem"]:visible').first();
  await expect(async () => {
    if (!(await item.isVisible())) await more.click();
    await expect(item, "the overflow menu must open").toBeVisible({ timeout: 1_500 });
  }).toPass({ timeout: 15_000 });
}

test.beforeAll(async ({ browser }, info) => {
  const email = process.env.E2E_REFEXT_EMAIL;
  const password = process.env.E2E_REFEXT_PASSWORD;
  if (!email || !password) throw new Error("[setup] E2E_REFEXT_EMAIL / E2E_REFEXT_PASSWORD are missing: FAIL SETUP, not a skip");
  outDir = path.resolve("reference-ext-e2e", info.project.name);
  fs.mkdirSync(outDir, { recursive: true });
  log(`scenario ${SCENARIO_VERSION} viewport ${info.project.name} sha ${process.env.CI_COMMIT_SHA ?? "local"}`);

  // browser.newContext() ignores the project's `use`: pass base URL and viewport explicitly.
  const { viewport, deviceScaleFactor } = info.project.use;
  const baseURL = process.env.APP_BASE_URL;
  if (!baseURL) throw new Error("[setup] APP_BASE_URL is missing: FAIL SETUP");
  const ver = await fetch(`${baseURL}/api/v1/version`).then((r) => r.text()).catch(() => "unavailable");
  log(`live service version ${ver.slice(0, 300)}`);
  context = await browser.newContext({ baseURL, viewport, deviceScaleFactor });
  await loginWithRetry(context.request, email, password, { maxAttempts: 1 }); // exactly one login per viewport: a 429 fails the run, it is never retried
  page = await context.newPage();
  page.on("pageerror", (e) => pageErrors.push(e.message));
  // The text of a console.error is not logged (it can carry a token URL): only its count and location.
  // Only the expected B409 is exempt: a "Failed to load resource ... 409" on the ui-context path while project B is being opened.
  page.on("console", (m) => {
    if (m.type() !== "error") return;
    const url = m.location().url.split("?")[0];
    const expectedB409 = uiContextB && url.endsWith("/ext/reference-ext/ui-context") && /Failed to load resource.*\b409\b/.test(m.text());
    if (expectedB409) expectedB409Logs += 1;
    else consoleErrors.push(url);
  });
  page.on("response", (r: Response) => {
    const u = new URL(r.url());
    const s = r.status();
    if (u.pathname.endsWith("/ext/reference-ext/ui-context")) uiContextStatuses.push(s);
    if (isFixtureOrigin(u, FIXTURE) && u.pathname.startsWith("/mesh/ui/project/reports") && !frameResponse) {
      const h = r.headers();
      frameResponse = { status: s, csp: h["content-security-policy"], xfo: h["x-frame-options"] };
    }
    if (s >= 500) badResponses.push(`${s} ${u.pathname}`);
    else if (s >= 400) {
      const okB = u.pathname.endsWith("/ext/reference-ext/ui-context") && s === 409 && uiContextB;
      if (!okB) badResponses.push(`${s} ${u.pathname}`);
    }
  });
});
let uiContextB = false;

test.afterAll(async () => {
  await context?.close();
});

test("red controls: the assert helpers fail on broken input", () => {
  expect(() => assertCsp("frame-ancestors *", undefined)).toThrow();
  expect(() => assertCsp("frame-ancestors https://mesh.entire.host", "DENY")).toThrow();
  expect(() => assertCsp(undefined, undefined)).toThrow();
  // origin control: same prefix + a foreign host (or port) must not count as the fixture
  expect(isFixtureOrigin(new URL("https://fx.example.invalid/mesh/ui"), "https://fx.example.invalid")).toBe(true);
  expect(isFixtureOrigin(new URL("https://fx.example.invalid.evil.test/mesh/ui"), "https://fx.example.invalid")).toBe(false);
  expect(isFixtureOrigin(new URL("https://fx.example.invalid:8443/mesh/ui"), "https://fx.example.invalid")).toBe(false);
  expect(() => assertNoHScroll({ scrollWidth: 500, clientWidth: 393 }, "red")).toThrow();
  expect(() => assertTabs(["Board"], ["Reference"], [])).toThrow();
  expect(() => assertExactTabs(["Board", "List", "Docs"], ["Board", "List"])).toThrow();
  expect(() => assertExactTabs(["Board"], ["Board", "List"])).toThrow();
  expect(() => assertTabs(["Board", "Reference"], [], ["Reference"])).toThrow();
  expect(() => assertFrameText("Monthly report: November", ["Synthetic monthly report: October, 48 items closed"])).toThrow();
  expect(() => assertFrameText("Reference is unavailable right now", [])).toThrow();
  expect(() => assertHistoryDelta(5, 6, 0, "red")).toThrow();
  expect(() => assertHistoryDelta(5, 5, 1, "red")).toThrow();
  expect(() => assertMarkerEqual("abc", "abd", "red")).toThrow();
  expect(() => assertMarkerEqual("", "", "red")).toThrow();
  expect(() => assertAddress("/a#weekly", "/a#monthly", "red")).toThrow();
  expect(() => assertRealReload("navigate", false)).toThrow();
  expect(() => assertRealReload("reload", true)).toThrow();
  log("red controls: 17/17 broken inputs rejected");
});

test("S1: project A has the Reference tab and real iframe content", async ({}, info) => {
  const mobile = info.project.name === "393";
  await page.goto(`/w/${WS}/p/${A.slug}`);
  await page.waitForLoadState("networkidle");
  await shot("s1-project-a");
  let names = await tabNames();
  if (mobile && !names.includes("Reference")) {
    await openOverflow();
    await shot("s1-project-a-overflow");
    names = await tabNames();
  }
  assertTabs(names, ["Reference"], []);
  log(`S1 tabs ${JSON.stringify(names)}`);

  await spaNav(EXT_PATH(A.slug));
  const frame = page.locator('[data-testid="external-frame"] iframe');
  await expect(frame).toHaveCount(1, { timeout: 30_000 });
  const fl = page.frameLocator('[data-testid="external-frame"] iframe');
  await expect(fl.locator("body")).toContainText("Monthly report", { timeout: 30_000 });
  expect(uiContextStatuses).toContain(200);
  const src = (await frame.getAttribute("src")) ?? "";
  expect(src.startsWith(`${FIXTURE}/mesh/ui/project/reports?token=`)).toBe(true);
  const text = await fl.locator("body").innerText();
  assertFrameText(text, [
    "Reference project · user@example.com",
    `Workspace ${WS_ID}`,
    `project ${A.id}`,
    "Monthly report",
    "Synthetic monthly report: October, 48 items closed",
    "Section: /reports#monthly",
  ]);
  const box = (await frame.boundingBox())!;
  const want = mobile ? { w: 369, h: 723 } : { w: 1152, h: 796 };
  log(`S1 iframe ${Math.round(box.width)}x${Math.round(box.height)} want ${want.w}x${want.h}`);
  expect(Math.abs(box.width - want.w)).toBeLessThanOrEqual(2);
  expect(Math.abs(box.height - want.h)).toBeLessThanOrEqual(2);
  assertNoHScroll(await page.evaluate(() => ({ scrollWidth: document.documentElement.scrollWidth, clientWidth: document.documentElement.clientWidth })), "host");
  assertNoHScroll(await (await frame.elementHandle())!.contentFrame().then((f) => f!.evaluate(() => ({ scrollWidth: document.documentElement.scrollWidth, clientWidth: document.documentElement.clientWidth }))), "iframe");
  await shot("s1-reference-tab");
});

test("S2: project B has no Reference tab", async ({}, info) => {
  await spaNav(`/w/${WS}/p/${B.slug}`);
  await expect(page).toHaveURL(new RegExp(`/p/${B.slug}$`));
  const mobile = info.project.name === "393";
  const wantTabs = mobile ? ["Board", "List", "Dependencies"] : ["Board", "List", "Dependencies", "Docs", "Updates"];
  // The URL changes before project B renders: poll until the strip stops showing project A's tabs, then assert the exact set.
  await expect.poll(async () => (await stripTabs()).join("|"), { timeout: 30_000 }).toBe(wantTabs.join("|"));
  const names = await stripTabs();
  assertExactTabs(names, wantTabs);
  if (mobile) {
    await openOverflow();
    assertTabs(await tabNames(), [], ["Reference"]);
  }
  log(`S2 tabs ${JSON.stringify(names)}`);
  await shot("s2-project-b");
});

test("S3: deep link on project B shows unavailable + Retry that re-requests", async () => {
  uiContextB = true;
  uiContextStatuses = [];
  await spaNav(EXT_PATH(B.slug));
  await expect(page.getByTestId("external-frame-unavailable")).toBeVisible({ timeout: 30_000 });
  await expect(page.getByTestId("external-frame-unavailable")).toContainText("Reference is unavailable right now");
  expect(await page.locator('[data-testid="external-frame"] iframe').count()).toBe(0);
  expect(uiContextStatuses).toEqual([409]);
  await shot("s3-unavailable");
  await page.getByRole("button", { name: "Retry" }).click();
  await expect.poll(() => uiContextStatuses.length, { timeout: 15_000 }).toBe(2);
  expect(uiContextStatuses).toEqual([409, 409]);
  await expect(page.getByTestId("external-frame-unavailable")).toBeVisible();
  await shot("s3-after-retry");
  uiContextB = false;
});

test("S5: real reload keeps URL, section, workspace/project/user", async () => {
  await spaNav(EXT_PATH(A.slug));
  await expect(frameBody().locator("body")).toContainText("Synthetic monthly report: October, 48 items closed", { timeout: 30_000 });
  await page.evaluate(() => { (window as unknown as { __sentinel: number }).__sentinel = 1; });
  const before = await hostAddress();
  assertAddress(before, EXT_PATH(A.slug), "before reload");
  await checkedShot("s5-before-reload");
  log("S5 reload kind: page.reload() (full document reload of the host, cookie jar of the same context, no goto/login)");
  await page.reload({ waitUntil: "load" });
  const reloaded = await page.evaluate(() => ({
    nav: (performance.getEntriesByType("navigation")[0] as PerformanceNavigationTiming | undefined)?.type,
    sentinel: (window as unknown as { __sentinel?: number }).__sentinel === 1,
  }));
  assertRealReload(reloaded.nav, reloaded.sentinel);
  assertAddress(await hostAddress(), EXT_PATH(A.slug), "after reload");
  await expect(frameBody().locator("body")).toContainText("Synthetic monthly report: October, 48 items closed", { timeout: 30_000 });
  const text = await frameBody().locator("body").innerText();
  assertFrameText(text, ["Reference project · user@example.com", `Workspace ${WS_ID}`, `project ${A.id}`, "Monthly report", "Section: /reports#monthly"]);
  log(`S5 after reload: nav ${reloaded.nav}, address ${await hostAddress()}, workspace/project/user as before`);
  await checkedShot("s5-after-reload");
});

test("S6: Reports weekly = mesh.state push, Replace with monthly = replace, marker equal", async () => {
  const m0 = await marker();
  await frameBody().getByRole("button", { name: "Overview" }).click();
  await expect.poll(hostAddress).toBe(`/w/${WS}/p/${A.slug}/x/reference-ext/overview`);
  await expect(frameBody().getByTestId("section")).toHaveText("/overview");
  const len1 = await historyLen();
  await checkedShot("s6-overview");

  await frameBody().getByRole("button", { name: "Reports: weekly" }).click();
  await expect.poll(hostAddress).toBe(`/w/${WS}/p/${A.slug}/x/reference-ext/reports#weekly`);
  await expect(frameBody().getByTestId("section-text")).toHaveText("Synthetic weekly report: week 44, 12 items closed.");
  const len2 = await historyLen();
  assertHistoryDelta(len1, len2, 1, "weekly (push)");
  assertMarkerEqual(m0, await marker(), "after push");
  log(`S6 push: address ${await hostAddress()}, history ${len1} -> ${len2}, marker equal`);
  await checkedShot("s6-weekly-push");

  await frameBody().getByRole("button", { name: "Replace with monthly" }).click();
  await expect.poll(hostAddress).toBe(EXT_PATH(A.slug));
  await expect(frameBody().getByTestId("section-text")).toHaveText("Synthetic monthly report: October, 48 items closed.");
  const len3 = await historyLen();
  assertHistoryDelta(len2, len3, 0, "replace with monthly");
  assertMarkerEqual(m0, await marker(), "after replace");
  log(`S6 replace: address ${await hostAddress()}, history ${len2} -> ${len3}, marker equal`);
  await checkedShot("s6-replace-monthly");
});

test("S7: back/forward restore section and the right A/B context", async () => {
  // History now: [.. overview, monthly(replaced weekly)]. Back must land on overview, not weekly: the replace left no weekly entry.
  await page.goBack();
  await expect.poll(hostAddress).toBe(`/w/${WS}/p/${A.slug}/x/reference-ext/overview`);
  await expect(frameBody().getByTestId("section")).toHaveText("/overview");
  log(`S7 back (replace left no weekly entry): ${await hostAddress()}`);
  await checkedShot("s7-back-overview");
  await page.goForward();
  await expect.poll(hostAddress).toBe(EXT_PATH(A.slug));
  await expect(frameBody().getByTestId("section")).toHaveText("/reports#monthly");
  log(`S7 forward: ${await hostAddress()}`);
  await checkedShot("s7-forward-monthly");

  // A -> B -> back: the frame must show project A again, never project B's (stale) content.
  uiContextB = true;
  await spaNav(EXT_PATH(B.slug));
  await expect(page.getByTestId("external-frame-unavailable")).toBeVisible({ timeout: 30_000 });
  expect(await page.locator('[data-testid="external-frame"] iframe').count()).toBe(0);
  await checkedShot("s7-project-b");
  await page.goBack();
  await expect.poll(hostAddress).toBe(EXT_PATH(A.slug));
  await expect(frameBody().locator("body")).toContainText(`project ${A.id}`, { timeout: 30_000 });
  const textA = await frameBody().locator("body").innerText();
  assertFrameText(textA, [`Workspace ${WS_ID}`, `project ${A.id}`, "Section: /reports#monthly"]);
  expect(textA.includes(B.id), "project B id must not appear in project A frame").toBe(false);
  expect(await page.getByTestId("external-frame-unavailable").count()).toBe(0);
  log("S7 back B->A: project A context restored, no project B content");
  await checkedShot("s7-back-project-a");
  await page.goForward();
  await expect(page.getByTestId("external-frame-unavailable")).toBeVisible({ timeout: 30_000 });
  expect(await page.locator('[data-testid="external-frame"] iframe').count()).toBe(0);
  log("S7 forward A->B: unavailable, no stale project A frame");
  await checkedShot("s7-forward-project-b");
  uiContextB = false;
});

test("S4: CSP of the live iframe and 401 without token, 0 pageerror/5xx/unexpected 4xx", async () => {
  expect(frameResponse, "the iframe response must have been observed").toBeTruthy();
  expect(frameResponse!.status).toBe(200);
  assertCsp(frameResponse!.csp, frameResponse!.xfo);
  log(`S4 iframe response 200, csp ok, x-frame-options absent`);
  const noToken = await context.request.get(`${FIXTURE}/mesh/ui/project`, { maxRedirects: 0 });
  expect(noToken.status()).toBe(401);
  log(`S4 no-token ${noToken.status()} (positive control: the same path with token answered ${frameResponse!.status})`);
  expect(pageErrors, "pageerror").toEqual([]);
  // The expected B409 is a network 4xx the browser reports as a console.error; every other console.error fails.
  expect(consoleErrors, "console.error outside the expected B409").toEqual([]);
  log(`console.error: 0 unexpected, ${expectedB409Logs} expected B409 ui-context`);
  expect(badResponses, "5xx / unexpected 4xx").toEqual([]);
  await shot("s4-final");
});
