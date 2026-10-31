import { defineConfig } from "@playwright/test";
import { fileURLToPath } from "node:url";

const api = process.env.PERF_API_URL;
if (!api || !["localhost", "127.0.0.1"].includes(new URL(api).hostname)) {
  throw new Error("Appearance tests require a disposable loopback API");
}

export default defineConfig({
  testDir: ".",
  testMatch: "*.spec.ts",
  outputDir: "./results",
  workers: 1,
  retries: 0,
  timeout: 90_000,
  expect: { timeout: 15_000 },
  reporter: [["list"], ["junit", { outputFile: fileURLToPath(new URL("./junit.xml", import.meta.url)) }]],
  use: {
    baseURL: "http://localhost:4327",
    locale: "en-US",
    serviceWorkers: "block",
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  webServer: {
    command: "pnpm exec vite preview --port 4327 --strictPort",
    cwd: "..",
    url: "http://localhost:4327/login",
    reuseExistingServer: false,
    timeout: 60_000,
    env: { PERF_API_URL: api },
  },
});
