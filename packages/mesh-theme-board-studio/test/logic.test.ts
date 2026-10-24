import { describe, expect, it } from "vitest";
import { dimmed, focus, horizon, initials, lanes, nextDay, raised, say, wip } from "../src/logic";

describe("copy", () => {
  it("speaks the locale, falls back to English, fills placeholders", () => {
    expect(say("ru-RU", "everyone")).toBe("Все");
    expect(say("de", "everyone")).toBe("Everyone");
    expect(say("en", "focusEmpty", { days: 7 })).toBe("Nothing due in the next 7 days");
    expect(say("en", "no-such-key")).toBe("no-such-key");
  });
});

describe("quick actions", () => {
  it("raises one step and stops at urgent", () => {
    expect(raised("none")).toBe("low");
    expect(raised("high")).toBe("urgent");
    expect(raised("urgent")).toBe("urgent");
    expect(raised("weird")).toBe("low");
  });
  it("postpones by one calendar day, from the due date or from today", () => {
    expect(nextDay("2026-10-31T09:00:00Z")).toBe("2026-11-01");
    expect(nextDay(null, new Date("2026-02-28T12:00:00Z"))).toBe("2026-03-01");
  });
});

describe("assignee focus", () => {
  const cards = [
    { assignee: { id: "a", name: "Ann" } }, { assignee: { id: "b", name: "Bo" } }, { assignee: { id: "b", name: "Bo" } }, { assignee: null },
  ];
  it("lists people busiest first, unassigned last", () => {
    expect(lanes(cards).map((l) => [l.key, l.count])).toEqual([["b", 2], ["a", 1], ["none", 1]]);
  });
  it("dims everyone but the chosen one; no choice dims nothing", () => {
    expect(dimmed(null, "a")).toBe(false);
    expect(dimmed("a", "a")).toBe(false);
    expect(dimmed("a", "b")).toBe(true);
    expect(dimmed("none", null)).toBe(false);
    expect(dimmed("none", "a")).toBe(true);
  });
  it("initials of one and of several words", () => {
    expect(initials("Ann")).toBe("AN");
    expect(initials("Ann Lee Smith")).toBe("AS");
    expect(initials("  ")).toBe("?");
  });
});

describe("wip", () => {
  it("is none without a limit, then ok, full, over", () => {
    expect([wip(3, null), wip(3, 0), wip(2, 3), wip(3, 3), wip(4, 3)]).toEqual(["none", "none", "ok", "full", "over"]);
  });
});

describe("focus widget selection", () => {
  const now = Date.parse("2026-10-24T10:00:00Z");
  const t = (id: string, due?: string) => ({ id, title: id, due_date: due });
  it("splits overdue from coming up, drops undated and out-of-horizon, sorts by date", () => {
    const r = focus([t("late2", "2026-10-20T00:00:00Z"), t("none"), t("far", "2026-12-01T00:00:00Z"), t("today", "2026-10-24T18:00:00Z"), t("late1", "2026-10-10T00:00:00Z"), t("soon", "2026-10-28T00:00:00Z")], 7, now);
    expect(r.overdue.map((x) => x.id)).toEqual(["late1", "late2"]);
    expect(r.soon.map((x) => x.id)).toEqual(["today", "soon"]);
  });
  it("a due date earlier today is not overdue yet", () => {
    expect(focus([t("morning", "2026-10-24T01:00:00Z")], 3, now).overdue).toEqual([]);
  });
  it("horizon accepts 1..30 whole numbers and falls back to 7", () => {
    expect([horizon(3), horizon(0), horizon(31), horizon("5"), horizon(undefined), horizon(2.6)]).toEqual([3, 7, 7, 7, 7, 3]);
  });
});
