import { describe, expect, it } from "vitest";
import { calculatePosition, planBoardMove } from "@/lib/board-move";
import type { Task } from "@/types";

const t = (id: string, position: number) => ({ id, position }) as unknown as Task;

describe("calculatePosition", () => {
  it("empty column -> 1; before first -> first-1; after last -> last+1; between -> midpoint", () => {
    expect(calculatePosition([], 0)).toBe(1);
    expect(calculatePosition([t("a", 5)], 0)).toBe(4);
    expect(calculatePosition([t("a", 5)], 1)).toBe(6);
    expect(calculatePosition([t("a", 1), t("b", 3)], 1)).toBe(2);
  });
});

describe("planBoardMove", () => {
  const cols = { s1: [t("a", 1), t("b", 2)], s2: [t("c", 10)] };
  const base = { tasksByColumn: cols, gateStatusByTask: {} as Record<string, string> };

  it("drop on a card of another column inserts before it", () => {
    expect(planBoardMove({ ...base, task: cols.s1[0]!, sourceColId: "s1", targetColId: "s2", overTaskId: "c" }))
      .toEqual({ status_id: "s2", position: 9 });
  });

  it("drop on the column itself appends", () => {
    expect(planBoardMove({ ...base, task: cols.s1[0]!, sourceColId: "s1", targetColId: "s2", overTaskId: null }))
      .toEqual({ status_id: "s2", position: 11 });
  });

  it("a drop that leaves the position unchanged is nothing", () => {
    // the last card dropped on the column itself stays where it is.
    expect(planBoardMove({ ...base, task: t("b", 2), tasksByColumn: { s1: [t("a", 1), t("b", 2)] }, sourceColId: "s1", targetColId: "s1", overTaskId: null }))
      .toBeNull();
  });

  it("inside one column, a drop on a card below lands after it; above lands before it", () => {
    const col = { s1: [t("a", 1), t("b", 2), t("c", 3)] };
    const mv = (id: string, over: string) =>
      planBoardMove({ ...base, tasksByColumn: col, task: col.s1.find((x) => x.id === id)!, sourceColId: "s1", targetColId: "s1", overTaskId: over });
    expect(mv("a", "b")).toEqual({ status_id: "s1", position: 2.5 });
    expect(mv("a", "c")).toEqual({ status_id: "s1", position: 4 });
    expect(mv("c", "a")).toEqual({ status_id: "s1", position: 0 });
  });

  it("a gated card listed in Triage dropped back on Triage does nothing", () => {
    expect(planBoardMove({ ...base, gateStatusByTask: { a: "Todo" }, task: cols.s1[0]!, sourceColId: "s1", targetColId: "s1", overTaskId: null }))
      .toBeNull();
  });
});
