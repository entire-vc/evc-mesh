import { describe, expect, it } from "vitest";
import { previousPeriod } from "../filters";
import { analyticsParams } from "../use-analytics";

const SECOND = 1_000;
const MINUTE = 60_000;

const at = (v = "") => Date.parse(v.includes("T") ? v : `${v}T00:00:00Z`);

/**
 * What the server is asked for, as [first, last] instants: it counts both bounds (`>= from AND <= to`).
 * A preset without `to` ends at `now`.
 */
function bounds(params: Record<string, string>, now: Date): [number, number] {
  return [at(params.from), params.to ? Date.parse(params.to) : now.getTime()];
}

/** Length of the counted span; the last counted second of a day ends at the next midnight. */
const length = ([first, last]: [number, number], closedSecond: boolean) => last - first + (closedSecond ? SECOND : 0);

const counts = ([first, last]: [number, number], event: number) => event >= first && event <= last;

describe("previousPeriod", () => {
  // At noon the current preset already holds half of today; the previous one must hold as much, just before it.
  for (const now of ["2026-11-15T12:00:42Z", "2026-11-15T00:00:30Z", "2026-11-15T23:59:59Z"]) {
    for (const days of [7, 30, 90] as const) {
      it(`${days} days at ${now}: the previous period is as long as the current one and ends where it starts`, () => {
        const t = new Date(now);
        const cur = bounds(analyticsParams({ period: { days } }, t), t);
        const prev = bounds(analyticsParams({ period: previousPeriod({ days }, t) }, t), t);
        // adjacent: the last counted second of the previous period is the one right before the current start
        expect(prev[1]).toBe(cur[0] - SECOND);
        // no common part
        expect(prev[1]).toBeLessThan(cur[0]);
        // the same length, up to the minute the start is cut to
        const gap = length(cur, false) - length(prev, true);
        expect(gap).toBeGreaterThanOrEqual(0);
        expect(gap).toBeLessThan(MINUTE);
      });
    }
  }

  it("an event in the part of today's first day already gone is counted by the current period only", () => {
    const t = new Date("2026-11-15T12:00:42Z");
    const cur = bounds(analyticsParams({ period: { days: 7 } }, t), t);
    const prev = bounds(analyticsParams({ period: previousPeriod({ days: 7 }, t) }, t), t);
    const inside = Date.parse("2026-11-08T06:00:00Z");
    expect(counts(cur, inside)).toBe(true);
    expect(counts(prev, inside)).toBe(false);
    const before = Date.parse("2026-11-07T23:59:59Z");
    expect(counts(prev, before)).toBe(true);
    expect(counts(cur, before)).toBe(false);
  });

  it("names the values: 7 days at noon run back by 7 days and the same half day", () => {
    const now = new Date("2026-11-15T12:00:42Z");
    expect(analyticsParams({ period: { days: 7 } }, now)).toEqual({ from: "2026-11-08" });
    expect(previousPeriod({ days: 7 }, now)).toEqual({ from: "2026-10-31T12:00:00Z", to: "2026-11-07" });
    expect(analyticsParams({ period: previousPeriod({ days: 7 }, now) }, now)).toEqual({ from: "2026-10-31T12:00:00Z", to: "2026-11-07T23:59:59Z" });
  });

  it("tiles drawn within the same minute ask the same previous period (one shared request)", () => {
    const a = previousPeriod({ days: 30 }, new Date("2026-11-15T12:00:01Z"));
    const b = previousPeriod({ days: 30 }, new Date("2026-11-15T12:00:59Z"));
    expect(a).toEqual(b);
  });

  it("control: a custom range keeps whole days, as long as itself and right before it", () => {
    const now = new Date("2026-11-15T12:00:00Z");
    const prev = previousPeriod({ from: "2026-10-01", to: "2026-10-31" }, now);
    expect(prev).toEqual({ from: "2026-08-31", to: "2026-09-30" });
    const cur = bounds(analyticsParams({ period: { from: "2026-10-01", to: "2026-10-31" } }, now), now);
    const before = bounds(analyticsParams({ period: prev }, now), now);
    expect(length(before, true)).toBe(length(cur, true));
    expect(before[1]).toBe(cur[0] - SECOND);
  });
});
