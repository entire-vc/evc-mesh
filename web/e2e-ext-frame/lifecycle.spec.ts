import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { expect, test, type APIRequestContext, type BrowserContext, type Page } from "@playwright/test";
import { loginWithRetry } from "../e2e/auth-helper";
import { OUT_DIR, cfg } from "./env";

/**
 * Iframe lifecycle of an external extension, in a real browser, against the live ui-context API
 * (docs/extensions/ui-iframe.md, "postMessage protocol" and "States"):
 *
 *   ready        a frame on its own origin gets mesh.context under an authenticated session
 *   transport    a refused renewal that does NOT revoke (503, simulated at the browser transport)
 *                reaches the frame as mesh.context.error and leaves the frame in place
 *   revocation   the isolated QA binding is switched off through the API; the NEXT renewal made by
 *                the app itself is refused by the live server (409): the frame gets
 *                mesh.context.error then mesh.revoked and the revoked screen shows
 *   restore      switching the binding on and pressing Retry gives a working frame again
 *
 * Everything is observed from inside the frame's own window, so it does not depend on what the
 * receiver chooses to display. Only the 503 of `transport` is a model; every other status is the
 * server's. Message tokens are redacted before they are recorded; screenshots show no token.
 */

interface Seen {
  at: number;
  viewport: string;
  frameOrigin: string;
  type: string;
  status?: number | null;
  reason?: string;
  hasToken: boolean;
}

// The frame is untrusted: only protocol values reach the receipt, anything else is dropped (a frame cannot smuggle text into the artifact).
const TYPES = new Set(["mesh.context", "mesh.context.error", "mesh.revoked"]);
const REASONS = new Set(["unauthorized", "forbidden", "not_found", "conflict", "unavailable", "network"]);
const safeReason = (r: unknown): string | undefined => (typeof r === "string" && REASONS.has(r) ? r : undefined);
const safeStatus = (s: unknown): number | null | undefined => (s === null ? null : typeof s === "number" && Number.isInteger(s) && s >= 100 && s <= 599 ? s : undefined);

const VIEWPORTS = [
  { name: "1440", width: 1440, height: 900 },
  { name: "393", width: 393, height: 852 },
] as const;

const c = cfg();
const seen: Seen[] = [];
const uiContextStatuses: Array<{ at: number; viewport: string; status: number; simulated: boolean }> = [];
const steps: Array<{ viewport: string; step: string; status: "PASS" | "FAIL" }> = [];
let current = "setup";
let context: BrowserContext;
let page: Page;
let api: APIRequestContext;
let token = "";
let taskUrl = "";
let simulating = false;
/** Origin of the extension iframe under test, known once the panel is open; later waits count only its messages. */
let expectedFrameOrigin = "";

const auth = () => ({ Authorization: `Bearer ${token}` });
const extPath = (verb: "enable" | "disable") => `/api/v1/workspaces/${c.workspaceId}/extensions/${c.extensionId}/${verb}`;

mkdirSync(OUT_DIR, { recursive: true });

async function step<T>(viewport: string, name: string, fn: () => Promise<T>): Promise<T> {
  try {
    const out = await fn();
    steps.push({ viewport, step: name, status: "PASS" });
    return out;
  } catch (e) {
    // No exception text in the receipt: it can carry external content. The reason is in the job log and junit.
    steps.push({ viewport, step: name, status: "FAIL" });
    throw e;
  }
}

/** Waits for a message the frame received after `from`; throws when none arrives (the detector can fail). */
async function waitSeen(pred: (m: Seen) => boolean, from: number, timeoutMs: number, what: string): Promise<Seen> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const hit = seen.slice(from).find((m) => (!expectedFrameOrigin || m.frameOrigin === expectedFrameOrigin) && pred(m));
    if (hit) return hit;
    if (Date.now() > deadline) throw new Error(`no ${what} reached the frame within ${timeoutMs} ms (seen since mark: ${JSON.stringify(seen.slice(from).map((m) => `${m.type}${m.status ? ":" + m.status : ""}`))})`);
    await page.waitForTimeout(250);
  }
}

const frameLocator = () => page.locator('[data-testid="external-frame"] iframe');

async function openPanel(url = taskUrl, tabTimeoutMs = 30_000): Promise<void> {
  await page.goto(url);
  const title = c.tabTitle;
  // The task panel tabs are plain buttons (no role=tab); the extension's own are slot-tab-<id> (desktop) and
  // slot-tab-mobile-<id> (393), only one of the two is visible.
  const slotTab = page.locator(`[data-testid="slot-tab-${c.extensionId}"], [data-testid="slot-tab-mobile-${c.extensionId}"]`).filter({ visible: true }).first();
  const tab = title ? slotTab.or(page.getByText(title, { exact: true }).filter({ visible: true })).first() : slotTab;
  try {
    await tab.click({ timeout: tabTimeoutMs });
  } catch (e) {
    // A tab that never shows is diagnosed from what the page shows, not from a 4 min test timeout.
    await page.screenshot({ path: join(OUT_DIR, "shots", `no-tab-${current}.png`) }).catch(() => undefined);
    const tabs = await page.locator('[data-testid^="slot-tab-"]').evaluateAll((els) => els.map((el) => el.getAttribute("data-testid"))).catch(() => []);
    throw new Error(`the extension tab did not appear at ${new URL(page.url()).pathname}; tabs=${JSON.stringify(tabs)}`, { cause: e });
  }
  await expect(frameLocator(), "the iframe of the extension screen must be in the task panel").toBeVisible({ timeout: 30_000 });
}

async function frameOrigin(): Promise<string> {
  const src = (await frameLocator().getAttribute("src")) ?? "";
  return new URL(src).origin; // the query (the token) is dropped here on purpose
}

/** The frame asks for a renewal itself, as a real screen would. */
async function frameAsksForContext(): Promise<void> {
  const origin = new URL(c.baseURL).origin;
  const frame = page.frames().find((f) => f !== page.mainFrame() && new URL(f.url()).origin !== origin);
  if (!frame) throw new Error("no cross-origin frame to send mesh.ready from");
  await frame.evaluate((o) => window.parent.postMessage({ type: "mesh.ready" }, o), origin);
}

async function shot(vp: string, name: string): Promise<void> {
  await page.screenshot({ path: join(OUT_DIR, "shots", `${name}-${vp}.png`) });
}

test.describe.serial("external frame lifecycle", () => {
  test.beforeAll(async ({ browser }) => {
    mkdirSync(join(OUT_DIR, "shots"), { recursive: true });
    context = await browser.newContext({ baseURL: c.baseURL, viewport: { width: 1440, height: 900 } });
    api = context.request;
    ({ accessToken: token } = await loginWithRetry(api, c.email, c.password));

    // Every message a cross-origin frame receives, as the frame receives it. The token is never recorded.
    await context.exposeBinding("__extFrameMsg", (src, payload: string) => {
      // Only a direct child of the app page that is not on the app origin: a nested or same-origin frame is not the extension screen.
      if (src.frame.parentFrame() !== src.page.mainFrame() || new URL(src.frame.url()).origin === new URL(c.baseURL).origin) return;
      try {
        const d = JSON.parse(payload) as { type?: string; status?: number | null; reason?: string; token?: string };
        if (typeof d.type !== "string" || !TYPES.has(d.type)) return;
        seen.push({ at: Date.now(), viewport: current, frameOrigin: new URL(src.frame.url()).origin, type: d.type, status: safeStatus(d.status), reason: safeReason(d.reason), hasToken: typeof d.token === "string" && d.token.length > 0 });
      } catch {
        /* not ours */
      }
    });
    await context.addInitScript((appOrigin: string) => {
      if (window === window.top) return;
      window.addEventListener("message", (ev) => {
        // Only what the app page itself posts counts: a frame posting to itself must not satisfy an assertion.
        if (ev.source !== window.parent || ev.origin !== appOrigin) return;
        try {
          (window as unknown as { __extFrameMsg: (s: string) => void }).__extFrameMsg(JSON.stringify(ev.data));
        } catch {
          /* binding not ready */
        }
      });
    }, new URL(c.baseURL).origin);

    page = await context.newPage();
    page.on("response", (r) => {
      if (/\/api\/v1\/ext\/[^/]+\/ui-context$/.test(new URL(r.url()).pathname)) uiContextStatuses.push({ at: Date.now(), viewport: current, status: r.status(), simulated: simulating });
    });

    const ws = await api.get(`/api/v1/workspaces/${c.workspaceId}`, { headers: auth() });
    expect(ws.status(), "the pilot workspace must be readable by the QA user").toBe(200);
    const wsSlug = ((await ws.json()) as { slug: string }).slug;
    const pr = await api.get(`/api/v1/projects/${c.projectId}`, { headers: auth() });
    expect(pr.status(), "the pilot project must be readable by the QA user").toBe(200);
    const projectSlug = ((await pr.json()) as { slug: string }).slug;
    taskUrl = `/w/${wsSlug}/p/${projectSlug}/t/${c.taskId}`;

    // Known starting point: the binding is on.
    const on = await api.post(extPath("enable"), { headers: auth() });
    expect(on.status(), "enabling the isolated QA binding at start").toBe(200);
  });

  test.afterAll(async () => {
    // Whatever happened, leave the isolated binding switched on.
    const back = await api.post(extPath("enable"), { headers: auth() }).catch(() => undefined);
    writeFileSync(
      join(OUT_DIR, "receipt.json"),
      JSON.stringify({ restored_binding_status: back?.status() ?? null, steps, messages: seen.map(({ viewport, type, status, reason, hasToken }) => ({ viewport, type, status, reason, hasToken })), ui_context_statuses: uiContextStatuses, note: "live API except entries marked simulated:true (503 modelled at the browser transport)" }, null, 1),
    );
    await context.close();
  });

  test("controls: the waiter fails when the message never comes; a foreign task is refused live", async () => {
    current = "controls";
    await expect(waitSeen((m) => m.type === "mesh.never-sent", seen.length, 600, "never-sent message")).rejects.toThrow(/no never-sent message/);
    // The tab wait is bounded and diagnosed: a page with no extension tab fails fast and says what it showed.
    const t0 = Date.now();
    await expect(openPanel("/w/no-such-workspace/p/no-such-project/t/no-such-task", 1_500)).rejects.toThrow(/the extension tab did not appear at .*tabs=\[/);
    expect(Date.now() - t0, "the missing tab must fail well inside the test timeout").toBeLessThan(30_000);
    if (c.foreignTaskId) {
      const r = await api.post(`/api/v1/ext/${c.extensionId}/ui-context`, { headers: auth(), data: { workspace_id: c.workspaceId, slot: "task.panel", task_id: c.foreignTaskId } });
      expect([403, 404], "a task outside the binding must not get a context").toContain(r.status());
    }
  });

  for (const vp of VIEWPORTS) {
    test(`${vp.name}: ready, non-revoking error, revocation, restore`, async () => {
      current = vp.name;
      await page.setViewportSize({ width: vp.width, height: vp.height });

      await step(vp.name, "ready: frame of its own origin gets mesh.context", async () => {
        const mark = seen.length;
        await openPanel();
        const origin = await frameOrigin();
        expect(origin, "the frame must be served from its own origin").not.toBe(new URL(c.baseURL).origin);
        expectedFrameOrigin = origin;
        const ctx = await waitSeen((m) => m.type === "mesh.context", mark, 30_000, "mesh.context");
        expect(ctx.hasToken).toBe(true);
        expect(ctx.frameOrigin).toBe(origin);
        // Negative control: a message the frame posts to itself is not an app message and must not be observed.
        const spoofMark = seen.length;
        const frame = page.frames().find((f) => f !== page.mainFrame() && new URL(f.url()).origin === origin);
        await frame!.evaluate((o) => window.postMessage({ type: "mesh.revoked", reason: "spoof" }, o), origin);
        await page.waitForTimeout(500);
        expect(seen.slice(spoofMark).some((m) => m.type === "mesh.revoked"), "a self-posted mesh.revoked must not be recorded").toBe(false);
        await shot(vp.name, "ready");
      });

      await step(vp.name, "transport: 503 on renewal -> mesh.context.error, no mesh.revoked, frame stays, recovers", async () => {
        const mark = seen.length;
        simulating = true;
        await page.route(/\/api\/v1\/ext\/[^/]+\/ui-context$/, (r) => r.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "unavailable" }) }));
        await frameAsksForContext();
        const err = await waitSeen((m) => m.type === "mesh.context.error", mark, 20_000, "mesh.context.error");
        expect(err.status).toBe(503);
        expect(err.reason).toBe("unavailable");
        await page.waitForTimeout(2_000);
        expect(seen.slice(mark).some((m) => m.type === "mesh.revoked"), "a 5xx must not revoke").toBe(false);
        await expect(frameLocator()).toBeVisible();
        await shot(vp.name, "context-error");
        await page.unroute(/\/api\/v1\/ext\/[^/]+\/ui-context$/);
        simulating = false;
        const mark2 = seen.length;
        await frameAsksForContext();
        await waitSeen((m) => m.type === "mesh.context" && m.hasToken, mark2, 20_000, "mesh.context after the transport recovered");
      });

      await step(vp.name, "revocation: binding off -> live 409 on the app's own renewal -> context.error + revoked + revoked screen", async () => {
        const mark = seen.length;
        const t0 = Date.now();
        const off = await api.post(extPath("disable"), { headers: auth() });
        expect(off.status(), "switching the isolated QA binding off").toBe(200);
        try {
          // No frame action: the proactive renewal at ~80% of the 60 s token is the next real refresh.
          const err = await waitSeen((m) => m.type === "mesh.context.error", mark, 90_000, "mesh.context.error after the binding was switched off");
          expect([403, 409], "403/409 mean the binding was switched off; 404 would mean the extension, screen or task is gone").toContain(err.status);
          // The protocol sends mesh.revoked right AFTER mesh.context.error: look only past the error, so a revoked that came first cannot pass.
          const rev = await waitSeen((m) => m.type === "mesh.revoked", seen.indexOf(err) + 1, 10_000, "mesh.revoked after mesh.context.error");
          expect(rev.reason).toBe(err.reason);
          await expect(page.getByTestId("external-frame-revoked"), "the revoked screen").toBeVisible();
          await expect(page.locator("iframe"), "the frame is removed").toHaveCount(0);
          const live = uiContextStatuses.filter((s) => s.viewport === vp.name && !s.simulated && s.at >= t0 && [403, 409].includes(s.status));
          expect(live.length, "the server itself answered the refused renewal").toBeGreaterThan(0);
          await shot(vp.name, "revoked");
        } finally {
          const on = await api.post(extPath("enable"), { headers: auth() });
          expect(on.status(), "switching the isolated QA binding back on").toBe(200);
        }
      });

      await step(vp.name, "restore: binding on + Retry -> working frame (positive control)", async () => {
        const mark = seen.length;
        await page.getByRole("button", { name: "Retry" }).click();
        await expect(frameLocator()).toBeVisible({ timeout: 30_000 });
        await waitSeen((m) => m.type === "mesh.context" && m.hasToken, mark, 30_000, "mesh.context after restore");
        await shot(vp.name, "restored");
      });
    });
  }
});
