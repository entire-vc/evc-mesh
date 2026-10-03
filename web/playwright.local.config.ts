import { defineConfig } from "@playwright/test";

const baseURL = process.env.LOCAL_STACK_WEB_URL;
if (!baseURL || !["localhost", "127.0.0.1"].includes(new URL(baseURL).hostname)) {
  throw new Error("LOCAL_STACK_WEB_URL must name the disposable local-stack server");
}
export default defineConfig({
  testDir: "./e2e-local", workers: 1, retries: 0, timeout: 120_000,
  use: { baseURL, trace: "off", screenshot: "off" }, reporter: "list",
});
