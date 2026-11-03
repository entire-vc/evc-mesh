import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api", () => ({ api: vi.fn() }));
import { api } from "@/lib/api";
import {
  attemptRows,
  fetchAgentExecutions,
  filterAttemptRows,
  isActiveAttempt,
  normalizeExecutionView,
  observedModel,
  sortAttemptRows,
  summarizeByAgent,
} from "@/lib/agent-executions";
import { AGENT_1, AGENT_2, attempt, envelope, FIXTURES, PROJ_A, PROJ_B, view, WS } from "./fixtures/agent-executions";

const n = (raw: unknown) => normalizeExecutionView(raw)!;

describe("fetchAgentExecutions", () => {
  beforeEach(() => vi.mocked(api).mockReset());

  it("walks next_cursor to the end so no attempt is lost", async () => {
    vi.mocked(api)
      .mockResolvedValueOnce(envelope([view({ project_id: PROJ_A })], "c2"))
      .mockResolvedValueOnce(envelope([view({ project_id: PROJ_B })], null));
    const r = await fetchAgentExecutions(WS, { agentId: AGENT_1 });
    expect(r).toMatchObject({ kind: "ok", partial: false });
    if (r.kind !== "ok") throw new Error("unreachable");
    expect(r.items.map((i) => i.project_id)).toEqual([PROJ_A, PROJ_B]);
    const second = vi.mocked(api).mock.calls[1]![0] as string;
    expect(second).toContain("cursor=c2");
    expect(second).toContain(`agent_id=${AGENT_1}`);
  });

  it("a first-page failure is an error, never unmanaged", async () => {
    vi.mocked(api).mockRejectedValueOnce(Object.assign(new Error("x"), { status: 500 }));
    expect(await fetchAgentExecutions(WS)).toEqual({ kind: "error" });
  });

  it("403 is forbidden, a later-page failure keeps rows and is partial", async () => {
    vi.mocked(api).mockRejectedValueOnce(Object.assign(new Error("x"), { status: 403 }));
    expect(await fetchAgentExecutions(WS)).toEqual({ kind: "forbidden" });

    vi.mocked(api).mockResolvedValueOnce(envelope([view()], "c2")).mockRejectedValueOnce(new Error("boom"));
    const r = await fetchAgentExecutions(WS);
    expect(r).toMatchObject({ kind: "ok", partial: true });
  });

  it("rejects a foreign schema version and counts malformed rows", async () => {
    vi.mocked(api).mockResolvedValueOnce({ schema_version: 2, items: [], next_cursor: null });
    expect(await fetchAgentExecutions(WS)).toEqual({ kind: "error" });

    vi.mocked(api).mockResolvedValueOnce(envelope([view(), { nope: true }], null));
    const r = await fetchAgentExecutions(WS);
    expect(r).toMatchObject({ kind: "ok", partial: true, rejected: 1 });
  });
});

describe("observed vs configured", () => {
  it("never fills an unknown observed model from configured or the profile", () => {
    const v = n(FIXTURES.managedModelUnknown);
    expect(observedModel(v.attempts[0]!)).toBeNull();
    expect(v.configured?.routes[0]?.model).toBe("gpt-6.1-sol");
    expect(v.profile?.model).toBe("sonnet");
  });

  it("ended and offline attempts are history, stale stays active", () => {
    expect(isActiveAttempt(n(FIXTURES.managedCurrent).attempts[0]!)).toBe(true);
    expect(isActiveAttempt(n(FIXTURES.stale).attempts[0]!)).toBe(true);
    expect(isActiveAttempt(n(FIXTURES.offline).attempts[0]!)).toBe(false);
    expect(isActiveAttempt(n(view({ attempts: [attempt({ state: "completed" })] })).attempts[0]!)).toBe(false);
  });
});

describe("summarizeByAgent", () => {
  it("keeps one entry per project and does not pick a global model", () => {
    const items = [
      n(view({ project_id: PROJ_A, attempts: [attempt({ attempt_id: "a" })] })),
      n(
        view({
          project_id: PROJ_B,
          attempts: [attempt({ attempt_id: "b", observed: { harness: "claude", model: "opus-5", effort: null, source: "adapter", observed_at: "2026-10-30T12:00:00Z" } })],
        }),
      ),
    ];
    const s = summarizeByAgent(items).get(AGENT_1)!;
    expect(s.active.map((x) => observedModel(x.attempt))).toEqual(["gpt-6.1-sol", "opus-5"]);
    expect(s.views).toHaveLength(2);
  });

  it("unmanaged shows the profile; managed does not", () => {
    const u = summarizeByAgent([n(FIXTURES.unmanaged)]).get(AGENT_1)!;
    expect(u.mode).toBe("unmanaged");
    expect(u.profile?.source).toBe("agent_profile");
    const m = summarizeByAgent([n(FIXTURES.managedCurrent)]).get(AGENT_1)!;
    expect(m.profile).toBeNull();
    expect(m.configuredRevisions).toEqual([17]);
    expect(m.configuredSources).toEqual(["runtime_policy"]);
  });

  it("separates agents and flags a mixed managed/unmanaged agent", () => {
    const s = summarizeByAgent([n(FIXTURES.managedCurrent), n(view({ agent_id: AGENT_2 })), n(view({ project_id: PROJ_B, managed: false, configured: null, attempts: [] }))]);
    expect(s.size).toBe(2);
    expect(s.get(AGENT_1)!.mixed).toBe(true);
    expect(s.get(AGENT_1)!.mode).toBe("managed");
  });
});

describe("detail list", () => {
  const rows = attemptRows([
    n(view({ project_id: PROJ_A, attempts: [attempt({ attempt_id: "run-a", heartbeat_at: "2026-10-30T12:00:00Z" }), attempt({ attempt_id: "old-a", state: "completed", heartbeat_at: "2026-10-30T13:00:00Z" })] })),
    n(view({ project_id: PROJ_B, attempts: [attempt({ attempt_id: "run-b", heartbeat_at: "2026-10-30T12:05:00Z" })] })),
  ]);

  it("filters by project, scope and search; active rows sort before history", () => {
    expect(filterAttemptRows(rows, { projectId: PROJ_B, scope: "all", query: "" }).map((r) => r.attempt.attempt_id)).toEqual(["run-b"]);
    expect(filterAttemptRows(rows, { projectId: null, scope: "history", query: "" }).map((r) => r.attempt.attempt_id)).toEqual(["old-a"]);
    expect(filterAttemptRows(rows, { projectId: null, scope: "all", query: "RUN-A" })).toHaveLength(1);
    expect(sortAttemptRows(rows).map((r) => r.attempt.attempt_id)).toEqual(["run-b", "run-a", "old-a"]);
  });
});
