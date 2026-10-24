import type { BoardColumnData, BoardLayoutData, Id } from "../../src/index";
import { FIELD, fields, status, task } from "./common";

export interface BoardFixtureOpts {
  /** Column ids in order; default Backlog / In progress / Done. */
  columns?: Id[];
  /** Tasks per column (ids `t1`, `t2`, ... in column order); default 2 / 1 / 0. */
  tasksPerColumn?: number[];
  /** A task waiting on a human gate: shown once, in Triage, not in its status column. */
  gated?: boolean;
}

export function column(id: Id, tasks: BoardColumnData["tasks"], over: Partial<BoardColumnData> = {}): BoardColumnData {
  return {
    id, kind: "status", title: id, color: "#888", tasks, totalCount: tasks.length, countLabel: String(tasks.length),
    wipLimit: null, isClosed: false, hasMore: false, loadingMore: false, status: status(id), ...over,
  };
}

/** `board.layout` data without the injected `components` (the harness adds them). */
export function board(opts: BoardFixtureOpts = {}): Omit<BoardLayoutData, "columns"> & { columns: Extract<BoardLayoutData["columns"], { state: "ready" }> } {
  const ids = opts.columns ?? ["backlog", "progress", "done"];
  const counts = opts.tasksPerColumn ?? [2, 1, 0];
  let n = 0;
  const cols = ids.map((id, i) =>
    column(id, Array.from({ length: counts[i] ?? 0 }, () => task(`t${++n}`, { statusId: id })), {
      isClosed: i === ids.length - 1,
    }),
  );
  if (opts.gated) {
    const gated = task("gated1", { statusId: "backlog", gate: { class: "hard", reason: "needs a decision" } });
    cols.unshift(column("triage", [gated], { kind: "triage", status: undefined, gateStatusNameByTask: { gated1: "Backlog" } }));
  }
  return {
    project: { id: "p1", slug: "proj", name: "Project" },
    groupBy: "status",
    sortBy: "manual",
    columns: { state: "ready", value: cols },
    dnd: { enabled: true, nonDroppableColumnIds: opts.gated ? ["triage"] : [] },
    availableFields: fields(),
    cardFields: [FIELD.priority.key, FIELD.due_date.key, FIELD.sprint.key],
  };
}
