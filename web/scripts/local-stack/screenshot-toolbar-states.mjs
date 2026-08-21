#!/usr/bin/env node
// One-off screenshot helper for the task-description-editor toolbar contrast
// fix (rich-text-editor.tsx ToolbarButton, task #6f73a105) — captures
// idle/hover/pressed at 1440 + 393, light + dark, against the task PANEL's
// own description editor (not the create-task dialog): task-panel.tsx mounts
// the identical RichTextEditor behind an Edit button, which is the surface
// the ticket names. Not part of the generic local-stack.sh flow
// (screenshot.mjs has no pre-action hooks for hover/mousedown), so this is a
// standalone script reusing the same login recipe as
// screenshot-toolbar-states.mjs did for markdown-editor.tsx before #631
// deleted it and unified task descriptions, comments and docs onto one
// editor.
import { chromium } from "@playwright/test";
import { mkdir } from "node:fs/promises";

const [, , loginUrl, email, password, boardUrl, outDir] = process.argv;
if (!loginUrl || !email || !password || !boardUrl || !outDir) {
  console.error(
    "usage: screenshot-toolbar-states.mjs <loginUrl> <email> <password> <boardUrl> <outDir>",
  );
  process.exit(1);
}

async function login(page) {
  await page.goto(loginUrl, { waitUntil: "domcontentloaded" });
  await page.fill("#email", email);
  await page.fill("#password", password);
  await Promise.all([
    page.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 15_000 }),
    page.click('button[type="submit"]'),
  ]);
}

async function openTaskPanelWithEditableDescription(page, title, width) {
  await page.goto(boardUrl, { waitUntil: "networkidle" });

  const newTaskBtn = page.getByRole("button", { name: /new task/i }).first();
  await newTaskBtn.click();

  await page.getByPlaceholder("Task title *").fill(title);
  await page.getByRole("button", { name: "Create Task" }).click();
  await page.getByRole("button", { name: "Create Task" }).waitFor({ state: "detached" });

  await page.getByText(title, { exact: true }).click();
  await page.waitForURL((u) => /\/t\//.test(u.pathname), { timeout: 10_000 });

  // Below the `lg` breakpoint (1024px) task-panel.tsx swaps to a 6-tab mobile
  // layout (Details/Description/Comments/...) and the Description section —
  // Edit button included — is not in the DOM until its tab is selected.
  // `.isVisible()` is a non-retrying immediate check, so branching on it here
  // races the mobile tab bar's first render; branch on the known viewport
  // width instead, then let `.click()`'s normal auto-waiting handle the rest.
  if (width < 1024) {
    await page.getByRole("button", { name: "Description", exact: true }).click();
  }

  await page.getByRole("button", { name: "Edit", exact: true }).click();

  // The comment composer further down the page mounts its own RichTextEditor
  // too (same component) — scope to the description's toolbar specifically,
  // which is the first one in DOM order.
  const bold = page.getByTitle("Bold (Ctrl+B)").first();
  await bold.waitFor({ state: "visible", timeout: 10_000 });
  return bold;
}

async function shootTheme(browser, width, dark) {
  const context = await browser.newContext({
    viewport: { width, height: 900 },
    colorScheme: dark ? "dark" : "light",
  });
  const page = await context.newPage();
  await login(page);

  const title = `Toolbar contrast check ${width}${dark ? "-dark" : ""} ${Date.now()}`;
  const bold = await openTaskPanelWithEditableDescription(page, title, width);

  const suffix = dark ? "-dark" : "";

  // Idle
  await page.mouse.move(0, 0);
  await page.waitForTimeout(120);
  await page.screenshot({ path: `${outDir}/toolbar-${width}-idle${suffix}.png` });

  // Hover
  await bold.hover();
  await page.waitForTimeout(120);
  await page.screenshot({ path: `${outDir}/toolbar-${width}-hover${suffix}.png` });

  // Pressed (mousedown, held — no mouseup so :active stays engaged for the shot)
  const box = await bold.boundingBox();
  if (!box) throw new Error("bold button has no bounding box");
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  await page.waitForTimeout(120);
  await page.screenshot({ path: `${outDir}/toolbar-${width}-pressed${suffix}.png` });
  await page.mouse.up();

  // The `[[` document-link menu (doc-link-menu.tsx) — fixed alongside the
  // toolbar in the same file/task, so captured here rather than in a
  // separate screenshot pass.
  // Same DOM-order scoping as the Bold button above.
  const editorBody = page.locator(".mesh-doc-editor-body").first();
  await editorBody.click();
  await page.keyboard.type("[[");
  const menuRow = page.getByRole("option").first();
  const menuAppeared = await menuRow
    .waitFor({ state: "visible", timeout: 4_000 })
    .then(() => true)
    .catch(() => false);
  if (menuAppeared) {
    await page.screenshot({ path: `${outDir}/doclink-menu-${width}${suffix}.png` });
  } else {
    console.warn(
      `doc-link menu did not appear for width=${width} dark=${dark} (no other documents to suggest in this seeded project) — skipping that shot.`,
    );
  }

  await context.close();
}

async function main() {
  await mkdir(outDir, { recursive: true });
  const browser = await chromium.launch();
  try {
    for (const width of [1440, 393]) {
      for (const dark of [false, true]) {
        await shootTheme(browser, width, dark);
        console.log(`done: width=${width} dark=${dark}`);
      }
    }
  } finally {
    await browser.close();
  }
}

main().catch((err) => {
  console.error(err.stack || String(err));
  process.exit(1);
});
