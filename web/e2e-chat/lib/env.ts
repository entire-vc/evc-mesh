export type ActorId = "A" | "B" | "C" | "V";
export const ACTORS: ActorId[] = ["A", "B", "C", "V"];
export type Harness = "claude" | "codex";

/** A missing fixture is a failure of the setup, never a skip. */
export class SetupFail extends Error {
  constructor(message: string) {
    super(`FAIL SETUP: ${message}`);
    this.name = "SetupFail";
  }
}

export const OUT_DIR = process.env.AGENT_CHAT_E2E_OUT ?? "agent-chat-e2e";

const env = (name: string) => process.env[name] ?? "";

/** Names only: values never reach a log or an artifact. */
export function requiredEnvNames(): string[] {
  const names = ["APP_BASE_URL", "E2E_CHAT_AGENT_CLAUDE_ID", "E2E_CHAT_AGENT_CODEX_ID", "E2E_CHAT_AGENT_OFFLINE_ID"];
  for (const a of ACTORS) names.push(`E2E_CHAT_${a}_EMAIL`, `E2E_CHAT_${a}_PASSWORD`);
  return names;
}

export function missingEnv(): string[] {
  return requiredEnvNames().filter((n) => !env(n));
}

export const cfg = () => ({
  baseURL: env("APP_BASE_URL").replace(/\/$/, ""),
  wsSlug: env("E2E_CHAT_WS_SLUG") || "e2e-ci-sandbox",
  projectSlug: env("E2E_CHAT_PROJECT_SLUG") || "e2e-fixtures",
  expectSha: env("E2E_CHAT_EXPECT_SHA"),
  agents: {
    claude: env("E2E_CHAT_AGENT_CLAUDE_ID"),
    codex: env("E2E_CHAT_AGENT_CODEX_ID"),
    offline: env("E2E_CHAT_AGENT_OFFLINE_ID"),
  },
  email: (a: ActorId) => env(`E2E_CHAT_${a}_EMAIL`),
  password: (a: ActorId) => env(`E2E_CHAT_${a}_PASSWORD`),
});

/** A unique marker for one run; threads and titles carry it so cleanup is exact. */
export const RUN_ID = process.env.E2E_CHAT_RUN_ID || `chat-${process.env.CI_PIPELINE_ID ?? "local"}-${Date.now().toString(36)}`;
