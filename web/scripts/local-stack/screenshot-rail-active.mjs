#!/usr/bin/env node
// #119078b0: collapsed-rail ACTIVE-project screenshot — proves the
// hover/active teal-letter treatment (text-sidebar-primary) still reads as
// "selected", without the tile's fill ever becoming the workspace's own
// brand-teal box.
import { chromium } from "@playwright/test";
import { mkdir } from "node:fs/promises";
import { readFileSync } from "node:fs";

function arg(name, def) {
  const i = process.argv.indexOf(name);
  return i === -1 ? def : process.argv[i + 1];
}
const seedPath = arg("--seed");
const outDir = arg("--out-dir", ".");
const seed = JSON.parse(readFileSync(seedPath, "utf8"));

async function main() {
  await mkdir(outDir, { recursive: true });
  const launchOpts = process.env.RAIL_CONTRAST_CHROMIUM
    ? { executablePath: process.env.RAIL_CONTRAST_CHROMIUM }
    : {};
  const browser = await chromium.launch(launchOpts);
  for (const dark of [false, true]) {
    const context = await browser.newContext({
      viewport: { width: 1440, height: 900 },
      colorScheme: dark ? "dark" : "light",
    });
    const page = await context.newPage();
    await page.goto(seed.login_url, { waitUntil: "domcontentloaded" });
    await page.fill("#email", seed.email);
    await page.fill("#password", seed.password);
    await Promise.all([
      page.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 15_000 }),
      page.click('button[type="submit"]'),
    ]);
    await page.waitForTimeout(600);
    const toggle = page.locator("header button:has(svg.lucide-menu)").first();
    await toggle.click();
    await page.waitForTimeout(300);
    // Navigate into the first project by clicking its collapsed tile.
    await page.locator('[data-testid="project-icon"]').first().click();
    await page.waitForTimeout(400);
    const out = `${outDir}/rail-collapsed-active-${dark ? "dark" : "light"}-1440.png`;
    await page.screenshot({ path: out });
    console.log(out);
    await context.close();
  }
  await browser.close();
}
main().catch((e) => { console.error(e.stack || String(e)); process.exit(1); });
