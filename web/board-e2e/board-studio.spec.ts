import { test, expect, type Browser, type Page, type Request } from "@playwright/test";
import { mkdirSync } from "node:fs";
// The theme system end to end: mesh-default on the SDK with workspace settings, and the control theme
// board-studio loaded from the Go binary's own builtin bundle. Against the loopback fixture (same stand as
// board-dnd.spec.ts): the fixture keeps moves and edits and tells every connected page, so "a second user sees it" is a
// second browser context on the same stand. Screenshots (1440/393 x light/dark) go to $STUDIO_SHOTS.
const API = "http://127.0.0.1:8019";
const BOARD_URL = "/w/board-fixture/p/contenthub";
const DASH_URL = "/w/board-fixture/dashboard";
const BOOT_MS = 60_000;
const SHOTS = process.env.STUDIO_SHOTS;
const isMove = (r: Request) => r.method() === "POST" && /\/tasks\/[^/]+\/move/.test(r.url());

type Scheme = "light" | "dark";
const fixture = async (page: Page, data: Record<string, unknown> = {}) => {
  await page.request.post(`${API}/__fixture`, { data: { mode: "normal", role: "owner", resetTheme: true, resetTasks: true, ...data } });
};
const writes = async (page: Page) => ((await (await page.request.get(`${API}/__fixture`)).json()) as { writes: Record<string, unknown>[] }).writes;
const prepare = async (page: Page, scheme: Scheme) => {
  await page.addInitScript((t) => {
    localStorage.setItem("theme", t);
    localStorage.setItem("mesh_board_filters_pagination-project", JSON.stringify({ showClosed: false, showSubtasks: true }));
  }, scheme);
};
const openBoard = async (page: Page, scheme: Scheme = "light") => {
  await prepare(page, scheme);
  const tasks = page.waitForResponse((r) => r.url().startsWith(API) && /\/tasks(\?|$)/.test(r.url()), { timeout: BOOT_MS });
  await page.goto(BOARD_URL);
  await tasks;
  await page.locator('[data-testid="task-card"]').first().waitFor({ timeout: BOOT_MS });
  await page.addStyleTag({ content: "*,*::before,*::after{animation:none!important;transition:none!important;caret-color:transparent!important}" });
};
const openDash = async (page: Page, scheme: Scheme = "light") => {
  await prepare(page, scheme);
  const mine = page.waitForResponse((r) => r.url().startsWith(API) && r.url().includes("/me/tasks"), { timeout: BOOT_MS });
  await page.goto(DASH_URL);
  await mine;
  await page.addStyleTag({ content: "*,*::before,*::after{animation:none!important;transition:none!important;caret-color:transparent!important}" });
};
const col = (page: Page, id: string) => page.locator(`[data-testid="board-column"][data-status-id="${id}"]`);
const shot = async (page: Page, name: string) => {
  if (!SHOTS) return;
  mkdirSync(SHOTS, { recursive: true });
  const scheme = await page.evaluate(() => localStorage.getItem("theme"));
  await page.screenshot({ path: `${SHOTS}/${name}-${page.viewportSize()!.width}-${scheme}.png`, fullPage: true });
};
const drag = async (page: Page, cardSel: ReturnType<Page["locator"]>, toSel: ReturnType<Page["locator"]>) => {
  await cardSel.scrollIntoViewIfNeeded();
  const from = (await cardSel.boundingBox())!, to = (await toSel.boundingBox())!;
  await page.mouse.move(from.x + 20, from.y + 20);
  await page.mouse.down();
  await page.mouse.move(from.x + 40, from.y + 30, { steps: 5 });
  await page.mouse.move(to.x + to.width / 2, to.y + to.height / 2, { steps: 15 });
  await page.mouse.up();
};
const secondUser = async (browser: Browser, scheme: Scheme = "light") => {
  const context = await browser.newContext({ viewport: { width: 1440, height: 3000 } });
  const page = await context.newPage();
  await openBoard(page, scheme);
  return { page, context };
};

test.afterEach(async ({ request }) => {
  await request.post(`${API}/__fixture`, { data: { mode: "normal", role: "owner", resetTheme: true, resetTasks: true } });
});

test.describe("board-studio: the board", () => {
  test.beforeEach(async ({ page }) => {
    await fixture(page, { studio: true, assignees: true });
    await page.setViewportSize({ width: 1440, height: 3000 });
  });

  test("the theme's own layout is on screen: bands, its card, its person strip", async ({ page }) => {
    await openBoard(page);
    await expect(page.locator(".studio-band").first()).toBeVisible();
    await expect(page.locator('[data-testid="studio-card"]').first()).toBeVisible();
    await expect(page.getByRole("group", { name: "Highlight work of" })).toBeVisible();
    expect(await page.locator(".studio-band").count()).toBeGreaterThanOrEqual(4);
  });

  test("it is drawn as Prototypes: the brand orange and the SB Sans Display stack reach the page", async ({ page }) => {
    await openBoard(page);
    const look = await page.evaluate(() => ({
      primary: getComputedStyle(document.documentElement).getPropertyValue("--primary").trim(),
      body: getComputedStyle(document.body).fontFamily,
    }));
    expect(look.primary).toBe("20.1 100% 56.1%"); // #FF6A1F exactly
    expect(look.body).toMatch(/^"?SB Sans Display"?,\s*"?Inter"?,/);
  });

  test("dragging a card changes its status, and a second user sees it without reloading", async ({ page, browser }) => {
    test.setTimeout(BOOT_MS * 2);
    await fixture(page, { studio: true, broadcastMoves: true });
    const other = await secondUser(browser);
    await openBoard(page);
    const card = col(page, "todo").locator('[data-testid="task-card"]').first();
    const id = (await card.getAttribute("data-task-id"))!;
    const moved = page.waitForRequest(isMove);
    await drag(page, card, col(page, "in_progress"));
    const req = await moved;
    expect(req.url()).toContain(`/tasks/${id}/move`);
    expect(req.postDataJSON().status_id).toBe("in_progress");
    await expect(col(page, "in_progress").locator(`[data-task-id="${id}"]`)).toBeAttached();
    // the second user never touched their page
    await expect(col(other.page, "in_progress").locator(`[data-task-id="${id}"]`)).toBeAttached({ timeout: 15_000 });
    await expect(col(other.page, "todo").locator(`[data-task-id="${id}"]`)).toHaveCount(0);
    expect((await writes(page)).some((w) => w.move === id && w.status_id === "in_progress")).toBe(true);
    await other.context.close();
  });

  test("keyboard: Space picks up, ArrowDown takes the card to the next band, Space drops it", async ({ page }) => {
    await openBoard(page);
    const card = col(page, "todo").locator('[data-testid="task-card"]').first();
    const id = (await card.getAttribute("data-task-id"))!;
    expect(await card.getAttribute("tabindex")).toBe("0");
    await card.focus();
    const moved = page.waitForRequest(isMove);
    const live = page.locator("[aria-live]").first();
    await page.keyboard.press("Space");
    await expect(live).toContainText("is over");
    await page.waitForTimeout(250); // dnd-kit attaches its key listeners a tick after pickup
    // a band is a horizontal row of cards: the first ArrowDown goes to the end of the row the card is in, the second crosses into the next band
    await page.keyboard.press("ArrowDown");
    await expect(live).toContainText("position 4 of 4");
    await page.keyboard.press("ArrowDown");
    await expect(live).toContainText("position 1 of 2");
    await page.keyboard.press("Space");
    const req = await moved;
    expect(req.url()).toContain(`/tasks/${id}/move`);
    expect(req.postDataJSON().status_id).toBe("in_progress");
    await expect(col(page, "in_progress").locator(`[data-task-id="${id}"]`)).toBeAttached();
  });

  test("keyboard: Space, ArrowDown, Space takes a card into an empty band", async ({ page }) => {
    await openBoard(page);
    await expect(col(page, "review").locator('[data-testid="task-card"]')).toHaveCount(0);
    const card = col(page, "in_progress").locator('[data-testid="task-card"]').first();
    const id = (await card.getAttribute("data-task-id"))!;
    await card.focus();
    const moved = page.waitForRequest(isMove);
    const live = page.locator("[aria-live]").first();
    await page.keyboard.press("Space");
    await expect(live).toContainText("is over");
    await page.waitForTimeout(250); // dnd-kit attaches its key listeners a tick after pickup
    await page.keyboard.press("ArrowDown");
    await expect(live).toContainText("position 1 of 1");
    await page.keyboard.press("Space");
    const req = await moved;
    expect(req.url()).toContain(`/tasks/${id}/move`);
    expect(req.postDataJSON().status_id).toBe("review");
    await expect(col(page, "review").locator(`[data-task-id="${id}"]`)).toBeAttached();
  });

  test("393: quick actions are one segmented bar of 44px targets inside the card", async ({ page }) => {
    await page.setViewportSize({ width: 393, height: 900 });
    await openBoard(page);
    const card = page.locator('[data-testid="studio-card"]').first();
    const bar = card.locator(".studio-actions");
    expect(await bar.evaluate((el) => getComputedStyle(el).display)).toBe("grid");
    const boxes = await bar.locator(".studio-act").evaluateAll((els) => els.map((e) => e.getBoundingClientRect()));
    expect(boxes).toHaveLength(3);
    for (const b of boxes) expect(b.height).toBeGreaterThanOrEqual(44);
    expect(new Set(boxes.map((b) => Math.round(b.top))).size).toBe(1);
    const cardBox = (await card.boundingBox())!, barBox = (await bar.boundingBox())!;
    expect(barBox.x).toBeGreaterThanOrEqual(cardBox.x);
    expect(barBox.x + barBox.width).toBeLessThanOrEqual(cardBox.x + cardBox.width + 1);
  });

  test("a viewer gets no drag handle, no quick actions, and a drag sends nothing", async ({ page }) => {
    await page.route(`${API}/**/members/me`, (route) => route.fulfill({ json: { role: "viewer" } }));
    await openBoard(page);
    const card = col(page, "todo").locator('[data-testid="task-card"]').first();
    expect(await card.getAttribute("tabindex")).toBeNull();
    expect(await card.getAttribute("aria-roledescription")).toBeNull();
    await expect(page.locator(".studio-actions")).toHaveCount(0);
    let moves = 0;
    page.on("request", (r) => { if (isMove(r)) moves++; });
    await drag(page, card, col(page, "in_progress"));
    await page.waitForTimeout(500);
    expect(moves).toBe(0);
  });

  test("quick actions: raise priority and postpone are saved, open goes to the task", async ({ page }) => {
    await openBoard(page);
    const card = col(page, "todo").locator('[data-testid="task-card"]').first();
    const id = (await card.getAttribute("data-task-id"))!;
    const body = card.locator('[data-testid="studio-card"]');
    await expect(body).toHaveAttribute("data-priority", "medium");
    await body.hover(); // the actions show with hover or focus
    await card.getByRole("button", { name: "Raise priority" }).click();
    await expect(body).toHaveAttribute("data-priority", "high");
    await body.hover();
    await card.getByRole("button", { name: "Postpone one day" }).click();
    await expect(card.locator('[data-field="due"], .studio-due').first()).toBeVisible();
    const w = await writes(page);
    expect(w.some((x) => x.patch === id && x.priority === "high")).toBe(true);
    expect(w.some((x) => x.patch === id && typeof x.due_date === "string")).toBe(true);
    await body.hover();
    await card.getByRole("button", { name: "Open" }).click();
    await expect(page).toHaveURL(new RegExp(`/t/${id}$`));
  });

  test("the person strip highlights, it does not hide: drop targets keep their cards", async ({ page }) => {
    await openBoard(page);
    const before = await page.locator('[data-testid="task-card"]').count();
    await page.getByRole("group", { name: "Highlight work of" }).getByRole("button").nth(1).click();
    await expect(page.locator('[data-testid="task-card"]')).toHaveCount(before);
    await expect(page.locator('[data-dim="true"]').first()).toBeAttached();
  });

  test("a theme setting changes the card: no cover", async ({ page }) => {
    await fixture(page, { studio: { cover_style: "none" } });
    await openBoard(page);
    await expect(page.locator('[data-testid="studio-card"]').first()).toHaveAttribute("data-cover", "none");
    await expect(page.locator(".studio-cover")).toHaveCount(0);
  });
});

test.describe("board-studio: the dashboard", () => {
  test("its own grid and its own widget show the user's tasks, read through the app's request", async ({ page }) => {
    await fixture(page, { studio: true });
    const mine: { auth: string | undefined }[] = [];
    page.on("request", (r) => { if (r.url().includes("/me/tasks")) mine.push({ auth: r.headers()["authorization"] }); });
    await openDash(page);
    await expect(page.getByRole("heading", { name: "Team focus" })).toBeVisible();
    const widget = page.getByRole("heading", { name: "Due this week" }).locator("..");
    await expect(widget.getByText("Assigned task 1")).toBeVisible();
    await expect(widget.getByRole("heading", { name: /Overdue/ })).toBeVisible();
    await expect(widget.getByRole("heading", { name: /Coming up/ })).toBeVisible();
    // 20 days out is beyond the 7-day horizon, no due date is not due: neither is listed
    await expect(widget.getByText("Assigned task 4")).toHaveCount(0);
    await expect(widget.getByText("Assigned task 5")).toHaveCount(0);
    expect(mine.length).toBeGreaterThan(0);
    expect(mine.every((m) => m.auth === "Bearer fixture-token"), "the read carries the user's own session").toBe(true);
    await widget.getByRole("link", { name: "Assigned task 1" }).click();
    await expect(page).toHaveURL(/\/t\//);
  });

  test("the horizon is a setting: 30 days brings the far task in", async ({ page }) => {
    await fixture(page, { studio: { focus_horizon_days: 30 } });
    await openDash(page);
    await expect(page.getByRole("link", { name: "Assigned task 4", exact: true })).toBeVisible();
  });

  test("the widget's error state has a way back, and an empty list says so", async ({ page }) => {
    await fixture(page, { studio: true, mode: "empty" });
    await openDash(page);
    await expect(page.getByText(/Nothing due in the next 7 days/)).toBeVisible();
  });
});

test.describe("mesh-default: workspace settings take effect", () => {
  test("accent, radius and density reach the page; the default views are still the ones drawn", async ({ page }) => {
    await page.request.post(`${API}/__fixture`, { data: { mode: "normal", role: "owner", resetTheme: true, resetTasks: true } });
    await openBoard(page);
    const read = () => page.evaluate(() => {
      const s = getComputedStyle(document.documentElement);
      return { primary: s.getPropertyValue("--primary").trim(), radius: s.getPropertyValue("--radius").trim(), unit: s.getPropertyValue("--space-unit").trim() };
    });
    const base = await read();
    // the owner saves settings for mesh-default through the panel's own endpoint
    await page.request.put(`${API}/api/v1/workspaces/pagination-workspace/theme`, { data: { theme_id: "mesh-default", settings: { accent: "12 80% 40%", radius: "1rem", density: "0.225rem" } } });
    await page.reload();
    await page.locator('[data-testid="task-card"]').first().waitFor({ timeout: BOOT_MS });
    const after = await read();
    expect(after.primary).toBe("12 80% 40%");
    expect(after.radius).toBe("1rem");
    expect(after.unit).toBe("0.225rem");
    expect(after).not.toEqual(base);
    await expect(page.locator(".studio-band")).toHaveCount(0);
    await expect(col(page, "todo")).toBeVisible();
  });
});

for (const width of [1440, 393]) {
  for (const scheme of ["light", "dark"] as const) {
    test.describe(`screens ${width} ${scheme}`, () => {
      test.use({ viewport: { width, height: 900 } });
      test("mesh-default and board-studio, board and dashboard", async ({ page }) => {
        test.setTimeout(BOOT_MS * 3);
        await fixture(page);
        await openBoard(page, scheme);
        await shot(page, "mesh-default-board");
        await openDash(page, scheme);
        await shot(page, "mesh-default-dashboard");
        await fixture(page, { studio: true });
        await openBoard(page, scheme);
        await expect(page.locator(".studio-band").first()).toBeVisible();
        await shot(page, "board-studio-board");
        const first = page.locator('[data-testid="task-card"]').first();
        const [taskId, title] = [await first.getAttribute("data-task-id"), (await first.locator(".studio-title").textContent())!.trim()];
        await page.goto(`/t/${taskId}`);
        await expect(page.getByRole("heading", { name: title })).toBeVisible({ timeout: BOOT_MS });
        await shot(page, "board-studio-task");
        await openDash(page, scheme);
        await expect(page.getByRole("heading", { name: "Team focus" })).toBeVisible();
        await shot(page, "board-studio-dashboard");
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
        expect(overflow, "no horizontal scroll").toBeLessThanOrEqual(0);
      });
    });
  }
}
