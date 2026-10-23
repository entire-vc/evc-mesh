import { defineConfig } from "@playwright/test";

// Project tab strip: backend-free harness on the vite dev server, no stack needed.
export default defineConfig({
  testDir: "./e2e-tabs", workers: 1, retries: 0, timeout: 60_000,
  outputDir: "./e2e-tabs/results",
  use: { baseURL: "http://127.0.0.1:3027", trace: "off" },
  webServer: { command: "pnpm dev --host 127.0.0.1 --port 3027 --strictPort", url: "http://127.0.0.1:3027", reuseExistingServer: !process.env.CI },
  // junit: CI publishes per-run outcomes (red run on the base strip, green run).
  reporter: [["list"], ["junit"]],
});
