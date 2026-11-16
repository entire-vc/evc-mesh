// Real-browser walk of the edit mode on the fixture page: lazy chunk, mouse drag, resize limits, conflict, screenshots.
import { chromium } from "@playwright/test";
const BASE = "http://localhost:5199/dev-fixtures/dashboard-edit.html";
const OUT = process.argv[2];
const log = (...a) => console.log(...a);
const browser = await chromium.launch(process.env.PW_EXE ? { executablePath: process.env.PW_EXE } : {});
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
const page = await ctx.newPage();
const reqs = [];
page.on("request", (r) => { const u = new URL(r.url()); if (/\.(js|css|tsx?|mjs)(\?|$)/.test(u.pathname) || u.pathname.includes("node_modules")) reqs.push(u.pathname + u.search.slice(0, 0)); });
await page.goto(BASE + "?reset");
await page.waitForSelector('[data-testid="view-mode"]');
await page.waitForTimeout(500);
const before = reqs.filter((u) => /react-grid-layout|react-draggable|react-resizable|dashboard-editor|edit\/grid|catalog-dialog|settings-dialog|schema-form/.test(u));
log("VIEW MODE: editor-related requests =", before.length, before);
reqs.length = 0;
await page.getByRole("button", { name: "Edit dashboard" }).click();
await page.waitForSelector('[data-testid="dash-edit-grid"]');
await page.waitForTimeout(800);
const after = reqs.filter((u) => /react-grid-layout|react-draggable|react-resizable|dashboard-editor|edit\/grid|catalog-dialog|settings-dialog|schema-form/.test(u));
log("AFTER GEAR: editor-related requests =", after.length); after.forEach((u) => log("  ", u));
await page.screenshot({ path: `${OUT}/edit-1440.png` });

const rect = async (id) => { const b = await page.getByTestId(`dash-item-${id}`).boundingBox(); return b; };
const saved = async () => JSON.parse(await page.evaluate(() => localStorage.getItem("dash-edit-fixture")));
// mouse drag c (agents summary) by its title bar to the right
const handle = page.getByTestId("dash-drag-handle").nth(2);
const hb = await handle.boundingBox();
const c0 = await rect("c");
await page.mouse.move(hb.x + 40, hb.y + 10);
await page.mouse.down();
await page.mouse.move(hb.x + 140, hb.y + 12, { steps: 6 });
await page.mouse.move(hb.x + 260, hb.y + 12, { steps: 6 });
await page.mouse.up();
await page.waitForTimeout(500);
const s1 = await saved();
log("DRAG: c before x=", Math.round(c0.x), "after x=", Math.round((await rect("c")).x), "saved c=", JSON.stringify(s1.items.find((i) => i.id === "c")), "version", s1.version);

// resize b (mentions, min 3x2 max 12x10) by dragging the SE corner far beyond max and far below min
const resizeBy = async (id, dx, dy) => {
  const hdl = page.getByTestId(`dash-item-${id}`).locator(".react-resizable-handle-se");
  const bb = await hdl.boundingBox();
  await page.mouse.move(bb.x + 6, bb.y + 6);
  await page.mouse.down();
  await page.mouse.move(bb.x + 6 + dx / 2, bb.y + 6 + dy / 2, { steps: 5 });
  await page.mouse.move(bb.x + 6 + dx, bb.y + 6 + dy, { steps: 5 });
  await page.mouse.up();
  await page.waitForTimeout(500);
};
await resizeBy("a", 1400, 900); // my-tasks max 12x8
let s2 = await saved();
log("RESIZE huge: a =", JSON.stringify(s2.items.find((i) => i.id === "a")), "(limit max 12x8)");
await resizeBy("a", -1400, -900); // min 3x2
s2 = await saved();
log("RESIZE tiny: a =", JSON.stringify(s2.items.find((i) => i.id === "a")), "(limit min 3x2)");

// keyboard drag
await page.getByTestId("dash-drag-handle").nth(2).focus();
const k0 = (await saved()).items.find((i) => i.id === "c");
await page.keyboard.press("ArrowLeft");
await page.waitForTimeout(400);
const k1 = (await saved()).items.find((i) => i.id === "c");
log("KEYBOARD: c x", k0.x, "->", k1.x);

// catalog + add
await page.getByRole("button", { name: "Add widget" }).first().click();
await page.waitForTimeout(300);
await page.screenshot({ path: `${OUT}/catalog-1440.png` });
await page.getByText("Failed pipelines").click();
await page.waitForTimeout(500);
const s3 = await saved();
log("ADD: new =", JSON.stringify(s3.items.find((i) => i.type === "gitlab-pipelines")));

// settings dialog
await page.getByRole("button", { name: /Settings: My tasks/ }).click();
await page.waitForTimeout(300);
await page.screenshot({ path: `${OUT}/settings-1440.png` });
await page.getByRole("button", { name: "Cancel", exact: true }).click();

// remove + undo
await page.getByRole("button", { name: /Remove: Mentions/ }).click();
await page.waitForTimeout(400);
await page.screenshot({ path: `${OUT}/removed-toast-1440.png` });
log("REMOVE: widgets now", (await saved()).items.length);
await page.getByRole("button", { name: "Undo" }).click();
await page.waitForTimeout(500);
log("UNDO: widgets now", (await saved()).items.length);

// cancel changes
await page.getByRole("button", { name: /Cancel changes/ }).click();
await page.waitForTimeout(800);
const sc = await saved();
log("CANCEL CHANGES: widgets", sc.items.length, "c =", JSON.stringify(sc.items.find((i) => i.id === "c")));

// two tabs conflict
const tab2 = await ctx.newPage();
await tab2.goto(BASE + "?edit");
await tab2.waitForSelector('[data-testid="dash-edit-grid"]');
await page.getByRole("button", { name: /Remove: My tasks/ }).click(); // tab 1 saves -> version moves
await page.waitForTimeout(500);
const afterTab1 = await saved();
await tab2.getByRole("button", { name: /Remove: Mentions/ }).click(); // tab 2 still holds the old version
await tab2.waitForTimeout(600);
const msg = await tab2.getByRole("alert").innerText();
const sAfter = await saved();
log("CONFLICT: tab2 message =", JSON.stringify(msg), "| server untouched =", JSON.stringify(sAfter) === JSON.stringify(afterTab1));
await tab2.screenshot({ path: `${OUT}/conflict-1440.png` });
await tab2.getByRole("button", { name: "Refresh" }).click();
await tab2.waitForTimeout(600);
log("REFRESH: tab2 alert gone =", (await tab2.getByRole("alert").count()) === 0);

// phone
const phone = await browser.newContext({ viewport: { width: 393, height: 852 }, isMobile: true, hasTouch: true });
const p = await phone.newPage();
await p.goto(BASE + "?reset&edit");
await p.waitForSelector('[data-testid="dash-edit-phone"]');
await p.waitForTimeout(500);
await p.screenshot({ path: `${OUT}/edit-393.png`, fullPage: true });
const overflow = await p.evaluate(() => document.documentElement.scrollWidth > window.innerWidth);
log("PHONE: horizontal overflow =", overflow, "| drag handles =", await p.getByTestId("dash-drag-handle").count());
await p.getByRole("button", { name: /Move down: My tasks/ }).click();
await p.waitForTimeout(500);
log("PHONE reorder saved:", JSON.stringify(JSON.parse(await p.evaluate(() => localStorage.getItem("dash-edit-fixture"))).items.map((i) => i.id + "@" + i.y)));
await p.getByRole("button", { name: "Add widget" }).first().click();
await p.waitForTimeout(300);
await p.screenshot({ path: `${OUT}/catalog-393.png` });

// theme v1 gear
await page.goto(BASE + "?theme=v1");
await page.getByRole("button", { name: "Edit dashboard" }).click({ force: true });
log("THEME v1:", await page.getByRole("status").innerText());
await page.screenshot({ path: `${OUT}/theme-v1-1440.png` });
await browser.close();
