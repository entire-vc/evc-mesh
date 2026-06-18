import { chromium, type FullConfig } from "@playwright/test";
import * as fs from "fs";
import * as path from "path";

/**
 * Global setup: logs in via Casdoor once and saves storageState.json.
 * All authed-e2e tests reuse this session — no per-test login overhead.
 *
 * Flow:
 *   1. Navigate to Casdoor login page
 *   2. Fill credentials from env vars (CASDOOR_AGENT_USER / _PASSWORD)
 *   3. Wait for redirect back to the app
 *   4. Save storageState → e2e/.auth/user.json
 */
export default async function globalSetup(_config: FullConfig) {
  const casdoorBase =
    process.env.CASDOOR_BASE_URL || "https://auth.entire.host";
  const appBase = process.env.APP_BASE_URL || "https://mesh.entire.host";
  const user = process.env.CASDOOR_AGENT_USER;
  const pass = process.env.CASDOOR_AGENT_PASSWORD;

  if (!user || !pass) {
    // In REPORT mode, missing creds → skip rather than hard-fail
    console.warn(
      "[global-setup] CASDOOR_AGENT_USER / CASDOOR_AGENT_PASSWORD not set; " +
        "e2e/.auth/user.json will be empty — authed tests will run unauthenticated."
    );
    // Write empty storage state so tests don't crash on missing file
    const authDir = path.join(__dirname, ".auth");
    fs.mkdirSync(authDir, { recursive: true });
    fs.writeFileSync(path.join(authDir, "user.json"), JSON.stringify({ cookies: [], origins: [] }));
    return;
  }

  const browser = await chromium.launch();
  const context = await browser.newContext();
  const page = await context.newPage();

  try {
    // Navigate to Casdoor login
    await page.goto(`${casdoorBase}/login`, { waitUntil: "networkidle" });

    // Fill login form — Casdoor's default selectors
    await page.fill('input[name="username"], input[placeholder*="username" i]', user);
    await page.fill('input[name="password"], input[type="password"]', pass);
    await page.click('button[type="submit"], input[type="submit"]');

    // Wait for redirect back to the app (Casdoor redirects to the registered callback)
    await page.waitForURL(new RegExp(appBase.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")), {
      timeout: 15_000,
    });

    // Save the authenticated session
    const authDir = path.join(__dirname, ".auth");
    fs.mkdirSync(authDir, { recursive: true });
    await context.storageState({ path: path.join(authDir, "user.json") });
    console.log("[global-setup] Casdoor login successful — storageState saved.");
  } finally {
    await browser.close();
  }
}
