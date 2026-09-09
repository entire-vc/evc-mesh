#!/usr/bin/env node
// Reproduction attempt for Pavel's "иконка воркспейса съезжает" complaint
// (task follow-up, 2026-09-08). Bill's own single-project 1440 measurement
// found perfect centering (cx=23.5 for all 12 rail items) post-animation,
// but flagged: (a) his workspace had only 1 project, not 13, (b) he never
// captured a frame DURING the 200ms collapse transition, (c) one real
// asymmetry — MeshIcon's fallback SVG at 18.5x18.5 vs every other icon's
// 16x16 — that could read as "off-center" without any coordinate being
// wrong. This script repeats his exact measurement with 13 seeded projects,
// both themes, AND grabs a mid-transition frame.
//
// UPDATE (`#13ff4803`): this script's own conclusion held — there is no
// coordinate offset, at any capture point, in either theme. The size
// asymmetry it flagged in (c) turned out to be the whole of the defect and is
// fixed; the mark is now 16x16 like every neighbour. What this script cannot
// see is size, by design — it reports the <svg> box and nothing about the ink
// inside it. `measure-rail-ink.mjs` next to it answers that question, and
// `scripts/assert-rail-icon-size.mjs` refuses a regression.
import { chromium } from "@playwright/test";
import { readFileSync } from "node:fs";

const seedPath = process.argv[process.argv.indexOf("--seed") + 1];
const outDir = process.argv[process.argv.indexOf("--out-dir") + 1] || ".";
const seed = JSON.parse(readFileSync(seedPath, "utf8"));

async function measureAll(page) {
  return page.evaluate(() => {
    const railWidth = document.querySelector("aside")?.getBoundingClientRect().width;
    const items = [...document.querySelectorAll("aside [data-testid], aside a")]
      .filter((el) => el.tagName === "A" || el.tagName === "DIV")
      .map((el) => {
        const r = el.getBoundingClientRect();
        if (r.width === 0 || r.height === 0) return null;
        const svg = el.querySelector("svg");
        const svgR = svg ? svg.getBoundingClientRect() : null;
        return {
          testid: el.getAttribute("data-testid") || el.tagName,
          left: +r.left.toFixed(1),
          width: +r.width.toFixed(1),
          height: +r.height.toFixed(1),
          cx: +(r.left + r.width / 2).toFixed(1),
          radius: getComputedStyle(el).borderRadius,
          svg: svgR ? { w: +svgR.width.toFixed(1), h: +svgR.height.toFixed(1) } : null,
        };
      })
      .filter(Boolean);
    return { railWidth, items };
  });
}

async function main() {
  const browser = await chromium.launch({ executablePath: process.env.RAIL_CONTRAST_CHROMIUM });
  const results = {};
  for (const dark of [false, true]) {
    const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: dark ? "dark" : "light" });
    const page = await context.newPage();
    await page.goto(seed.login_url, { waitUntil: "domcontentloaded" });
    await page.fill("#email", seed.email);
    await page.fill("#password", seed.password);
    await Promise.all([
      page.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 15000 }),
      page.click('button[type="submit"]'),
    ]);
    await page.waitForTimeout(600);
    const key = dark ? "dark" : "light";

    // Baseline: expanded state, before any toggle.
    results[`${key}_expanded`] = await measureAll(page);

    const toggle = page.locator("header button:has(svg.lucide-menu)").first();
    await toggle.click();

    // Mid-transition frame (~100ms into the 200ms CSS transition).
    await page.waitForTimeout(100);
    results[`${key}_mid_transition`] = await measureAll(page);
    await page.screenshot({ path: `${outDir}/rail-mid-transition-${key}.png` });

    // Settled, post-animation.
    await page.waitForTimeout(400);
    results[`${key}_settled`] = await measureAll(page);

    await context.close();
  }
  await browser.close();
  console.log(JSON.stringify(results, null, 2));
}
main().catch((e) => { console.error(e.stack || String(e)); process.exit(1); });
