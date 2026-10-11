import { test, expect, type APIRequestContext } from "@playwright/test";
const URL = "/w/board-fixture/p/contenthub";
const FIXTURE = "http://127.0.0.1:8019/__fixture";
type Call = { status: string | null; page: number; list_revision: string | null };
const taskCalls = async (request: APIRequestContext): Promise<Call[]> =>
  (await request.get(FIXTURE)).json().then((s) => s.taskCalls);
test.beforeEach(async ({ page, request }) => {
  await request.post(FIXTURE, { data: { mode: "normal" } });
  await page.addInitScript(() => localStorage.setItem("mesh_board_filters_pagination-project", JSON.stringify({ showClosed: true, showSubtasks: true })));
});

// ContentHub fixture: 331 tasks — 39 open, 282 Done, 10 Cancelled.
test("open work complete, Done is the latest 200 of 282 and loads more on demand", async ({ page, request }) => {
  const errors: string[] = []; page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(URL);
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
  await page.goto(URL);
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
  await page.goto(URL);
  await expect(page.getByRole("alert")).toContainText("Counts are unavailable");
  await expect(page.locator('[data-testid="board-column"]')).toHaveCount(0);
  await request.post(FIXTURE, { data: { mode: "normal" } });
  await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(249);
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("refresh error keeps the board with a visible warning", async ({ page, request }) => {
  await page.goto(URL);
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(249);
  await request.post(FIXTURE, { data: { mode: "error", event: true } });
  await expect(page.getByRole("alert")).toContainText("Showing the last complete list");
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(249);
});

test("a slow load never shows a partial board", async ({ page, request }) => {
  await request.post(FIXTURE, { data: { mode: "slow" } });
  await page.goto(URL);
  await expect.poll(async () => (await taskCalls(request)).length).toBeGreaterThan(0);
  await expect(page.locator('[data-testid="board-column"]')).toHaveCount(0);
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(249);
});

test("project switch discards the old load and an old debounced refresh", async ({ page, request }) => {
  await request.post(FIXTURE, { data: { mode: "slow" } });
  await page.goto(URL);
  await expect.poll(async () => (await taskCalls(request)).length).toBeGreaterThan(0);
  await request.post(FIXTURE, { data: { mode: "slow", event: true } });
  await page.locator('a[href="/w/board-fixture/p/other"]').first().click();
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(1);
  await expect(page.getByText("Other project's only task", { exact: true })).toBeVisible();
  // Wait for the old load and debounce to have had time to return.
  await page.waitForTimeout(1700);
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(1);
  await expect(page.getByText("Other project's only task", { exact: true })).toBeVisible();
});
