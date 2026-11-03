export const OUT_DIR = process.env.EXT_FRAME_E2E_OUT ?? "ext-frame-e2e";

/** A missing fixture is a failure of the setup, never a skip. */
export class SetupFail extends Error {
  constructor(message: string) {
    super(`FAIL SETUP: ${message}`);
    this.name = "SetupFail";
  }
}

const env = (name: string) => process.env[name] ?? "";

/** Names only: values never reach a log or an artifact. */
export const requiredEnvNames = () => [
  "APP_BASE_URL",
  "E2E_EXT_EMAIL",
  "E2E_EXT_PASSWORD",
  "E2E_EXT_ID",
  "E2E_EXT_WORKSPACE_ID",
  "E2E_EXT_PROJECT_ID",
  "E2E_EXT_TASK_ID",
];

export const missingEnv = () => requiredEnvNames().filter((n) => !env(n));

export const cfg = () => ({
  baseURL: env("APP_BASE_URL").replace(/\/$/, ""),
  email: env("E2E_EXT_EMAIL"),
  password: env("E2E_EXT_PASSWORD"),
  extensionId: env("E2E_EXT_ID"),
  workspaceId: env("E2E_EXT_WORKSPACE_ID"),
  projectId: env("E2E_EXT_PROJECT_ID"),
  taskId: env("E2E_EXT_TASK_ID"),
  /** Optional: a task of a project the extension is NOT bound to (live 403 control). */
  foreignTaskId: env("E2E_EXT_FOREIGN_TASK_ID"),
  /** Optional: the tab title when it is not the extension's own name. */
  tabTitle: env("E2E_EXT_TAB_TITLE"),
  // In GitLab CI the pipeline commit alone decides; the override exists for local runs only and cannot detach a CI receipt from its commit.
  expectSha: process.env.GITLAB_CI ? env("CI_COMMIT_SHA") : env("CI_COMMIT_SHA") || env("E2E_EXT_EXPECT_SHA"),
});
