import { test, expect, type CDPSession, type Page } from "@playwright/test";
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

/**
 * Per-action perf counters on four authed paths (web/perf/README.md, part 2).
 *
 * For every action it records, from the moment the action starts until the
 * page is quiet again:
 *   react_commits       commits seen by the root <Profiler id="app"> (perf build only)
 *   board_card_commits  commits under the board card Profiler alone
 *   layout_count        CDP Performance.getMetrics LayoutCount delta
 *   recalc_style_count  CDP RecalcStyleCount delta
 *   dom_mutations       MutationObserver records on the whole document
 *   ms                  wall time — written to the report, NEVER gated
 *
 * Every counter except ms is compared with its ceiling in budget.json
 * (`<path>.<metric>`); going over fails the test with the path and metric in
 * the message. Going under never fails — lower the ceiling instead (README).
 */

const here = dirname(fileURLToPath(import.meta.url));

/**
 * `AppLayout` warms the dashboard and board chunks on idle
 * (`src/lib/prefetch-next-route.ts`) — the first `import()` of a lazy chunk
 * inserts its `<link rel="modulepreload">`s into `<head>`, and a test whose
 * setup navigates straight to a route (skipping whichever of the two chunks
 * that route doesn't itself load) can have the warm-up's idle callback land
 * *during* the timed action, miscounting its `<head>` insertions as the
 * action's own `dom_mutations`. The fix is to wait for the warmed chunk's
 * resource-timing entry before the action starts (below).
 *
 * The wait needs the *exact* built filename, not a hand-written prefix
 * pattern like `/assets/board-[\w-]+\.js` — that string is a bet that no
 * other current or future chunk's name also starts with "board-" right after
 * "/assets/", and nothing enforces the bet. Reading it from the same
 * manifest `check-bundle-budget.mjs` uses (keyed by source path, so a rename
 * of the chunk is still found) removes the bet entirely: wrong or missing
 * entry fails loudly here instead of quietly matching nothing.
 */
const manifestPath = resolve(here, "..", "dist-perf", ".vite", "manifest.json");
function manifestChunkFile(srcPath: string): string {
  const manifest = JSON.parse(readFileSync(manifestPath, "utf8")) as Record<
    string,
    { file?: string }
  >;
  const file = manifest[srcPath]?.file;
  if (!file) {
    throw new Error(
      `${manifestPath} has no chunk file for "${srcPath}" (renamed or moved?). Keys: ${Object.keys(manifest).join(", ") || "(none)"}`,
    );
  }
  return file;
}
const BOARD_CHUNK = manifestChunkFile("src/pages/board.tsx");
const DASHBOARD_CHUNK = manifestChunkFile("src/pages/dashboard.tsx");

async function waitForChunkFetched(target: Page, chunkFile: string): Promise<void> {
  await target.waitForFunction(
    (file) => performance.getEntriesByType("resource").some((e) => e.name.endsWith(file)),
    chunkFile,
    { timeout: 15_000 },
  );
}

const fixture = JSON.parse(readFileSync(resolve(here, ".fixture.json"), "utf8")) as {
  ws_slug: string;
  project_slug: string;
  project_id: string;
  open_task_id: string;
  comment_task_id: string;
  email: string;
  password: string;
};
const budget = JSON.parse(readFileSync(resolve(here, "budget.json"), "utf8")) as Record<
  string,
  number
>;

const GATED = [
  "react_commits",
  "board_card_commits",
  "layout_count",
  "recalc_style_count",
  "dom_mutations",
] as const;
type Metric = (typeof GATED)[number];
type Sample = Record<Metric, number> & { ms: number };

const QUIET_MS = 500;
const SETTLE_TIMEOUT_MS = 30_000;

test.describe.configure({ mode: "serial" });

let page: Page;
let cdp: CDPSession;
/** In-flight /api requests, by request, so a stuck one can be named. */
const inflight = new Map<object, string>();
const report: Record<string, { gated: Sample; runs: Sample[] }> = {};
let authHeaders: Record<string, string> = {};

/**
 * Each path is measured REPEAT times from the same starting state, and the
 * MINIMUM of each metric is what gets gated. Measured on 8 local runs: the
 * app's own data fetches race (tasks vs statuses), and when they land in the
 * unlucky order every board card renders twice — 300 card commits instead of
 * 150, about 1 run in 8. A race can only ADD work, never remove it, so the
 * minimum of 3 is the deterministic figure, and a ceiling taken from it is
 * tight enough to catch a real doubled render. All runs go to the report.
 */
const REPEAT = Number(process.env.PERF_REPEAT || 3);

async function cdpMetrics(): Promise<{ layout: number; recalc: number }> {
  const { metrics } = await cdp.send("Performance.getMetrics");
  const get = (name: string) => metrics.find((m) => m.name === name)?.value ?? 0;
  return { layout: get("LayoutCount"), recalc: get("RecalcStyleCount") };
}

/** Quiet = no in-flight /api request and no DOM mutation for QUIET_MS. */
async function settle(): Promise<void> {
  const deadline = Date.now() + SETTLE_TIMEOUT_MS;
  let quietSince = 0;
  let lastMutations = -1;
  while (Date.now() < deadline) {
    const mutations = await page.evaluate(() => window.__meshPerfMutations ?? 0);
    const now = Date.now();
    if (inflight.size === 0 && mutations === lastMutations) {
      if (quietSince === 0) quietSince = now;
      if (now - quietSince >= QUIET_MS) return;
    } else {
      quietSince = 0;
    }
    lastMutations = mutations;
    await page.waitForTimeout(50);
  }
  throw new Error(
    `page did not go quiet within ${SETTLE_TIMEOUT_MS} ms; in flight: ${[...inflight.values()].join(", ") || "none (DOM kept mutating)"}`
  );
}

async function measure(action: () => Promise<void>): Promise<Sample> {
  await settle();
  await page.evaluate(() => {
    window.__meshPerfCommits = {};
    window.__meshPerfMutations = 0;
  });
  const before = await cdpMetrics();
  const t0 = Date.now();
  await action();
  await settle();
  const ms = Date.now() - t0 - QUIET_MS;
  const after = await cdpMetrics();
  const { commits, mutations } = await page.evaluate(() => ({
    commits: window.__meshPerfCommits ?? {},
    mutations: window.__meshPerfMutations ?? 0,
  }));
  return {
    react_commits: commits.app ?? 0,
    board_card_commits: commits["board-card"] ?? 0,
    layout_count: after.layout - before.layout,
    recalc_style_count: after.recalc - before.recalc,
    dom_mutations: mutations,
    ms,
  };
}

async function measureRepeated(
  path: string,
  setup: () => Promise<void>,
  action: () => Promise<void>
): Promise<Sample> {
  const runs: Sample[] = [];
  for (let i = 0; i < REPEAT; i++) {
    await setup();
    runs.push(await measure(action));
  }
  const gated = Object.fromEntries(
    [...GATED, "ms"].map((m) => [m, Math.min(...runs.map((r) => r[m as keyof Sample]))])
  ) as Sample;
  report[path] = { gated, runs };
  return gated;
}

/**
 * PERF_RECORD=1 measures and writes the report without gating — how the
 * ceilings in budget.json were first taken. The CI job never sets it.
 */
const RECORD_ONLY = process.env.PERF_RECORD === "1";

function assertWithinBudget(path: string, sample: Sample) {
  if (RECORD_ONLY) return;
  const over = GATED.flatMap((metric) => {
    const key = `${path}.${metric}`;
    const ceiling = budget[key];
    if (ceiling === undefined) return [`${key}: no ceiling in budget.json (measured ${sample[metric]})`];
    return sample[metric] > ceiling ? [`${key}: ${sample[metric]} > ceiling ${ceiling}`] : [];
  });
  expect(over, `perf budget exceeded on ${path}:\n  ${over.join("\n  ")}`).toEqual([]);
}

test.beforeAll(async ({ browser }) => {
  const context = await browser.newContext();

  // Counters live in the page from the first byte, so they survive the
  // app's own full-page redirects and are there before React mounts.
  await context.addInitScript(() => {
    window.__meshPerfMutations = 0;
    const start = () =>
      new MutationObserver((records) => {
        window.__meshPerfMutations = (window.__meshPerfMutations ?? 0) + records.length;
      }).observe(document, {
        subtree: true,
        childList: true,
        attributes: true,
        characterData: true,
      });
    if (document.documentElement) start();
    else document.addEventListener("readystatechange", start, { once: true });
  });

  // Live updates would make the counters depend on whatever else happened on
  // the API during the run. The socket is answered locally and never talks
  // to the server: the app sees a connected, silent channel.
  await context.routeWebSocket(/\/ws(\?|$)/, () => {});

  page = await context.newPage();
  page.on("request", (r) => {
    const url = new URL(r.url());
    if (url.pathname.startsWith("/api/")) inflight.set(r, `${r.method()} ${url.pathname}${url.search}`);
  });
  const done = (r: object) => {
    inflight.delete(r);
  };
  page.on("requestfinished", done);
  page.on("requestfailed", done);
  // A request the previous document still had open when page.goto replaced
  // it gets neither event (measured: GET /api/v1/workspaces left "in flight"
  // forever, 2 runs in 5). The new document's own requests start after the
  // commit, so dropping everything here loses nothing that belongs to it.
  page.on("framenavigated", (frame) => {
    if (frame === page.mainFrame()) inflight.clear();
  });

  cdp = await context.newCDPSession(page);
  await cdp.send("Performance.enable");
  await cdp.send("Emulation.setCPUThrottlingRate", { rate: 4 });

  // One login per run, through the UI, exactly as a person does it.
  await page.goto("/login");
  await page.getByLabel(/email/i).fill(fixture.email);
  await page.getByLabel(/password/i).fill(fixture.password);
  const loginResponse = page.waitForResponse((r) => r.url().endsWith("/api/v1/auth/login"));
  await page.getByRole("button", { name: /sign in|log in/i }).click();
  const login = await loginResponse;
  expect(login.status(), "fixture login must succeed — was perf/seed-fixture.mjs run?").toBe(200);
  const token = ((await login.json()) as { tokens: { access_token: string } }).tokens.access_token;
  await page.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 30_000 });

  // Clean slate for comment.send: comments from an earlier local run would
  // otherwise make the thread longer every time. The token is the one the
  // login just returned: calling /auth/refresh here would rotate the one-shot
  // refresh cookie underneath the app.
  authHeaders = { Authorization: `Bearer ${token}` };
});

async function wipeComments() {
  const list = await page.request.get(
    `/api/v1/tasks/${fixture.comment_task_id}/comments?page_size=200`,
    { headers: authHeaders }
  );
  expect(list.status(), "listing the comment task's comments must succeed").toBe(200);
  const items = ((await list.json()) as { items?: { id: string }[] }).items ?? [];
  for (const c of items) {
    const del = await page.request.delete(`/api/v1/comments/${c.id}`, { headers: authHeaders });
    expect(del.ok(), `deleting comment ${c.id} must succeed`).toBe(true);
  }
}

const boardUrl = () => `/w/${fixture.ws_slug}/p/${fixture.project_slug}`;
const firstCard = () => page.getByText("Perf fixture task 001", { exact: true });

test.afterAll(async () => {
  writeFileSync(
    resolve(here, "..", "perf-counters-report.json"),
    JSON.stringify({ commit: process.env.CI_COMMIT_SHA ?? "local", paths: report }, null, 2)
  );
});

test("board.open — open the project board from the sidebar", async () => {
  const sample = await measureRepeated(
    "board.open",
    async () => {
      await page.goto(`/w/${fixture.ws_slug}/dashboard`);
      await expect(page.getByRole("link", { name: /perf fixture/i }).first()).toBeVisible();
      // The signed-in shell warms the board chunk on idle (AppLayout →
      // prefetch-next-route.ts). Wait for it instead of hoping the idle
      // callback fires inside the 500 ms quiet window: if it landed during
      // the click, its 32 <head> links would be counted as the click's own.
      // And if the warm-up stops working, this fails by name rather than
      // as a mystery +31 on dom_mutations.
      await waitForChunkFetched(page, BOARD_CHUNK);
    },
    async () => {
      await page.getByRole("link", { name: /perf fixture/i }).first().click();
      await expect(firstCard()).toBeVisible();
    }
  );
  expect(sample.react_commits, "the Profiler must see commits — 0 means a non-profiling build").toBeGreaterThan(0);
  assertWithinBudget("board.open", sample);
});

test("task.open — open a card from the board", async () => {
  const sample = await measureRepeated(
    "task.open",
    async () => {
      await page.goto(boardUrl());
      await expect(firstCard()).toBeVisible();
      // Same race as board.open's setup, mirrored: this setup lands directly
      // on the board (board chunk already loaded as part of the navigation
      // itself), but AppLayout's warm-up still has to fetch the dashboard
      // chunk it hasn't seen yet — wait for that before starting the click.
      await waitForChunkFetched(page, DASHBOARD_CHUNK);
    },
    async () => {
      await firstCard().click();
      await page.waitForURL(`**/t/${fixture.open_task_id}`);
      await expect(page.getByRole("heading", { name: "Perf fixture task 001" })).toBeVisible();
    }
  );
  assertWithinBudget("task.open", sample);
});

test("view.switch — board → list", async () => {
  const sample = await measureRepeated(
    "view.switch",
    async () => {
      await page.goto(boardUrl());
      await expect(firstCard()).toBeVisible();
      // Same race as task.open's setup — see the comment there.
      await waitForChunkFetched(page, DASHBOARD_CHUNK);
    },
    async () => {
      await page.getByRole("button", { name: "List", exact: true }).click();
      await page.waitForURL(`**/p/${fixture.project_slug}/list`);
      await expect(firstCard()).toBeVisible();
    }
  );
  assertWithinBudget("view.switch", sample);
});

test("comment.send — post a comment on a task", async () => {
  const text = "perf fixture comment";
  // The composer is a rich-text editor; its placeholder is not exposed to the
  // accessibility tree, so find it as the textbox of the form that owns the
  // Comment button.
  const form = () =>
    page.locator("form").filter({ has: page.getByRole("button", { name: "Comment", exact: true }) });
  const sample = await measureRepeated(
    "comment.send",
    async () => {
      // Same thread length every time — an earlier run's comment would make
      // the thread, and every counter on it, grow run by run.
      await wipeComments();
      await page.goto(`${boardUrl()}/t/${fixture.comment_task_id}`);
      const box = form().getByRole("textbox");
      await expect(box).toBeVisible();
      // Unlike task.open/view.switch, this setup never navigates through the
      // board — it goes straight to the task-detail URL — so AppLayout's
      // warm-up still has to fetch BOTH the dashboard and the board chunk it
      // hasn't seen yet. Wait for both before starting the action.
      await waitForChunkFetched(page, DASHBOARD_CHUNK);
      await waitForChunkFetched(page, BOARD_CHUNK);
      await box.click();
      await page.keyboard.type(text);
      await expect(form().getByRole("button", { name: "Comment", exact: true })).toBeEnabled();
    },
    async () => {
      await form().getByRole("button", { name: "Comment", exact: true }).click();
      await expect(page.getByText(text, { exact: true })).toBeVisible();
    }
  );
  assertWithinBudget("comment.send", sample);
});

declare global {
  interface Window {
    __meshPerfCommits?: Record<string, number>;
    __meshPerfMutations?: number;
  }
}
