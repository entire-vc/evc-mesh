import { defineConfig } from "@playwright/test";
import { fileURLToPath } from "node:url";
const cwd = fileURLToPath(new URL("..", import.meta.url));
export default defineConfig({
  testDir: ".", testMatch: "board-pagination.spec.ts", workers: 1, retries: 0,
  use: { baseURL: "http://127.0.0.1:3019", viewport: { width: 1440, height: 900 }, trace: "off",
    channel: process.env.BOARD_BROWSER_CHANNEL },
  webServer: [
    { cwd, command: "node board-e2e/fixture-server.mjs", url: "http://127.0.0.1:8019/__fixture", reuseExistingServer: !process.env.CI },
    { cwd, command: "VITE_API_URL=http://127.0.0.1:8019 pnpm dev --host 127.0.0.1 --port 3019 --strictPort", url: "http://127.0.0.1:3019", reuseExistingServer: !process.env.CI },
  ],
});
