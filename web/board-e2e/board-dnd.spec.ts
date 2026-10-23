import { test, expect, type Page, type Request } from "@playwright/test";
// Board drag-and-drop on the shared primitives, against the loopback fixture
// (same stand as board-pagination.spec.ts). The move request IS the proof: a
// repaint is not a move.
const BOARD_URL = "/w/board-fixture/p/contenthub";
const API = "http://127.0.0.1:8019";
const BOOT_MS = 60_000;
const isMove = (r: Request) => r.method() === "POST" && /\/tasks\/[^/]+\/move/.test(r.url());

const openBoard = async (page: Page) => {
  await page.request.post(`${API}/__fixture`, { data: { mode: "normal" } });
  await page.addInitScript(() => localStorage.setItem("mesh_board_filters_pagination-project", JSON.stringify({ showClosed: false, showSubtasks: true })));
  const tasks = page.waitForResponse((r) => r.url().startsWith(API) && /\/tasks(\?|$)/.test(r.url()), { timeout: BOOT_MS });
  await page.goto(BOARD_URL);
  await tasks;
  await page.locator('[data-testid="task-card"]').first().waitFor({ timeout: BOOT_MS });
};
const col = (page: Page, id: string) => page.locator(`[data-testid="board-column"][data-status-id="${id}"]`);

test("pointer drag moves the card to the other column", async ({ page }) => {
  test.setTimeout(BOOT_MS + 30_000);
  await openBoard(page);
  const card = col(page, "backlog").locator('[data-testid="task-card"]').first();
  const id = await card.getAttribute("data-task-id");
  const from = (await card.boundingBox())!, to = (await col(page, "todo").boundingBox())!;
  const moved = page.waitForRequest(isMove);
  await page.mouse.move(from.x + 20, from.y + 20);
  await page.mouse.down();
  await page.mouse.move(from.x + 40, from.y + 30, { steps: 5 });
  await page.mouse.move(to.x + to.width / 2, to.y + to.height - 10, { steps: 15 });
  await page.mouse.up();
  const req = await moved;
  expect(req.url()).toContain(`/tasks/${id}/move`);
  expect(req.postDataJSON().status_id).toBe("todo");
  await expect(col(page, "todo").locator(`[data-task-id="${id}"]`)).toBeAttached();
});

test("keyboard: Space picks up, ArrowRight moves to the next column, Space drops", async ({ page }) => {
  test.setTimeout(BOOT_MS + 30_000);
  await openBoard(page);
  const card = col(page, "backlog").locator('[data-testid="task-card"]').first();
  const id = await card.getAttribute("data-task-id");
  expect(await card.getAttribute("tabindex")).toBe("0");
  await card.focus();
  const moved = page.waitForRequest(isMove);
  const live = page.locator("[aria-live]").first();
  await page.keyboard.press("Space");
  await expect(live).toContainText("is over");
  await page.waitForTimeout(250); // dnd-kit attaches its key listeners a tick after pickup
  await page.keyboard.press("ArrowRight");
  // The announcement is the event: the sensor has moved over the first card of Todo (4 cards).
  await expect(live).toContainText("position 1 of 4");
  await page.keyboard.press("Space");
  const req = await moved;
  expect(req.url()).toContain(`/tasks/${id}/move`);
  expect(req.postDataJSON().status_id).toBe("todo");
  await expect(col(page, "todo").locator(`[data-task-id="${id}"]`)).toBeAttached();
});

test("keyboard: ArrowDown inside a column moves the card below the next one", async ({ page }) => {
  test.setTimeout(BOOT_MS + 30_000);
  await openBoard(page);
  const cards = col(page, "backlog").locator('[data-testid="task-card"]');
  const first = await cards.nth(0).getAttribute("data-task-id");
  const second = await cards.nth(1).getAttribute("data-task-id");
  await cards.nth(0).focus();
  const moved = page.waitForRequest(isMove);
  const live = page.locator("[aria-live]").first();
  await page.keyboard.press("Space");
  await expect(live).toContainText("is over");
  await page.waitForTimeout(250); // dnd-kit attaches its key listeners a tick after pickup
  await page.keyboard.press("ArrowDown");
  await expect(live).toContainText("position 2 of");
  await page.keyboard.press("Space");
  const req = await moved;
  expect(req.url()).toContain(`/tasks/${first}/move`);
  expect(req.postDataJSON().status_id).toBe("backlog");
  const order = await col(page, "backlog").locator('[data-testid="task-card"]').evaluateAll((els) => els.slice(0, 2).map((e) => e.getAttribute("data-task-id")));
  expect(order).toEqual([second, first]);
});

test("keyboard: Escape cancels the move", async ({ page }) => {
  test.setTimeout(BOOT_MS + 30_000);
  await openBoard(page);
  const card = col(page, "backlog").locator('[data-testid="task-card"]').first();
  const id = await card.getAttribute("data-task-id");
  let moves = 0;
  page.on("request", (r) => { if (isMove(r)) moves++; });
  await card.focus();
  const live = page.locator("[aria-live]").first();
  await page.keyboard.press("Space");
  await expect(live).toContainText("is over");
  await page.waitForTimeout(250); // dnd-kit attaches its key listeners a tick after pickup
  await page.keyboard.press("ArrowRight");
  await expect(live).toContainText("position 1 of 4");
  await page.keyboard.press("Escape");
  await expect(live).toContainText("cancelled");
  await page.waitForTimeout(500);
  expect(moves).toBe(0);
  await expect(col(page, "backlog").locator(`[data-task-id="${id}"]`)).toBeAttached();
});

test("viewer: no drag handle on a card and a drag sends nothing", async ({ page }) => {
  test.setTimeout(BOOT_MS + 30_000);
  await page.route(`${API}/**/members/me`, (route) => route.fulfill({ json: { role: "viewer" } }));
  await openBoard(page);
  const card = col(page, "backlog").locator('[data-testid="task-card"]').first();
  expect(await card.getAttribute("tabindex")).toBeNull();
  expect(await card.getAttribute("aria-roledescription")).toBeNull();
  let moves = 0;
  page.on("request", (r) => { if (isMove(r)) moves++; });
  const from = (await card.boundingBox())!, to = (await col(page, "todo").boundingBox())!;
  await page.mouse.move(from.x + 20, from.y + 20);
  await page.mouse.down();
  await page.mouse.move(to.x + to.width / 2, to.y + 40, { steps: 15 });
  await page.mouse.up();
  await page.waitForTimeout(500);
  expect(moves).toBe(0);
});
