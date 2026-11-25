import { defineConfig } from "@playwright/test";

// The «Память Mesh» widget: backend-free harness on the vite dev server, the API answered at
// the network boundary (page.route in the spec) — the widget runs its real request path and cache.
export default defineConfig({
  testDir: ".",
  workers: 1,
  retries: 0,
  timeout: 60_000,
  outputDir: "./results",
  use: { baseURL: "http://127.0.0.1:3029", trace: "off" },
  // The API on the page's own origin, as in production: .env.development points at localhost:8005, and
  // cross-origin the browser hides Content-Disposition, so the CSV would lose its server-given name.
  webServer: {
    command: "pnpm dev --host 127.0.0.1 --port 3029 --strictPort",
    url: "http://127.0.0.1:3029",
    env: { VITE_API_URL: "http://127.0.0.1:3029" },
    reuseExistingServer: !process.env.CI,
  },
  reporter: "list",
});
