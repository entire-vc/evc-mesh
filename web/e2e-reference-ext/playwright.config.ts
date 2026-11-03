import { defineConfig } from "@playwright/test";

/**
 * Browser receipt for the reference-ext `project.view` slot (an internal ticket, scenarios 1-4 of Mesh Doc
 * e2e-reference-ext-project-view). Real Chromium against the PUBLISHED Mesh app ($APP_BASE_URL) and the
 * published fixture origin. One login and one browser context per viewport (the refresh cookie is one-shot).
 * Traces, videos and HAR are OFF: the iframe URL carries a live token.
 */
export default defineConfig({
  testDir: ".",
  testMatch: /.*\.spec\.ts$/,
  timeout: 3 * 60_000,
  retries: 0,
  workers: 1,
  fullyParallel: false,
  reporter: [["list"], ["junit", { outputFile: "../reference-ext-e2e/junit.xml" }]],
  use: { baseURL: process.env.APP_BASE_URL, trace: "off", video: "off", screenshot: "off" },
  projects: [
    { name: "1440", use: { viewport: { width: 1440, height: 900 } } },
    { name: "393", use: { viewport: { width: 393, height: 852 }, deviceScaleFactor: 2 } },
  ],
});
