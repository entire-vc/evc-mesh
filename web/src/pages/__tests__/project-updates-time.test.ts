import { describe, expect, it } from "vitest";
import { formatPublished } from "../project-updates";

describe("formatPublished", () => {
  it("distinguishes two updates of the same day by time", () => {
    const a = formatPublished("2026-11-03T09:05:00Z");
    const b = formatPublished("2026-11-03T14:40:00Z");
    expect(a?.date).toBe(b?.date);
    expect(a?.time).not.toBe(b?.time);
    expect(a?.time).toMatch(/\d{2}:\d{2}/);
  });
  it("matches the local-zone time of the API timestamp", () => {
    const iso = "2026-11-03T14:40:00Z";
    const d = new Date(iso);
    const hh = String(d.getHours()).padStart(2, "0");
    const mm = String(d.getMinutes()).padStart(2, "0");
    expect(formatPublished(iso)?.time).toBe(`${hh}:${mm}`);
  });
  it("does not invent a time for date-only or invalid values", () => {
    expect(formatPublished("2026-11-03")?.time).toBeNull();
    expect(formatPublished("garbage")).toBeNull();
    expect(formatPublished("")).toBeNull();
  });
  it("rejects impossible calendar dates without throwing", () => {
    expect(() => formatPublished("2026-02-30")).not.toThrow();
    expect(formatPublished("2026-02-30")).toBeNull();
    expect(formatPublished("2026-02-30T10:00:00Z")).toBeNull();
    expect(formatPublished("2026-13-01T10:00:00Z")).toBeNull();
  });
});
