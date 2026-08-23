#!/usr/bin/env node
// Ad-hoc §1k screenshots for the Docs selection-colour fix: the tree's selected
// row and the move dialog's picked destination, in both themes.
//
// Not folded into screenshot.mjs: that helper shoots a URL, and both surfaces
// here exist only after interaction (a dialog has to be opened, a destination
// picked).
import { chromium } from "@playwright/test";
import { readFileSync, mkdirSync } from "node:fs";

const seed = JSON.parse(readFileSync(process.argv[2], "utf8"));
const outDir = process.argv[3];
mkdirSync(outDir, { recursive: true });

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 820 } });
const errors = [];
page.on("pageerror", (e) => errors.push(String(e)));
page.on("console", (m) => m.type() === "error" && errors.push(m.text()));

await page.goto(seed.login_url, { waitUntil: "domcontentloaded" });
await page.fill('input[type="email"]', seed.email);
await page.fill('input[type="password"]', seed.password);
await page.click('button[type="submit"]');
await page.waitForURL((u) => !u.pathname.endsWith("/login"), { timeout: 30000 });

async function setTheme(dark) {
  await page.evaluate((d) => {
    document.documentElement.classList.toggle("dark", d);
    localStorage.setItem("theme", d ? "dark" : "light");
  }, dark);
  await page.waitForTimeout(200);
}

async function shoot(name) {
  await page.screenshot({ path: `${outDir}/${name}.png` });
  console.log(`wrote ${name}.png`);
}

for (const dark of [false, true]) {
  const suffix = dark ? "dark" : "light";
  await page.goto(seed.doc_url, { waitUntil: "domcontentloaded" });
  await setTheme(dark);
  await page.waitForTimeout(1500);

  // 1. The tree with a document selected — the row Pavel called dark.
  const row = page.locator('[role="treeitem"][aria-selected="true"]');
  const rowCls = (await row.first().getAttribute("class")) ?? "";
  if (!/bg-secondary/.test(rowCls)) throw new Error(`tree row not secondary: ${rowCls}`);
  await shoot(`01-tree-selected-${suffix}`);

  // 2. The move dialog with a destination picked.
  await page.click('[aria-label="Actions for Onboarding"]');
  await page.click("text=Move to...");
  await page.waitForTimeout(400);
  await page.click('[role="option"]:has-text("Runbook")');
  // Move the pointer off the row before shooting: `hover:bg-muted` sits after
  // `bg-secondary` in the sheet, so a hovered pick shows the hover tint and the
  // frame would not show the fill under discussion.
  await page.mouse.move(1200, 700);
  await page.waitForTimeout(300);
  const picked = page.locator('[role="option"][aria-selected="true"]');
  const cls = (await picked.first().getAttribute("class")) ?? "";
  if (!/bg-secondary/.test(cls)) throw new Error(`picked destination not secondary: ${cls}`);
  if (/bg-accent/.test(cls)) throw new Error(`picked destination still accent: ${cls}`);
  await shoot(`02-move-dialog-${suffix}`);
  await page.keyboard.press("Escape");
  await page.waitForTimeout(300);
}

console.log(errors.length ? `console errors:\n${errors.join("\n")}` : "no console errors");
if (errors.length) process.exitCode = 1;
await browser.close();
