#!/usr/bin/env node
// One-off screenshot helper for the task-description-editor toolbar contrast
// fix (markdown-editor.tsx ToolbarButton) — captures idle/hover/pressed at
// 1440 + 393, light + dark. Not part of the generic local-stack.sh flow
// (screenshot.mjs has no pre-action hooks for hover/mousedown), so this is a
// standalone script reusing the same login recipe.
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

async function shootTheme(browser, width, dark) {
  const context = await browser.newContext({
    viewport: { width, height: 900 },
    colorScheme: dark ? "dark" : "light",
  });
  const page = await context.newPage();
  await login(page);

  await page.goto(boardUrl, { waitUntil: "networkidle" });

  const newTaskBtn = page.getByRole("button", { name: /new task/i }).first();
  await newTaskBtn.click();

  const bold = page.getByTitle("Bold (Ctrl+B)");
  await bold.waitFor({ state: "visible", timeout: 10_000 });

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
