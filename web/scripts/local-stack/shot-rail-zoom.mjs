// High-DPI crops of the rail head (workspace mark + its first neighbours) and
// of the expanded sidebar header, in both themes. §1k asks for screens; a
// 48px-wide strip at 1x is not something a person can actually judge a 2.5px
// size difference on, so these are captured at deviceScaleFactor 4.
import { chromium } from "@playwright/test";
import { readFileSync } from "node:fs";
const seed = JSON.parse(readFileSync(process.argv[2], "utf8"));
const outDir = process.argv[3];
const label = process.argv[4] || "run";
const browser = await chromium.launch({ executablePath: process.env.RAIL_CONTRAST_CHROMIUM });
for (const dark of [false, true]) {
  const key = dark ? "dark" : "light";
  const ctx = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    colorScheme: dark ? "dark" : "light",
    deviceScaleFactor: 4,
  });
  const page = await ctx.newPage();
  await page.goto(seed.login_url, { waitUntil: "domcontentloaded" });
  await page.fill("#email", seed.email);
  await page.fill("#password", seed.password);
  await Promise.all([
    page.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 15000 }),
    page.click('button[type="submit"]'),
  ]);
  await page.waitForTimeout(700);
  // Expanded header first — the same mark lives there at a smaller box, and a
  // ratio change moves both states, so both have to be looked at.
  await page.screenshot({ path: `${outDir}/expanded-header-${key}-${label}.png`, clip: { x: 0, y: 0, width: 260, height: 190 } });
  await page.locator("header button:has(svg.lucide-menu)").first().click();
  await page.waitForTimeout(600);
  await page.screenshot({ path: `${outDir}/rail-head-${key}-${label}.png`, clip: { x: 0, y: 0, width: 48, height: 200 } });
  await ctx.close();
}
await browser.close();
console.log("ok");
