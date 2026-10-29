import { describe, expect, it } from "vitest";
import { runnerVersion, type RunnerRow } from "@/components/my-runner/runners-section";
import type { ControllerState, RuntimeController } from "@/lib/agent-runtime";
import type { OperatorRunner } from "@/lib/my-runner";

const entry = (version: string): RuntimeController =>
  ({ host: "mini", version, reporter_grant_id: "g", capabilities: [], enabled: true, heartbeat_max_age_seconds: 120 });
const report = (version?: string): ControllerState =>
  ({ controller_ref: "mesh-runner", report: { revision: 44, digest: "d", status: "applied", version }, received_at: "2026-10-28T15:40:00Z", current: true });
const lease = (version: string): OperatorRunner =>
  ({ runner_id: "mesh-runner", name: "mesh-runner", host: "mini", version, lease_expires_at: "2026-10-28T15:42:00Z", live: true, agents: [] });

describe("runnerVersion", () => {
  it("shows the reported version and marks a catalog entry that says otherwise", () => {
    const row: RunnerRow = { key: "mesh-runner", controller: { ref: "mesh-runner", entry: entry("0.1.2"), report: report("0.1.3") } };
    expect(runnerVersion(row)).toEqual({ version: "0.1.3", catalogVersion: "0.1.2" });
  });

  it("prefers the report over the lease", () => {
    const row: RunnerRow = { key: "mesh-runner", runner: lease("0.1.2"), controller: { ref: "mesh-runner", entry: entry("0.1.3"), report: report("0.1.3") } };
    expect(runnerVersion(row)).toEqual({ version: "0.1.3", catalogVersion: undefined });
  });

  it("falls back to the lease, then to the catalog, without a mismatch mark", () => {
    expect(runnerVersion({ key: "r", runner: lease("0.1.3") })).toEqual({ version: "0.1.3", catalogVersion: undefined });
    const row: RunnerRow = { key: "mesh-runner", controller: { ref: "mesh-runner", entry: entry("0.1.2"), report: report() } };
    expect(runnerVersion(row)).toEqual({ version: "0.1.2", catalogVersion: undefined });
  });
});
