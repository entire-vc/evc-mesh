// Real-browser walk of the table affordances on dev-fixtures/table-editor.html:
// hover strips, + row / + column, right-click menu, refusal text, read-only, mousemove listeners, input lag.
// Run: npx vite --port 5199, then node dev-fixtures/walk-table.mjs <out-dir>
import { chromium } from "@playwright/test";
const BASE = "http://localhost:5199/dev-fixtures/table-editor.html?lang=ru";
const OUT = process.argv[2] ?? ".";
const log = (...a) => console.log(...a);
const browser = await chromium.launch(process.env.PW_EXE ? { executablePath: process.env.PW_EXE } : {});

async function open(width, height) {
  const ctx = await browser.newContext({ viewport: { width, height }, deviceScaleFactor: 1 });
  const page = await ctx.newPage();
  await page.addInitScript(() => {
    const w = window;
    w.__moves = [];
    const orig = EventTarget.prototype.addEventListener;
    EventTarget.prototype.addEventListener = function (type, fn, opts) {
      if ((type === "mousemove" || type === "pointermove") && w.__track) w.__moves.push(`${type} on ${this.nodeName ?? this} @ ${(new Error().stack ?? "").split("\n").slice(2, 4).map((s) => s.trim().replace(/\?v=[^:]*/, "").replace(/http:\/\/localhost:5199/, "")).join(" < ")}`);
      return orig.call(this, type, fn, opts);
    };
    w.__longtasks = [];
    new PerformanceObserver((l) => l.getEntries().forEach((e) => w.__longtasks.push(Math.round(e.duration)))).observe({ type: "longtask", buffered: true });
  });
  page.on("pageerror", (e) => log("PAGEERROR", e.message));
  await page.goto(BASE);
  await page.waitForSelector('[data-testid="doc-edit"] table td');
  await page.waitForTimeout(400);
  await page.evaluate(() => { window.__track = true; });
  return { ctx, page };
}
const countTable = (page, sel) => page.evaluate((s) => { const t = document.querySelector(`${s} table`); return { rows: t.querySelectorAll("tr").length, cols: t.querySelector("tr").children.length }; }, sel);
const handles = (page, sel) => page.locator(`${sel} [data-table-handle]`).count();

// ---------------- 1440 ----------------
{
  const { ctx, page } = await open(1440, 900);
  const doc = '[data-testid="doc-edit"]';
  const shot = (n) => page.screenshot({ path: `${OUT}/${n}.png` });
  log("1440 idle: handles =", await handles(page, doc));
  await shot("1440-1-idle");
  const cell = page.locator(`${doc} td`).nth(5);
  await cell.hover();
  log("1440 hover: handles =", await handles(page, doc));
  await page.locator(`${doc} table`).evaluate((t) => t.scrollIntoView({ block: "end" }));
  await page.mouse.move(0, 0); await page.locator(`${doc} td`).last().hover();
  await page.locator(`${doc} [data-table-handle=row]`).hover(); await page.waitForTimeout(250);
  await shot("1440-2-hover-row-strip");
  const before = await countTable(page, doc);
  await page.locator(`${doc} [data-table-handle=row]`).click();
  await page.waitForTimeout(150);
  const afterRow = await countTable(page, doc);
  await page.locator(`${doc} td`).last().hover();
  await page.locator(`${doc} [data-table-handle=col]`).hover(); await page.waitForTimeout(250);
  await shot("1440-3-hover-col-strip");
  await page.locator(`${doc} [data-table-handle=col]`).click();
  await page.waitForTimeout(150);
  const afterCol = await countTable(page, doc);
  log("1440 + row/col:", JSON.stringify(before), "->", JSON.stringify(afterRow), "->", JSON.stringify(afterCol));
  await page.keyboard.type("new");
  await page.waitForTimeout(400);
  const md = await page.evaluate(() => window.__md.doc);
  const mdRows = md.split("\n").filter((l) => l.startsWith("|"));
  log("1440 markdown: table lines =", mdRows.length, "cols in header =", mdRows[0].split("|").length - 2, "typed into new cell:", mdRows[0].includes("new"));

  // right-click a body cell -> menu -> delete that row
  const target = page.locator(`${doc} td`, { hasText: "r5c3" });
  await target.click({ button: "right" });
  const menu = page.locator("[data-table-context-menu]");
  log("1440 right-click: menu visible =", await menu.isVisible(), "items =", await menu.locator("[role^=menuitem]").count());
  await shot("1440-4-context-menu");
  await menu.getByRole("menuitem", { name: "Удалить строку" }).click();
  await page.waitForTimeout(400);
  const md2 = await page.evaluate(() => window.__md.doc);
  log("1440 delete row via right-click: r5c3 gone =", !md2.includes("r5c3"), "rows now =", (await countTable(page, doc)).rows);

  // multi-row selection survives the right-click: drag r7c2 -> r8c2, right-click inside, delete -> two rows go
  const rowsBeforeMulti = (await countTable(page, doc)).rows;
  const a = await page.locator(`${doc} td`, { hasText: "r7c2" }).boundingBox();
  const b2 = await page.locator(`${doc} td`, { hasText: "r8c2" }).boundingBox();
  await page.mouse.move(a.x + 10, a.y + a.height / 2); await page.mouse.down();
  await page.mouse.move(b2.x + 10, b2.y + b2.height / 2, { steps: 5 }); await page.mouse.up();
  await page.mouse.click(b2.x + 10, b2.y + b2.height / 2, { button: "right" });
  await page.locator("[data-table-context-menu]").getByRole("menuitem", { name: "Удалить строку" }).click();
  await page.waitForTimeout(300);
  const md3 = await page.evaluate(() => window.__md.doc);
  log("1440 multi-row right-click delete: rows", rowsBeforeMulti, "->", (await countTable(page, doc)).rows, "r7/r8 gone =", !md3.includes("r7c2") && !md3.includes("r8c2"), "r6/r9 kept =", md3.includes("r6c2") && md3.includes("r9c2"));

  // read-only: nothing
  const view = '[data-testid="doc-view"]';
  await page.locator(`${view} td`).nth(3).hover();
  log("1440 read-only hover: handles =", await handles(page, view), "anchors =", await page.locator(`${view} [data-table-handles]`).count());
  await page.locator(`${view} td`).nth(3).click({ button: "right" });
  log("1440 read-only right-click: table menu =", await page.locator("[data-table-context-menu]").count());
  await page.keyboard.press("Escape");

  // task description: refusal text
  const task = '[data-testid="task-edit"]';
  await page.locator(`${task} td`).first().click({ button: "right" });
  await page.locator("[data-table-context-menu]").getByRole("menuitem", { name: "Удалить строку" }).click();
  await page.waitForTimeout(200);
  await page.locator(`${task} td`).first().click({ button: "right" });
  const reason = page.locator("[data-table-context-menu] [data-reason-for=delete-row]");
  log("1440 task: rows =", (await countTable(page, task)).rows, "refusal visible =", await reason.isVisible(), JSON.stringify(await reason.textContent()));
  await page.screenshot({ path: `${OUT}/1440-5-task-refusal.png` });
  await page.keyboard.press("Escape");
  await page.locator(`${task} td`).first().hover();
  log("1440 task hover: handles =", await handles(page, task));
  await page.locator(`${task} [data-table-handle=row]`).click();
  await page.waitForTimeout(300);
  log("1440 task + row: rows =", (await countTable(page, task)).rows, "md rows =", (await page.evaluate(() => window.__md.desc)).split("\n").filter((l) => l.startsWith("|")).length);

  // input lag + scroll with the 20x10 table hovered
  await page.locator(`${doc} td`, { hasText: "r10c5" }).click();
  await page.evaluate(() => { window.__longtasks = []; });
  const t0 = Date.now();
  await page.keyboard.type("abcdefghijklmnopqrstuvwxyz0123456789", { delay: 0 });
  const typeMs = Date.now() - t0;
  for (let i = 0; i < 10; i++) { await page.mouse.wheel(0, 300); await page.waitForTimeout(30); }
  for (let i = 0; i < 10; i++) { await page.mouse.wheel(0, -300); await page.waitForTimeout(30); }
  log("1440 perf: 36 keys in", typeMs, "ms; long tasks during type+scroll:", JSON.stringify(await page.evaluate(() => window.__longtasks)));
  log("mousemove/pointermove listeners added after load:", JSON.stringify(await page.evaluate(() => window.__moves)));
  await ctx.close();
}

// ---------------- 393 ----------------
{
  const { ctx, page } = await open(393, 852);
  const doc = '[data-testid="doc-edit"]';
  await page.screenshot({ path: `${OUT}/393-1-idle.png` });
  await page.locator(`${doc} td`).nth(2).hover();
  await page.locator(`${doc} table`).evaluate((t) => t.scrollIntoView({ block: "start" }));
  await page.locator(`${doc} td`).nth(2).hover();
  log("393 hover: handles =", await handles(page, doc));
  await page.screenshot({ path: `${OUT}/393-2-hover-strips.png` });
  const before = await countTable(page, doc);
  await page.locator(`${doc} [data-table-handle=col]`).click();
  await page.waitForTimeout(300);
  const newCell = await page.locator(`${doc} table tr`).first().locator("th, td").last().evaluate((c) => {
    const t = c.closest("table").getBoundingClientRect(); const r = c.getBoundingClientRect();
    return { visible: r.left >= t.left - 1 && r.right <= t.right + 1, scrollLeft: Math.round(c.closest("table").scrollLeft), focused: c.contains(getSelection().anchorNode) };
  });
  log("393 + col:", JSON.stringify(before), "->", JSON.stringify(await countTable(page, doc)), "new cell", JSON.stringify(newCell));
  await page.screenshot({ path: `${OUT}/393-2b-after-add-col.png` });
  await page.locator(`${doc} td`).last().scrollIntoViewIfNeeded();
  await page.locator(`${doc} td`).last().hover();
  await page.locator(`${doc} [data-table-handle=row]`).click();
  await page.waitForTimeout(300);
  log("393 + row:", JSON.stringify(await countTable(page, doc)));
  await page.screenshot({ path: `${OUT}/393-2c-after-add-row.png` });
  const task = '[data-testid="task-edit"]';
  await page.locator(`${task} td`).first().scrollIntoViewIfNeeded();
  await page.locator(`${task} td`).first().click({ button: "right" });
  const menu = page.locator("[data-table-context-menu]");
  const box = await menu.boundingBox();
  log("393 right-click: menu box =", JSON.stringify(box), "fits =", box.x >= 0 && box.x + box.width <= 393);
  await page.screenshot({ path: `${OUT}/393-3-context-menu.png` });
  await page.keyboard.press("Escape");
  await page.locator(`${task} td`).first().hover();
  await page.screenshot({ path: `${OUT}/393-4-task-hover.png` });
  await ctx.close();
}
// ---------------- 393, touch ----------------
{
  const ctx = await browser.newContext({ viewport: { width: 393, height: 852 }, hasTouch: true, isMobile: true, deviceScaleFactor: 1 });
  const page = await ctx.newPage();
  await page.goto(BASE);
  await page.waitForSelector('[data-testid="task-edit"] table td');
  await page.waitForTimeout(300);
  const task = '[data-testid="task-edit"]';
  await page.locator(`${task} td`).first().scrollIntoViewIfNeeded();
  await page.locator(`${task} td`).first().tap();
  await page.waitForTimeout(200);
  log("393 touch tap in table: handles =", await handles(page, task));
  const before = await countTable(page, task);
  if (await handles(page, task)) { await page.locator(`${task} [data-table-handle=row]`).tap(); await page.waitForTimeout(300); }
  log("393 touch tap +row:", JSON.stringify(before), "->", JSON.stringify(await countTable(page, task)));
  await page.screenshot({ path: `${OUT}/393-5-touch.png` });
  await ctx.close();
}
await browser.close();
