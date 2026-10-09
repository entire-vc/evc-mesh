import { describe, expect, it } from "vitest";
import { dropPeers, passesSubtaskFilter, relocateGatedToTriage } from "../board-gate-triage";
import type { StatusCategory, Task } from "@/types";

const col = (id: string, name: string, category: StatusCategory) => ({ id, status: { id, name, category } });
const triage = col("s-triage", "Triage", "triage");
const prog = col("s-prog", "In Progress", "in_progress");
const done = col("s-done", "Done", "done");
const task = (id: string, over: Partial<Task> = {}) =>
  ({ id, title: id, human_gate: false, position: 1, ...over }) as unknown as Task;
const ids = (rows: Task[] | undefined) => (rows ?? []).map((t) => t.id);
const sort = (rows: Task[]) => rows;
const all = (...rows: Task[][]) => new Set(rows.flat().map((t) => t.id));

describe("relocateGatedToTriage", () => {
  it("lists a gated card in Triage with its real status name, once", () => {
    const g = task("g", { human_gate: true });
    const plain = task("p");
    const by = { [triage.id]: [task("t")], [prog.id]: [g, plain], [done.id]: [] };
    const r = relocateGatedToTriage([triage, prog, done], by, all(...Object.values(by)), sort);
    expect(ids(r.tasksByColumn[triage.id])).toEqual(["t", "g"]);
    expect(ids(r.tasksByColumn[prog.id])).toEqual(["p"]);
    expect(r.statusNameByTask).toEqual({ g: "In Progress" });
    const every = Object.values(r.tasksByColumn).flat().map((t) => t.id);
    expect(new Set(every).size).toBe(every.length);
  });

  it("a gate that was cleared stays in its own column", () => {
    const by = { [triage.id]: [], [prog.id]: [task("g", { human_gate: false })] };
    const r = relocateGatedToTriage([triage, prog], by, all(...Object.values(by)), sort);
    expect(ids(r.tasksByColumn[prog.id])).toEqual(["g"]);
    expect(ids(r.tasksByColumn[triage.id])).toEqual([]);
    expect(r.statusNameByTask).toEqual({});
  });

  it("a done card with a gate is not shown in Triage", () => {
    const by = { [triage.id]: [], [done.id]: [task("g", { human_gate: true })] };
    const r = relocateGatedToTriage([triage, done], by, all(...Object.values(by)), sort);
    expect(ids(r.tasksByColumn[triage.id])).toEqual([]);
    expect(ids(r.tasksByColumn[done.id])).toEqual(["g"]);
  });

  it("a card already in the Triage status is not duplicated or marked", () => {
    const by = { [triage.id]: [task("g", { human_gate: true })], [prog.id]: [] };
    const r = relocateGatedToTriage([triage, prog], by, all(...Object.values(by)), sort);
    expect(ids(r.tasksByColumn[triage.id])).toEqual(["g"]);
    expect(r.statusNameByTask).toEqual({});
  });

  it("a project without a Triage column is left untouched", () => {
    const by = { [prog.id]: [task("g", { human_gate: true })] };
    const r = relocateGatedToTriage([prog], by, all(...Object.values(by)), sort);
    expect(r.tasksByColumn).toBe(by);
    expect(r.statusNameByTask).toEqual({});
  });

  it("respects board filters: a gated card filtered out is not pulled in", () => {
    const by = { [triage.id]: [], [prog.id]: [task("g", { human_gate: true })] };
    const r = relocateGatedToTriage([triage, prog], by, new Set(), sort);
    expect(ids(r.tasksByColumn[triage.id])).toEqual([]);
  });
});

describe("dropPeers", () => {
  const names = { g: "In Progress" };
  it("dropping a gated card back on Triage does nothing (never sets triage)", () => {
    expect(dropPeers("g", triage.id, triage.id, [task("g"), task("t")], names)).toBeNull();
  });
  it("dropping it on another column proceeds, as a card of its real status", () => {
    expect(ids(dropPeers("g", triage.id, prog.id, [task("p")], names) ?? undefined)).toEqual(["p"]);
  });
  it("a plain card dropped on Triage proceeds, ignoring gate-only cards for positions", () => {
    expect(ids(dropPeers("p", prog.id, triage.id, [task("g"), task("t")], names) ?? undefined)).toEqual(["t"]);
  });
  it("a real triage card reordered inside Triage still proceeds", () => {
    expect(ids(dropPeers("t", triage.id, triage.id, [task("t"), task("t2")], names) ?? undefined)).toEqual(["t2"]);
  });
});

describe("passesSubtaskFilter", () => {
  const closed = new Set(["s-done"]);
  const sub = (over: Partial<Task> = {}) =>
    ({ parent_task_id: "p", human_gate: false, status_id: "s-prog", ...over }) as unknown as Task;
  it("hides a plain subtask when the toggle is off, shows it when on", () => {
    expect(passesSubtaskFilter(sub(), false, closed)).toBe(false);
    expect(passesSubtaskFilter(sub(), true, closed)).toBe(true);
  });
  it("an open gated subtask passes with the toggle off", () => {
    expect(passesSubtaskFilter(sub({ human_gate: true }), false, closed)).toBe(true);
  });
  it("a closed gated subtask stays hidden with the toggle off", () => {
    expect(passesSubtaskFilter(sub({ human_gate: true, status_id: "s-done" }), false, closed)).toBe(false);
  });
  it("a top-level card always passes", () => {
    expect(passesSubtaskFilter(sub({ parent_task_id: undefined }), false, closed)).toBe(true);
  });
});
