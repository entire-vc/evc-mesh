import type { Task } from "@/types";
import { dropPeers } from "@/lib/board-gate-triage";

/** Position for a card inserted at targetIndex among `tasks` (sorted by position). */
export function calculatePosition(tasks: Task[], targetIndex: number): number {
  if (tasks.length === 0) return 1;
  if (targetIndex <= 0) {
    const first = tasks[0];
    return first ? first.position - 1 : 1;
  }
  if (targetIndex >= tasks.length) {
    const last = tasks[tasks.length - 1];
    return last ? last.position + 1 : 1;
  }
  const prev = tasks[targetIndex - 1];
  const next = tasks[targetIndex];
  if (prev && next) return (prev.position + next.position) / 2;
  return targetIndex;
}

export interface BoardMovePlan {
  status_id: string;
  position: number;
}

/**
 * Same as planBoardMove for a target given as an insertion index among the target
 * column's cards without the dragged one (the `board.*` contract's `position.index`).
 */
export function planMoveToIndex(args: {
  task: Task;
  sourceColId: string;
  targetColId: string;
  index: number;
  tasksByColumn: Record<string, Task[]>;
  gateStatusByTask: Record<string, string>;
}): BoardMovePlan | null {
  const { task, sourceColId, targetColId, index, tasksByColumn, gateStatusByTask } = args;
  const peers = dropPeers(task.id, sourceColId, targetColId, tasksByColumn[targetColId] ?? [], gateStatusByTask);
  if (!peers) return null;
  const position = calculatePosition(peers, Math.max(0, Math.min(index, peers.length)));
  if (sourceColId === targetColId && task.position === position) return null;
  return { status_id: targetColId, position };
}

/**
 * What a finished drop means for the store: null = nothing to do, else the
 * status and fractional position to send. `overTaskId` is the card the drop
 * landed on (null = the column itself, i.e. the end).
 */
export function planBoardMove(args: {
  task: Task;
  sourceColId: string;
  targetColId: string;
  overTaskId: string | null;
  tasksByColumn: Record<string, Task[]>;
  gateStatusByTask: Record<string, string>;
}): BoardMovePlan | null {
  const { task, sourceColId, targetColId, overTaskId, tasksByColumn, gateStatusByTask } = args;
  // Gated cards listed in Triage only by gate keep their real status.
  const peers = dropPeers(task.id, sourceColId, targetColId, tasksByColumn[targetColId] ?? [], gateStatusByTask);
  if (!peers) return null;

  let dropIndex = peers.length;
  if (overTaskId !== null) {
    const idx = peers.findIndex((t) => t.id === overTaskId);
    if (idx !== -1) {
      dropIndex = idx;
      // Inside one column, dropping on a card below the dragged one lands after it.
      if (sourceColId === targetColId) {
        const col = tasksByColumn[targetColId] ?? [];
        const from = col.findIndex((t) => t.id === task.id);
        const over = col.findIndex((t) => t.id === overTaskId);
        if (from !== -1 && over !== -1 && from < over) dropIndex = idx + 1;
      }
    }
  }
  const position = calculatePosition(peers, dropIndex);
  if (sourceColId === targetColId && task.position === position) return null;
  // For status grouping the column id IS the status id.
  return { status_id: targetColId, position };
}
