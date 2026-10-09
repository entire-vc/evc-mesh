import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import {
  controllerSync,
  poolObservations,
  normalizeRuntime,
  preferredVsActual,
  rankRoutes,
  type ControllerState,
  type RuntimeBinding,
  type RuntimeCatalog,
} from "@/lib/agent-runtime";

// The published contract example is the fixture: if R1 changes it, this breaks.
const example = JSON.parse(
  // The contract fixture names its first account "preferred"; the UI tests rename it so
// no account reads "preferred preferred" (the "preferred" label belongs to the policy).
  readFileSync(resolve(__dirname, "../../../../docs/api/agent-runtime.example.json"), "utf8").split('"preferred"').join('"main"'),
) as RuntimeCatalog;

function must<T>(v: T | undefined): T {
  if (v === undefined) throw new Error("fixture missing");
  return v;
}
const binding = (): RuntimeBinding => structuredClone(must(example.bindings["worker-b"]));

const managed = (over: Record<string, unknown> = {}) => ({
  mode: "managed", revision: 3, digest: "abc", enabled: true, drain_requested: false, config: example, controllers: [], ...over,
});

describe("normalizeRuntime", () => {
  it("accepts the real managed envelope with the published example catalog", () => {
    const s = normalizeRuntime(managed());
    expect(s).not.toBe("direct");
    const st = s as Exclude<typeof s, "direct" | null>;
    expect(st.revision).toBe(3);
    expect(Object.keys(st.catalog.bindings)).toEqual(["worker-b"]);
  });
  it("direct mode is its own result, not an error", () => {
    expect(normalizeRuntime({ mode: "direct", controllers: [] })).toBe("direct");
  });
  it("rejects wrong schema version, unknown mode and non-objects (negative control)", () => {
    expect(normalizeRuntime(managed({ config: { ...example, schema_version: 1 } }))).toBeNull();
    expect(normalizeRuntime({ mode: "weird", config: example })).toBeNull();
    expect(normalizeRuntime({})).toBeNull();
    expect(normalizeRuntime(null)).toBeNull();
    expect(normalizeRuntime("x")).toBeNull();
  });
});

describe("rankRoutes", () => {
  it("orders primary first, then the quota-edge reserve", () => {
    const r = rankRoutes(example, binding());
    expect(r.map((x) => [x.profileRef, x.role, x.rank])).toEqual([
      ["main", "primary", 1],
      ["reserve", "quota_reserve", 2],
    ]);
    expect(r.every((x) => x.unavailable === null)).toBe(true);
  });
  it("never lists a profile outside permitted_profiles", () => {
    const b = binding();
    b.permitted_profiles = ["main"];
    expect(rankRoutes(example, b).map((x) => x.profileRef)).toEqual(["main"]);
  });
  it("marks an API profile unavailable unless the provider is opted in", () => {
    const cat = structuredClone(example);
    must(cat.profiles.reserve).execution_mode = "api";
    must(cat.accounts.reserve).quota_pools_by_mode = { api: ["reserve-window"] };
    const off = rankRoutes(cat, binding());
    expect(must(off[1]).role).toBe("api_reserve");
    expect(must(off[1]).unavailable).toMatch(/API not opted in/);
    const b = binding();
    b.policy.api_reserve_providers = ["provider-a"];
    expect(must(rankRoutes(cat, b)[1]).unavailable).toBeNull();
  });
  it("an explicit primary API profile is usable without the reserve opt-in", () => {
    const cat = structuredClone(example);
    must(cat.profiles.main).execution_mode = "api";
    expect(must(rankRoutes(cat, binding())[0]).unavailable).toBeNull();
  });
  it("disabled binding, disabled or draining runtime make every route unavailable", () => {
    const b = binding();
    b.enabled = false;
    expect(rankRoutes(example, b).every((r) => r.unavailable === "binding disabled")).toBe(true);
    expect(must(rankRoutes(example, binding(), { enabled: false, drainRequested: false })[0]).unavailable).toBe("runtime disabled");
    expect(must(rankRoutes(example, binding(), { enabled: true, drainRequested: true })[0]).unavailable).toBe("runtime draining");
  });
  const NOW = Date.parse("2026-10-09T00:10:00Z");
  const obs = (state: string, over: Record<string, unknown> = {}) => ({
    "shared-window": { state, observed_at: "2026-10-09T00:09:50Z", verified: true, ...over },
  });
  it("a fresh verified exhausted pool makes the route unavailable by quota, so the projection moves to the reserve", () => {
    const r = rankRoutes(example, binding(), undefined, obs("exhausted"), NOW);
    expect(must(r[0]).unavailable).toBe("pool shared-window exhausted");
    expect(must(r[0]).unavailableKind).toBe("quota");
    expect(must(preferredVsActual(example, binding(), r)[0]).actualAccount).toBe("reserve");
    expect(must(rankRoutes(example, binding(), undefined, obs("available"), NOW)[0]).unavailable).toBeNull();
  });
  it("unverified, stale or future pool evidence never moves the projection", () => {
    for (const over of [{ verified: false }, { observed_at: "2026-10-08T00:00:00Z" }, { observed_at: "2026-10-09T01:00:00Z" }]) {
      expect(must(rankRoutes(example, binding(), undefined, obs("exhausted", over), NOW)[0]).unavailable).toBeNull();
    }
  });
  it("auth/network faults and config faults do not open the reserve", () => {
    const r = rankRoutes(example, binding(), undefined, obs("auth_error"), NOW);
    expect(must(r[0]).unavailableKind).toBe("other");
    const p = must(preferredVsActual(example, binding(), r)[0]);
    expect(p.actualAccount).toBeNull();
    expect(p.reason).toMatch(/only on confirmed quota exhaustion/);
    const cat = structuredClone(example);
    must(cat.profiles.main).enabled = false;
    const b = binding();
    expect(must(preferredVsActual(cat, b, rankRoutes(cat, b))[0]).actualAccount).toBeNull();
  });
  it("reports disabled controller and profile with a reason", () => {
    const cat = structuredClone(example);
    must(cat.controllers["runner-a"]).enabled = false;
    expect(must(rankRoutes(cat, binding())[0]).unavailable).toBe("controller disabled");
    const cat2 = structuredClone(example);
    must(cat2.profiles.main).enabled = false;
    expect(must(rankRoutes(cat2, binding())[0]).unavailable).toBe("profile disabled");
  });
});

describe("preferredVsActual", () => {
  it("actual = preferred while the preferred route is usable, even with a reserve present", () => {
    const b = binding();
    const p = preferredVsActual(example, b, rankRoutes(example, b));
    expect(p).toEqual([
      { provider: "provider-a", preferredAccount: "main", actualAccount: "main", reason: "preferred is usable" },
    ]);
  });
  it("shows none when nothing is usable", () => {
    const cat = structuredClone(example);
    must(cat.profiles.main).enabled = false;
    must(cat.profiles.reserve).enabled = false;
    const b = binding();
    expect(must(preferredVsActual(cat, b, rankRoutes(cat, b))[0]).actualAccount).toBeNull();
  });
});

describe("controllerSync", () => {
  const rep = (over: Record<string, unknown> = {}, current = true): ControllerState => ({
    controller_ref: "runner-a",
    report: { revision: 4, digest: "d", status: "applied", ...over },
    received_at: "2026-10-09T00:00:00Z",
    current,
  });
  it("applied only when revision, digest and the server `current` all agree", () => {
    expect(controllerSync(rep(), 4, "d").label).toBe("applied");
  });
  it("stale with a reason on digest mismatch, missing desired digest, revision lag, or server not-current", () => {
    expect(controllerSync(rep({ digest: "aaa" }), 4, "bbb")).toEqual({ label: "stale", detail: "applied digest differs from desired" });
    expect(controllerSync(rep(), 4, "").label).toBe("stale");
    expect(controllerSync(rep({ revision: 3 }), 4, "d").detail).toBe("applied r3, desired r4");
    const old = controllerSync(rep({}, false), 4, "d");
    expect(old.label).toBe("stale");
    expect(old.detail).toMatch(/not current/);
  });
  it("error on rejection, paused on emergency pause, unknown without a report", () => {
    expect(controllerSync(rep({ status: "rejected" }), 4, "d").label).toBe("error");
    expect(controllerSync(rep({ emergency_paused: true }), 4, "d").label).toBe("paused");
    expect(controllerSync(undefined, 4, "d").label).toBe("unknown");
  });
});

describe("poolObservations", () => {
  it("ignores reports that are not current, rejected or paused", () => {
    const base = (over: Record<string, unknown>, current = true): ControllerState => ({
      controller_ref: "x", received_at: "2026-10-09T00:00:00Z", current,
      report: { revision: 1, digest: "d", status: "applied", pools: { p: { state: "exhausted", observed_at: "2026-10-09T00:00:00Z", verified: true } }, ...over },
    });
    expect(poolObservations([base({}, false)])).toEqual({});
    expect(poolObservations([base({ status: "rejected" })])).toEqual({});
    expect(poolObservations([base({ emergency_paused: true })])).toEqual({});
    expect(poolObservations([base({})]).p?.state).toBe("exhausted");
  });
  it("keeps the freshest observation per pool", () => {
    const c = (t: string, state: string): ControllerState => ({
      controller_ref: "x", received_at: t, current: true,
      report: { revision: 1, digest: "d", status: "applied", pools: { p: { state, observed_at: t, verified: true } } },
    });
    const o = poolObservations([c("2026-10-09T00:00:00Z", "available"), c("2026-10-09T01:00:00Z", "exhausted")]);
    expect(o.p?.state).toBe("exhausted");
  });
});
