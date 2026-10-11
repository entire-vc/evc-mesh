import { test, expect } from "@playwright/test";
const URL = "/w/board-fixture/p/contenthub";
test.beforeEach(async ({ page, request }) => {
  await request.post("http://127.0.0.1:8019/__fixture", { data: { mode: "normal" } });
  await page.addInitScript(() => localStorage.setItem("mesh_board_filters_pagination-project", JSON.stringify({ showClosed: true, showSubtasks: true })));
});

test("all 331 cards, Done 282, page two accessible and stable after non-Done update", async ({ page, request }) => {
  const errors: string[] = []; page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(URL);
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(331);
  const done = page.locator('[data-testid="board-column"][data-status-id="done"]').locator("..");
  await expect(done).toContainText("282");
  await expect(page.getByText("Done from page two", { exact: true })).toBeAttached();
  await request.post("http://127.0.0.1:8019/__fixture", { data: { reordered: true, revision: 48, event: true } });
  await expect.poll(async () => (await request.get("http://127.0.0.1:8019/__fixture")).json().then((s) => s.pageTwoCalls)).toBeGreaterThan(0);
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(331);
  await expect(done).toContainText("282");
  await page.getByRole("textbox", { name: "Search tasks" }).fill("Done from page two");
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(1);
  await expect(done).toContainText("1");
  await page.getByRole("textbox", { name: "Search tasks" }).fill("");
  await page.getByRole("button", { name: "Subtasks", exact: true }).click();
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(330);
  await expect(done).toContainText("281");
  await page.getByRole("button", { name: "Closed", exact: true }).click();
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(39);
  await page.getByRole("button", { name: "Group: Status", exact: true }).click();
  await page.getByRole("menuitem", { name: "Priority", exact: true }).click();
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(39);
  expect(errors).toEqual([]);
});

test("initial page-two error hides counters and Retry restores all cards", async ({ page, request }) => {
  await request.post("http://127.0.0.1:8019/__fixture", { data: { mode: "error" } });
  await page.goto(URL);
  await expect(page.getByRole("alert")).toContainText("Counts are unavailable");
  await expect(page.locator('[data-testid="board-column"]')).toHaveCount(0);
  await request.post("http://127.0.0.1:8019/__fixture", { data: { mode: "normal" } });
  await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(331);
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("refresh error keeps full snapshot with a visible warning", async ({ page, request }) => {
  await page.goto(URL);
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(331);
  await request.post("http://127.0.0.1:8019/__fixture", { data: { mode: "error", event: true } });
  await expect(page.getByRole("alert")).toContainText("Showing the last complete list");
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(331);
});

test("stale revision recovers; slow pagination never shows a partial board", async ({ page, request }) => {
  await request.post("http://127.0.0.1:8019/__fixture", { data: { mode: "slow" } });
  await page.goto(URL);
  await expect.poll(async () => (await request.get("http://127.0.0.1:8019/__fixture")).json().then((s) => s.pageTwoCalls)).toBeGreaterThan(0);
  await expect(page.locator('[data-testid="board-column"]')).toHaveCount(0);
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(331);
  await request.post("http://127.0.0.1:8019/__fixture", { data: { mode: "stale", event: true } });
  await expect.poll(async () => (await request.get("http://127.0.0.1:8019/__fixture")).json().then((s) => s.pageTwoCalls)).toBe(2);
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(331);
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("project switch discards old pagination and an old debounced refresh", async ({ page, request }) => {
  await request.post("http://127.0.0.1:8019/__fixture", { data: { mode: "slow" } });
  await page.goto(URL);
  await expect.poll(async () => (await request.get("http://127.0.0.1:8019/__fixture")).json().then((s) => s.pageTwoCalls)).toBeGreaterThan(0);
  await request.post("http://127.0.0.1:8019/__fixture", { data: { mode: "slow", event: true } });
  await page.locator('a[href="/w/board-fixture/p/other"]').first().click();
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(1);
  await expect(page.getByText("Other project's only task", { exact: true })).toBeVisible();
  // Wait for the old page two and debounce to have had time to return.
  await page.waitForTimeout(1700);
  await expect(page.locator('[data-testid="task-card"]')).toHaveCount(1);
  await expect(page.getByText("Other project's only task", { exact: true })).toBeVisible();
});
