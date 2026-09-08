#!/usr/bin/env node
// #119078b0: collapsed-rail screenshots with 13 projects — the density that
// actually triggered Pavel's rejection of #bb8f1092's fix (a single project
// tile doesn't show the "every tile looks like the workspace logo" effect).
// Reuses the authed local-stack the same way screenshot-rail-expanded.mjs
// does; the caller is expected to have already seeded N projects via a
// register+create-projects script (this one does NOT seed on its own,
// unlike screenshot-rail-expanded.mjs, so it can be pointed at a workspace
// seeded any way — see seed-rail-projects.sh next to this file's usage in
// the task comment for the exact 13-project seed used here).
//
// Usage: node screenshot-rail-collapsed.mjs --seed <path/to/seed.json> --out-dir <dir>
import { chromium } from "@playwright/test";
import { mkdir } from "node:fs/promises";
import { readFileSync } from "node:fs";

function arg(name, def) {
  const i = process.argv.indexOf(name);
  return i === -1 ? def : process.argv[i + 1];
}

const seedPath = arg("--seed");
const outDir = arg("--out-dir", ".");
if (!seedPath) throw new Error("--seed is required");
const seed = JSON.parse(readFileSync(seedPath, "utf8"));

async function main() {
  await mkdir(outDir, { recursive: true });
  const launchOpts = process.env.RAIL_CONTRAST_CHROMIUM
    ? { executablePath: process.env.RAIL_CONTRAST_CHROMIUM }
    : {};
  const browser = await chromium.launch(launchOpts);

  const results = [];
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

    // Collapse the rail — hamburger toggle in the header, per Bill/Garfield's
    // prior measurement: (274, 28) at 1440x900, NOT the neighboring
    // "+ New Project" button at ~(209, 28).
    const toggle = page.locator("header button:has(svg.lucide-menu)").first();
    if ((await toggle.count()) === 0) {
      throw new Error("sidebar toggle button not found — markup changed, update the selector");
    }
    await toggle.click();
    await page.waitForTimeout(300);

    const tiles = await page.locator('[data-testid="project-icon"]').count();
    if (tiles < 10) {
      throw new Error(
        `expected ~13 seeded project tiles in the collapsed rail, found ${tiles} — seed step didn't run or selector is stale`,
      );
    }

    // computed styles for the workspace-logo tile vs. the first project tile
    // — the actual assertion this card cares about, not just a picture.
    const computed = await page.evaluate(() => {
      const px = (el) => {
        if (!el) return null;
        const cs = getComputedStyle(el);
        return { bg: cs.backgroundColor, color: cs.color, radius: cs.borderRadius, w: cs.width, h: cs.height };
      };
      const wsLogo = document.querySelector('[data-testid="workspace-logo"]');
      const firstProject = document.querySelector('[data-testid="project-icon"]');
      return { workspaceLogo: px(wsLogo), firstProjectTile: px(firstProject) };
    });

    const p1440 = `${outDir}/rail-collapsed-${dark ? "dark" : "light"}-1440.png`;
    await page.screenshot({ path: p1440 });
    results.push({ file: p1440, tiles, computed });

    await page.setViewportSize({ width: 393, height: 852 });
    await page.waitForTimeout(250);
    const p393 = `${outDir}/rail-collapsed-${dark ? "dark" : "light"}-393.png`;
    await page.screenshot({ path: p393 });
    results.push({ file: p393, tiles, computed });

    await context.close();
  }

  await browser.close();
  console.log(JSON.stringify(results, null, 2));
}

main().catch((err) => {
  console.error(err.stack || String(err));
  process.exit(1);
});
