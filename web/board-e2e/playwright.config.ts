import { defineConfig } from "@playwright/test";
import { fileURLToPath } from "node:url";
const host = "127.0.0.1";
const cwd = fileURLToPath(new URL("..", import.meta.url));
export default defineConfig({
  testDir: ".", testMatch: process.env.DOCS_SHOTS ? /docs-shots\.spec\.ts$/ : /(board-(pagination|dnd|shots|studio)|dashboard|themes)\.spec\.ts$/, workers: 1, retries: 0,
  use: { baseURL: `http://${host}:3019`, viewport: { width: 1440, height: 900 }, trace: "off",
    channel: process.env.BOARD_BROWSER_CHANNEL },
  webServer: [
    { cwd, command: "node board-e2e/fixture-server.mjs", url: `http://${host}:8019/__fixture`, reuseExistingServer: !process.env.CI },
    { cwd, command: `VITE_API_URL=http://${host}:8019 VITE_DEV_API_PORT=8019 pnpm dev --host ${host} --port 3019 --strictPort`, url: `http://${host}:3019`, reuseExistingServer: !process.env.CI },
  ],
});
