import { defineConfig } from "@playwright/test";

// Dashboard widget settings dialog: backend-free harness on the vite dev server, no stack needed.
export default defineConfig({
  testDir: "./e2e-widget-settings", workers: 1, retries: 0, timeout: 60_000,
  outputDir: "./e2e-widget-settings/results",
  use: { baseURL: "http://127.0.0.1:3028", trace: "off" },
  webServer: { command: "pnpm dev --host 127.0.0.1 --port 3028 --strictPort", url: "http://127.0.0.1:3028", reuseExistingServer: !process.env.CI },
  reporter: "list",
});
