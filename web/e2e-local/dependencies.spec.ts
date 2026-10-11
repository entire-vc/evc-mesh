import { test, expect } from "@playwright/test";
import { readFileSync, writeFileSync } from "node:fs";

// Real local API/DB fixture only. Production data and workflow are never written.
const fixturePath = process.env.DEPENDENCY_FIXTURE;
test.skip(!fixturePath, "Requires the disposable 1000-task/3000-edge fixture");
test("read-only dependencies: bounded focus, expansion, back, task panel, responsive and performance", async ({ page, context }) => {
  const fixture = JSON.parse(readFileSync(fixturePath!, "utf8"));
  if (!["localhost", "127.0.0.1"].includes(new URL(fixture.url).hostname)) throw new Error("Local fixture required");
  const failures: string[] = [], mutations: string[] = [];
  page.on("pageerror", e => failures.push(e.message));
  page.on("response", r => { if (r.status() >= 500) failures.push(`HTTP ${r.status()} ${r.url()}`); });
  page.on("request", r => { if (r.url().includes("/api/v1/") && !["GET", "HEAD"].includes(r.method()) && !r.url().includes("/auth/")) mutations.push(r.method() + " " + r.url()); });
  const login = await context.request.post("/api/v1/auth/login", { data: {
    email: "local-stack@example.test", password: "LocalStack1", // pragma: allowlist secret
  } });
  expect(login.ok()).toBe(true);
  const token = (await login.json()).tokens.access_token;
  const headers = { Authorization: `Bearer ${token}` };
  const graphPath = `/api/v1/projects/${fixture.project.id}/dependency-graph`;
  const graph = await context.request.get(graphPath + "?focus=" + fixture.focus, { headers });
  const data = await graph.json();
  expect(data.tasks.length).toBeLessThanOrEqual(21);
  expect(data.before.has_more).toBe(true); expect(data.after.has_more).toBe(true);
  expect(data.before.ids).toHaveLength(10); expect(data.after.ids).toHaveLength(10);
  expect(data.before.total_count).toBe(23); expect(data.after.total_count).toBe(20);
  const overview = await context.request.get(graphPath + "?mode=overview&page_size=200&completed=true", { headers });
  expect((await overview.json()).total_count).toBe(1000);

  const started = performance.now();
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(fixture.url);
  const center = page.getByRole("region", { name: "Selected task" });
  await expect(center.getByRole("button", { name: "Verify access boundaries and release dependencies", exact: true })).toBeVisible();
  await expect(page.getByText("Loading dependencies…", { exact: true })).toHaveCount(0);
  const initialMs = performance.now() - started;
  expect(await page.locator("[data-dependency-node]").count()).toBe(21);
  await page.getByRole("button", { name: /Show more blockers/ }).click();
  await expect(page).toHaveURL(/before=2/);
  await expect(page.getByText("Loading dependencies…", { exact: true })).toHaveCount(0);
  await expect(page.getByRole("region", { name: "What blocks this task" }).locator("[data-dependency-node]")).toHaveCount(20);
  await page.getByRole("region", { name: "What blocks this task" }).getByRole("button", { name: /completed · expand original links/ }).click();
  await expect(page).toHaveURL(/completed=true/);
  await expect(page.getByRole("region", { name: "What blocks this task" }).getByText("Done", { exact: true }).first()).toBeVisible();

  // Follow one neighbor and Browser Back restores the expanded scope.
  const neighbor = page.getByRole("region", { name: "What waits for this task" }).locator("[data-dependency-node]").first();
  const neighborId = await neighbor.getAttribute("data-dependency-node");
  await page.locator("main").evaluate(el => { el.scrollTop = 400; });
  await expect.poll(() => page.locator("main").evaluate(el => el.scrollTop)).toBe(400);
  await page.waitForTimeout(50);
  await neighbor.getByRole("button", { name: "Follow chain →" }).evaluate(el => (el as HTMLButtonElement).click());
  await expect(page).toHaveURL(new RegExp(`focus=${neighborId}`));
  await expect(page.getByText("Loading dependencies…", { exact: true })).toHaveCount(0);
  await page.goBack();
  await expect(page).toHaveURL(new RegExp(`focus=${fixture.focus}`));
  await expect(center.getByRole("button", { name: "Verify access boundaries and release dependencies", exact: true })).toBeVisible();

  await expect.poll(() => page.locator("main").evaluate(el => el.scrollTop)).toBe(400);
  await page.reload();
  await expect(center.getByRole("button", { name: "Verify access boundaries and release dependencies", exact: true })).toBeVisible();
  await expect.poll(() => page.locator("main").evaluate(el => el.scrollTop)).toBe(400);

  // Open the real panel without replacing the focus URL.
  const scopeURL = page.url();
  await center.getByRole("button", { name: "Open task", exact: true }).click();
  await expect(page.getByRole("dialog", { name: "Task detail" }).getByRole("heading", { name: "Verify access boundaries and release dependencies" })).toBeVisible();
  expect(page.url()).toBe(scopeURL);
  await page.keyboard.press("Escape");

  await neighbor.getByRole("button", { name: "Follow chain →" }).focus();
  await page.keyboard.press("Enter");
  await expect(center.locator(`[data-dependency-node="${neighborId}"]`)).toBeVisible();
  await page.goBack();
  await expect(center.locator(`[data-dependency-node="${fixture.focus}"]`)).toBeVisible();

  const samples: number[] = [];
  for (let i = 0; i < 20; i++) {
    const id = i % 2 ? fixture.focus : neighborId;
    const response = page.waitForResponse(r => new URL(r.url()).pathname === graphPath && new URL(r.url()).searchParams.get("focus") === id && r.request().method() === "GET");
    const start = performance.now();
    const branch = page.getByRole("region", { name: i % 2 ? "What blocks this task" : "What waits for this task" });
    await branch.locator(`[data-dependency-node="${id}"]`).getByRole("button", { name: "Follow chain →" }).click();
    expect((await response).ok()).toBe(true);
    await expect(page.getByRole("region", { name: "Selected task" }).locator(`[data-dependency-node="${id}"]`)).toBeVisible();
    await expect(page.getByText("Loading dependencies…", { exact: true })).toHaveCount(0);
    samples.push(performance.now() - start);
  }
  samples.sort((a,b) => a-b);
  const report = { dataset: { nodes: 1000, edges: 3000 }, initial_navigation_ms: initialMs,
    focus_interaction_p50_ms: samples[9], focus_interaction_p95_ms: samples[18],
    environment: "localhost Vite dev + real Go/PostgreSQL; neighbor click through fresh API response and rendered focus; initial navigation includes app bootstrap", samples_ms: samples };
  console.log(JSON.stringify(report));
  if (process.env.DEPENDENCY_REPORT) writeFileSync(process.env.DEPENDENCY_REPORT, JSON.stringify(report, null, 2));
  for (const width of [1440, 393, 360, 320]) {
    await page.setViewportSize({ width, height: 1000 });
    await page.evaluate(() => sessionStorage.clear());
    await page.goto(fixture.url);
    await expect(center.locator(`[data-dependency-node="${fixture.focus}"]`)).toBeVisible();
    await expect(page.getByText("Loading dependencies…", { exact: true })).toHaveCount(0);
    expect(await page.evaluate(() => [document.documentElement, document.querySelector("main")!, document.querySelector("[data-dependencies-view]")!].every(el => el.scrollWidth <= el.clientWidth))).toBe(true);
    if (width < 1024) await expect(center.getByRole("button", { name: "Verify access boundaries and release dependencies", exact: true })).toBeInViewport();
    const cards = page.locator("[data-dependency-node] button.font-semibold");
    for (const el of await cards.all()) expect(await el.evaluate(n => n.scrollWidth <= n.clientWidth)).toBe(true);
    if (process.env.DEPENDENCY_SCREENSHOTS) await page.screenshot({ path: `${process.env.DEPENDENCY_SCREENSHOTS}/focus-${width}.png` });
    console.log(`width=${width} overflow=0 readable_titles=PASS focus_nodes=${await page.locator('[data-dependency-node]').count()}`);
  }
  // Positive control: the viewport check must reject an intentionally oversized element.
  await page.evaluate(() => { const el = document.createElement("div"); el.id = "overflow-control"; el.style.cssText = "width:1000px;height:10px"; document.querySelector("[data-dependencies-view]")!.append(el); });
  expect(await page.evaluate(() => [document.documentElement, document.querySelector("main")!, document.querySelector("[data-dependencies-view]")!].every(el => el.scrollWidth <= el.clientWidth))).toBe(false);
  await page.locator("#overflow-control").evaluate(el => el.remove());
  expect(await page.evaluate(() => [document.documentElement, document.querySelector("main")!, document.querySelector("[data-dependencies-view]")!].every(el => el.scrollWidth <= el.clientWidth))).toBe(true);
  console.log("overflow_control=RED→GREEN readonly_mutations=" + mutations.length + " browser_errors=" + failures.length);
  expect(mutations).toEqual([]); expect(failures).toEqual([]);
});
