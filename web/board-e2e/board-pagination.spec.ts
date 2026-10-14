import { test, expect, type APIRequestContext, type BrowserContext, type Page } from "@playwright/test";
const BOARD_URL = "/w/board-fixture/p/contenthub";
const API = "http://127.0.0.1:8019";
const PROJECT_ID = "pagination-project";
const FIXTURE = `${API}/__fixture`;
// One-off cost of the stand's first load (vite transforms, the auth/workspace/
// project chain before the first /tasks request). Waited for by event, so it is
// a ceiling, not a delay; the 5s assertion default stays for the board itself.
const BOOT_MS = 60_000;
// Red control: BOARD_E2E_COLD_START_MS stalls the first API request of every
// context (the app boots after `load`, so this is what a cold runner looks
// like to the board). A spec that races the first render fails.
// Every API request of a test's page is tagged `e2e=<tag>`, so the fixture log
// can be read per test: a request still in flight from the previous test's page
// lands in the fixture after the reset and must not count as this test's load.
const instrument = async (context: BrowserContext, tag: string) => {
  const ms = Number(process.env.BOARD_E2E_COLD_START_MS ?? 0);
  let first = true;
  await context.route(`${API}/**`, async (route) => {
    if (ms && first) { first = false; await new Promise((r) => setTimeout(r, ms)); }
    const u = new URL(route.request().url());
    u.searchParams.set("e2e", tag);
    await route.continue({ url: u.toString() });
  });
};
// Navigates and returns once the first tasks response has arrived: the readiness event.
// `until: "request"` is for the one test that inspects the board while the response is still pending.
const openBoard = async (page: Page, url = BOARD_URL, until: "request" | "response" = "response") => {
  const isTasks = (r: { url(): string }) => r.url().startsWith(API) && /\/tasks(\?|$)/.test(r.url());
  const asked = until === "request" ? page.waitForRequest(isTasks, { timeout: BOOT_MS }) : page.waitForResponse(isTasks, { timeout: BOOT_MS });
  await page.goto(url);
  await asked;
};
type Call = { tag: string | null; status: string | null; page: number; list_revision: string | null };
let currentTag = "warmup";
const taskCalls = async (request: APIRequestContext): Promise<Call[]> =>
  (await request.get(FIXTURE)).json().then((s) => s.taskCalls.filter((c: Call) => c.tag === currentTag));
test.beforeAll(async ({ browser }) => {
  // Pay the cold start once, outside any assertion.
  test.setTimeout(BOOT_MS + 30_000);
  const context = await browser.newContext({ baseURL: "http://127.0.0.1:3019" });
  await instrument(context, "warmup");
  const page = await context.newPage();
  await openBoard(page);
  await page.locator('[data-testid="board-column"]').first().waitFor({ timeout: BOOT_MS });
  await context.close();
});
test.beforeEach(async ({ page, request, context }, info) => {
  currentTag = `${info.testId}-${info.repeatEachIndex}`;
  await instrument(context, currentTag);
  await request.post(FIXTURE, { data: { mode: "normal" } });
  await page.addInitScript(() => localStorage.setItem("mesh_board_filters_pagination-project", JSON.stringify({ showClosed: true, showSubtasks: true })));
});

// ContentHub fixture: 331 tasks — 39 open, 282 Done, 10 Cancelled.
test("open work complete, Done is the latest 200 of 282 and loads more on demand", async ({ page, request }) => {
  const errors: string[] = []; page.on("pageerror", (e) => errors.push(e.message));
  await openBoard(page);
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(39 + 200 + 10);
  const done = page.locator('[data-testid="board-column"][data-status-id="done"]').locator("..");
  await expect(done.getByTestId("column-count")).toHaveText("282");
  await expect(done.getByTestId("closed-slice")).toContainText("Showing latest 200 of 282");
  await expect(page.getByText("Done from page two", { exact: true })).toHaveCount(0);
  // A filter on a partial column: "at least" what matched among loaded cards.
  await page.getByRole("textbox", { name: "Search tasks" }).fill("Content task 00");
  await expect(done.getByTestId("column-count")).toHaveText("10+");
  await page.getByRole("textbox", { name: "Search tasks" }).fill("");
  await done.getByRole("button", { name: "Load more" }).click();
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(331);
  await expect(page.getByText("Done from page two", { exact: true })).toBeAttached();
  await expect(done.getByTestId("closed-slice")).toHaveCount(0);
  // Four non-Done tasks updated + WS event: the refresh keeps both Done pages.
  await request.post(FIXTURE, { data: { reordered: true, event: true } });
  await expect.poll(async () => (await taskCalls(request)).length).toBe(4);
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(331);
  await expect(done.getByTestId("column-count")).toHaveText("282");
  await page.getByRole("textbox", { name: "Search tasks" }).fill("Done from page two");
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(1);
  await expect(done.getByTestId("column-count")).toHaveText("1");
  await page.getByRole("textbox", { name: "Search tasks" }).fill("");
  await page.getByRole("button", { name: "Subtasks", exact: true }).click();
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(330);
  await expect(done.getByTestId("column-count")).toHaveText("281");
  await page.getByRole("button", { name: "Closed", exact: true }).click();
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(39);
  await page.getByRole("button", { name: "Group: Status", exact: true }).click();
  await page.getByRole("menuitem", { name: "Priority", exact: true }).click();
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(39);
  expect(errors).toEqual([]);
});

test("a load is one open query + one page per closed status, never revision-pinned", async ({ page, request }) => {
  await openBoard(page);
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(249);
  const calls = await taskCalls(request);
  expect(calls.map((c) => `${c.status}#${c.page}`).sort()).toEqual(
    ["backlog,todo,in_progress,review#1", "cancelled#1", "done#1"]);
  expect(calls.every((c) => c.list_revision === null)).toBe(true);
  // A WS refresh repeats the bounded plan instead of walking the project.
  await request.post(FIXTURE, { data: { event: true } });
  await expect.poll(async () => (await taskCalls(request)).length).toBe(3);
  await page.waitForTimeout(1500);
  expect((await taskCalls(request)).length).toBe(3);
});

test("initial error hides counters and Retry restores the board", async ({ page, request }) => {
  await request.post(FIXTURE, { data: { mode: "error" } });
  await openBoard(page);
  await expect(page.getByRole("alert")).toContainText("Counts are unavailable");
  await expect(page.locator('[data-testid="board-column"]')).toHaveCount(0);
  await request.post(FIXTURE, { data: { mode: "normal" } });
  await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(249);
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("refresh error keeps the board with a visible warning", async ({ page, request }) => {
  await openBoard(page);
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(249);
  await request.post(FIXTURE, { data: { mode: "error", event: true } });
  await expect(page.getByRole("alert")).toContainText("Showing the last complete list");
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(249);
});

test("a slow load never shows a partial board", async ({ page, request }) => {
  await request.post(FIXTURE, { data: { mode: "slow" } });
  await openBoard(page, BOARD_URL, "request");
  await expect(page.locator('[data-testid="board-column"]')).toHaveCount(0);
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(249);
});

test("project switch discards the old load and an old debounced refresh", async ({ page, request }) => {
  await request.post(FIXTURE, { data: { mode: "slow" } });
  await openBoard(page, BOARD_URL, "request");
  await request.post(FIXTURE, { data: { mode: "slow", event: true } });
  await page.locator('a[href="/w/board-fixture/p/other"]').first().click();
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(1);
  await expect(page.getByText("Other project's only task", { exact: true })).toBeVisible();
  // Wait for the old load and debounce to have had time to return.
  await page.waitForTimeout(1700);
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(1);
  await expect(page.getByText("Other project's only task", { exact: true })).toBeVisible();
});

test("fixture calls are counted per test: a stray request from another page is ignored", async ({ page, request }) => {
  await openBoard(page);
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(249);
  const own = (await taskCalls(request)).length;
  const stray = await request.get(`${API}/api/v1/projects/${PROJECT_ID}/tasks?status=done&e2e=stray`);
  expect(stray.ok()).toBe(true);
  // Positive control: the fixture did log the stray call, the filter dropped it.
  const all = (await (await request.get(FIXTURE)).json()).taskCalls;
  expect(all.some((c: Call) => c.tag === "stray")).toBe(true);
  expect((await taskCalls(request)).length).toBe(own);
});
