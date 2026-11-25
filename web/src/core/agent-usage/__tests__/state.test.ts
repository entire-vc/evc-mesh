import { describe, expect, it } from "vitest";
import { DEFAULT_STATE, onlyDay, parsePeriod, quotaQuery, stateFromSearch, stateFromSettings, stateToSearch, stateToSettings, usageQuery, whoSpent, withFilter, withoutFilter } from "../state";

describe("statistics links and dashboard settings", () => {
  it("restores a shared token slice including repeated filters, stack and custom days", () => {
    const state = stateFromSearch(new URLSearchParams("m=tokens&t=cache_read&g=provider&s=agent&p=2026-11-01..2026-11-18&n=20&f.agent=a&f.agent=b&f.model=opus&f.billing_mode=subscription"));
    expect(stateFromSearch(stateToSearch(state))).toEqual(state);
    expect(stateFromSettings(stateToSettings(state))).toEqual(state);
    const query = usageQuery(state);
    expect(Object.fromEntries(query)).toMatchObject({ metric: "tokens", tokens: "cache_read", group_by: "provider", stack_by: "agent", from: "2026-11-01", to: "2026-11-18", step: "auto", top: "20" });
    expect(query.getAll("agent_id")).toEqual(["a", "b"]);
    expect(usageQuery(state, { format: "csv" }).get("format")).toBe("csv");
  });

  it("rejects invalid dates, reversed ranges, extra bounds, enums and duplicate dimensions", () => {
    for (const p of ["2026-02-30", "2026-11-20..2026-11-01", "2026-11-01..2026-11-02..2026-11-03", "bad"]) expect(parsePeriod(p)).toBe("7d");
    expect(stateFromSearch(new URLSearchParams("m=bogus&g=model&s=model&n=100"))).toEqual(DEFAULT_STATE);
  });

  it("drills a column into a whole day and removes only the selected chip", () => {
    const day = onlyDay(DEFAULT_STATE, "2026-11-13T00:00:00+03:00");
    expect(day.p).toEqual({ from: "2026-11-13", to: "2026-11-13" });
    const filtered = withFilter(day, "model", "opus");
    expect(usageQuery(filtered).get("model")).toBe("opus");
    expect(withoutFilter(filtered, "model", "opus")).toEqual(day);
  });

  it("keeps period and account when navigating from limits to contributors", () => {
    const quota = { ...DEFAULT_STATE, m: "quota" as const, w: "month" as const, f: { project: ["p"], account: ["pool"] } };
    expect(quotaQuery(quota).get("project_id")).toBeNull();
    expect(quotaQuery(quota).get("window")).toBe("month");
    expect(whoSpent(quota, "claude/bob-team")).toMatchObject({ m: "tokens", t: "total", g: "agent", s: null, p: "7d", f: { account: ["claude/bob-team"] } });
    expect(stateFromSearch(stateToSearch(quota))).toMatchObject({ m: "quota", w: "month", p: "7d" });
  });
});

describe("period days follow Moscow time", () => {
  it("a preset ends on Moscow's today, not UTC's, and 7 d spans seven days", async () => {
    const { periodRange } = await import("../controls");
    // 22:30 UTC on the 18th is already the 19th in Moscow (UTC+3).
    expect(periodRange("7d", new Date("2026-11-18T22:30:00Z"))).toEqual({ from: "2026-11-13", to: "2026-11-19", step: "day" });
    expect(periodRange("1d", new Date("2026-11-18T20:59:00Z"))).toEqual({ from: "2026-11-18", to: "2026-11-18", step: "hour" });
  });
});
