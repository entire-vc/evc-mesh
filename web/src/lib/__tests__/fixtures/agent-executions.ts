// Shared fixtures for the scoped agent-executions projection (spec §7 table).
// Shapes follow Mesh Doc runner-execution-observation-v1 §3; ids are fixed so
// stories can assert exact model / turn / revision rather than "something shown".

export const WS = "00000000-0000-4000-8000-000000000001";
export const PROJ_A = "00000000-0000-4000-8000-0000000000a1";
export const PROJ_B = "00000000-0000-4000-8000-0000000000b1";
export const AGENT_1 = "00000000-0000-4000-8000-0000000000c1";
export const AGENT_2 = "00000000-0000-4000-8000-0000000000c2";
export const AGENT_3 = "00000000-0000-4000-8000-0000000000c3";

export const LONG_ID = `attempt-${"0123456789abcdef".repeat(6)}`;

const NOW = "2026-10-30T12:00:00Z";

export const route = (over: Record<string, unknown> = {}) => ({
  binding_ref: "lane-a",
  controller_ref: "controller-a",
  profile_ref: "sol-high",
  harness: "codex",
  model: "gpt-6.1-sol",
  effort: null,
  order: 0,
  ...over,
});

export const attempt = (over: Record<string, unknown> = {}) => ({
  attempt_id: "att-1",
  turn_id: "turn-1",
  task_id: "00000000-0000-4000-8000-0000000000d1",
  binding_ref: "lane-a",
  controller_ref: "controller-a",
  instance_id: "inst-1",
  generation: 3,
  sequence: 12,
  state: "running",
  freshness: "current",
  policy_matches: true,
  resolved_route: { source: "runtime_policy", revision: 17, profile_ref: "sol-high", harness: "codex", model: "gpt-6.1-sol", effort: null },
  observed: { harness: "codex", model: "gpt-6.1-sol", effort: "high", source: "adapter", observed_at: NOW },
  heartbeat_at: NOW,
  received_at: NOW,
  ...over,
});

export const view = (over: Record<string, unknown> = {}) => ({
  workspace_id: WS,
  project_id: PROJ_A,
  agent_id: AGENT_1,
  managed: true,
  enabled: true,
  management_source: "runtime_policy",
  configured: { source: "runtime_policy", revision: 17, digest: "d17", routes: [route()] },
  attempts: [attempt()],
  profile: { source: "agent_profile", harness: "claude", model: "sonnet" },
  freshness: "current",
  ...over,
});

export const envelope = (items: unknown[], next_cursor: string | null = null) => ({
  schema_version: 1,
  generated_at: NOW,
  items,
  next_cursor,
});

export const FIXTURES = {
  managedCurrent: view(),
  managedNoUsage: view({ attempts: [], freshness: "unknown" }),
  managedModelUnknown: view({
    attempts: [attempt({ observed: { harness: "codex", model: null, effort: null, source: "adapter", observed_at: NOW }, freshness: "unknown" })],
    freshness: "unknown",
  }),
  twoAttemptsTwoModels: view({
    attempts: [
      attempt({ attempt_id: "att-1", turn_id: "turn-1" }),
      attempt({
        attempt_id: "att-2",
        turn_id: "turn-9",
        observed: { harness: "claude", model: "opus-5", effort: null, source: "adapter", observed_at: NOW },
      }),
    ],
  }),
  policyMismatch: view({
    configured: { source: "runtime_policy", revision: 18, digest: "d18", routes: [route({ model: "gpt-6.2" })] },
    attempts: [attempt({ policy_matches: false })],
  }),
  stale: view({ attempts: [attempt({ freshness: "stale" })], freshness: "stale" }),
  offline: view({ attempts: [attempt({ freshness: "offline" })], freshness: "offline" }),
  unmanaged: view({
    managed: false,
    management_source: null,
    configured: null,
    attempts: [],
    freshness: "unknown",
  }),
  disabled: view({ enabled: false, attempts: [], freshness: "unknown" }),
};
