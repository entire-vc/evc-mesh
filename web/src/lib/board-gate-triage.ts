import type { StatusCategory, Task } from "@/types";

interface ColumnLike {
  id: string;
  status?: { id: string; name: string; category: StatusCategory };
}

const CLOSED: StatusCategory[] = ["done", "cancelled"];

/**
 * A card waiting on a human keeps its real status (moving it would break the
 * feed and cause triage<->in_progress loops), so the Triage column shows it by
 * gate instead. Each such card is listed ONCE, in Triage, with its real status
 * name in `statusNameByTask`; it is removed from its own column so card ids
 * stay unique for drag-and-drop. Closed cards are never relocated, and a
 * project without a Triage column is left untouched (the card keeps its own
 * column and its gate badge). Pure: returns new column lists, mutates nothing.
 */
export function relocateGatedToTriage(
  columns: ColumnLike[],
  tasksByColumn: Record<string, Task[]>,
  visibleTaskIds: ReadonlySet<string>,
  sort: (tasks: Task[]) => Task[],
): { tasksByColumn: Record<string, Task[]>; statusNameByTask: Record<string, string> } {
  const triage = columns.find((c) => c.status?.category === "triage");
  if (!triage) return { tasksByColumn, statusNameByTask: {} };

  const out: Record<string, Task[]> = { ...tasksByColumn };
  const statusNameByTask: Record<string, string> = {};
  const moved: Task[] = [];
  for (const col of columns) {
    const st = col.status;
    if (!st || col.id === triage.id || st.category === "triage" || CLOSED.includes(st.category)) continue;
    const rows = tasksByColumn[col.id] ?? [];
    const keep = rows.filter((t) => {
      const gated = t.human_gate && visibleTaskIds.has(t.id);
      if (gated) {
        moved.push(t);
        statusNameByTask[t.id] = st.name;
      }
      return !gated;
    });
    if (keep.length !== rows.length) out[col.id] = keep;
  }
  if (moved.length === 0) return { tasksByColumn, statusNameByTask };
  out[triage.id] = sort([...(tasksByColumn[triage.id] ?? []), ...moved]);
  return { tasksByColumn: out, statusNameByTask };
}

/**
 * Drop handling for a column that may list gated cards by gate only. Returns
 * null when the drop must do nothing (a gated card put back on the Triage
 * column it is only listed in would otherwise set the triage status), else the
 * tasks that really sit in the target status, for position maths.
 */
export function dropPeers(
  draggedId: string,
  sourceColId: string,
  targetColId: string,
  targetTasks: Task[],
  statusNameByTask: Record<string, string>,
): Task[] | null {
  if (statusNameByTask[draggedId] !== undefined && sourceColId === targetColId) return null;
  return targetTasks.filter((t) => t.id !== draggedId && statusNameByTask[t.id] === undefined);
}
