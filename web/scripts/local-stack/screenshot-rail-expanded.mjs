#!/usr/bin/env node
// Ad-hoc: expanded-sidebar screenshots for #bb8f1092, AC#5 ("оба состояния
// панели"). Reuses the authed local-stack (scripts/local-stack.sh up) —
// registers a handful of extra projects via the same REST API the seed
// script uses, then screenshots the workspace root with the sidebar in its
// EXPANDED state at both 1440 (default-expanded) and 393 (mobile: forced
// open via the header toggle, which renders as a full-width overlay — that
// overlay IS the expanded state on mobile, not a separate mode).
//
// Usage: node screenshot-rail-expanded.mjs --seed <path/to/seed.json> --out-dir <dir>
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

const API_URL = new URL(seed.login_url).origin.replace(/:\d+$/, "") + ":" + (process.env.LOCAL_STACK_API_PORT || "8096");

async function main() {
  await mkdir(outDir, { recursive: true });
  const launchOpts = process.env.RAIL_CONTRAST_CHROMIUM
    ? { executablePath: process.env.RAIL_CONTRAST_CHROMIUM }
    : {};
  const browser = await chromium.launch(launchOpts);

  // --- seed a few more projects so the expanded list shows "several", not one ---
  const loginRes = await fetch(new URL("/api/v1/auth/login", API_URL), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ email: seed.email, password: seed.password }),
  });
  if (!loginRes.ok) throw new Error(`login failed: ${loginRes.status} ${await loginRes.text()}`);
  const { tokens } = await loginRes.json();
  const token = tokens.access_token;

  const wsRes = await fetch(new URL("/api/v1/workspaces", API_URL), {
    headers: { Authorization: `Bearer ${token}` },
  });
  const workspaces = await wsRes.json();
  const wsId = workspaces[0].id;

  const extraProjects = [
    { name: "Rocket Launch", slug: "rail-shot-rocket", icon: "🚀" },
    { name: "Compliance", slug: "rail-shot-compliance", icon: "C" },
    { name: "Support", slug: "rail-shot-support", icon: "S" },
    { name: "Logistics", slug: "rail-shot-logistics", icon: "L" },
  ];
  for (const p of extraProjects) {
    const res = await fetch(new URL(`/api/v1/workspaces/${wsId}/projects`, API_URL), {
      method: "POST",
      headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
      body: JSON.stringify({ name: p.name, slug: p.slug, icon: p.icon }),
    });
    if (!res.ok) {
      const body = await res.text();
      console.error(`  (skip) create project ${p.slug}: ${res.status} ${body}`);
    }
  }

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

    // --- 1440: expanded is the default state, nothing to click ---
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.waitForTimeout(200);
    const p1440 = `${outDir}/rail-expanded-${dark ? "dark" : "light"}-1440.png`;
    await page.screenshot({ path: p1440 });
    results.push(p1440);

    // --- 393: mobile default is collapsed-off-canvas; open via the header
    // toggle button to reach the expanded overlay state ---
    await page.setViewportSize({ width: 393, height: 852 });
    await page.waitForTimeout(250);
    const toggle = page.locator("header button:has(svg.lucide-menu)").first();
    if ((await toggle.count()) === 0) {
      throw new Error("sidebar toggle button (header > button:has(svg.lucide-menu)) not found — markup changed, update the selector");
    }
    await toggle.click();
    await page.waitForTimeout(300);
    const p393 = `${outDir}/rail-expanded-${dark ? "dark" : "light"}-393.png`;
    await page.screenshot({ path: p393 });
    results.push(p393);

    await context.close();
  }

  await browser.close();
  console.log(JSON.stringify(results, null, 2));
}

main().catch((err) => {
  console.error(err.stack || String(err));
  process.exit(1);
});
