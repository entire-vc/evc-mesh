import { chromium, type FullConfig } from "@playwright/test";
import * as fs from "fs";
import * as path from "path";
import { fileURLToPath } from "url";

// ESM equivalent of __dirname (package.json has "type":"module")
const __dirname = path.dirname(fileURLToPath(import.meta.url));

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
    // "Authed E2E" is a required status check on main (branch protection,
    // enforce_admins: true) — it must be able to go red. Skipping here made
    // every run report "2 skipped" as a pass, so the required check was
    // green regardless of whether the app actually worked (task #40ea4053).
    throw new Error(
      "[global-setup] CASDOOR_AGENT_USER / CASDOOR_AGENT_PASSWORD not set — " +
        "cannot run authed E2E. This is a required check; it cannot silently " +
        "skip. Add the repo secrets (see .github/workflows/ci.yml) or drop " +
        "'Authed E2E' from required_status_checks if authed E2E is not wanted."
    );
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
