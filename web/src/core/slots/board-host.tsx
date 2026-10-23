import { memo } from "react";
import { loadRuntimeFlags } from "@/lib/runtime-flags";
import type {
  BoardCardView,
  BoardColumnView,
  BoardLayoutComponents,
  BoardLayoutSettings,
  SlotProps,
  BoardCardViewData,
  BoardColumnViewData,
  BoardCardActions,
  BoardColumnActions,
  BoardCardSettings,
} from "./types";
import { resolveSlotView, withSlotFallback } from "./registry";

// Fires once at module load. By the time a real drag or click can happen the fetch has
// long since resolved into window.__MESH_FLAGS__, which the (necessarily synchronous)
// memo comparators below read directly. Before it resolves, and if it never does, memoization
// stays on: the same "absent means on" contract every flag in runtime-flags.ts uses.
void loadRuntimeFlags();

export function boardCardMemoEnabled(): boolean {
  return window.__MESH_FLAGS__?.boardCardMemo ?? true;
}

// The contract hands every view fresh `data`/`actions` objects on each render. A card or a
// column must not re-render for that alone (a drag touches one column; the hot path is under
// the frontend-perf budget), so the core memoizes the slot component by what the objects hold.
// `actions` are call-throughs onto stable container callbacks, so their identity is not compared.

export function cardPropsEqual(
  prev: SlotProps<BoardCardViewData, BoardCardActions, BoardCardSettings>,
  next: SlotProps<BoardCardViewData, BoardCardActions, BoardCardSettings>,
): boolean {
  if (!boardCardMemoEnabled()) return false;
  const a = prev.data;
  const b = next.data;
  return (
    // task is reference-compared on purpose: the task store keeps the identity of every task a
    // move did not touch, and toTaskData keeps the identity of its result for the same task.
    a.task === b.task &&
    a.column.id === b.column.id &&
    a.column.kind === b.column.kind &&
    a.column.statusCategory === b.column.statusCategory &&
    a.gateStatusName === b.gateStatusName &&
    a.fields === b.fields &&
    a.selected === b.selected &&
    a.isOverlay === b.isOverlay &&
    Boolean(prev.actions.quickEdit) === Boolean(next.actions.quickEdit) &&
    prev.themeSettings === next.themeSettings &&
    prev.ctx === next.ctx
  );
}

export function columnPropsEqual(
  prev: SlotProps<BoardColumnViewData, BoardColumnActions, BoardLayoutSettings>,
  next: SlotProps<BoardColumnViewData, BoardColumnActions, BoardLayoutSettings>,
): boolean {
  if (!boardCardMemoEnabled()) return false;
  const a = prev.data;
  const b = next.data;
  return (
    a.column === b.column &&
    a.index === b.index &&
    a.total === b.total &&
    a.dnd.enabled === b.dnd.enabled &&
    a.dnd.reason === b.dnd.reason &&
    a.fields === b.fields &&
    a.components.Card === b.components.Card &&
    Boolean(prev.actions.createTask) === Boolean(next.actions.createTask) &&
    Boolean(prev.actions.quickEdit) === Boolean(next.actions.quickEdit) &&
    prev.themeSettings === next.themeSettings &&
    prev.ctx === next.ctx
  );
}

const memoized = new WeakMap<object, unknown>();
function memoOnce<V extends object>(view: V, equal: (a: never, b: never) => boolean): V {
  let m = memoized.get(view);
  if (!m) {
    m = memo(view as never, equal as never);
    memoized.set(view, m);
  }
  return m as V;
}

/** What the board layout renders its columns and cards with: the active views, memoized (one memo per view). */
export function resolveBoardComponents(): BoardLayoutComponents {
  return {
    Card: memoOnce(withSlotFallback("board.card", resolveSlotView("board.card")) as BoardCardView, cardPropsEqual),
    Column: memoOnce(withSlotFallback("board.column", resolveSlotView("board.column")) as BoardColumnView, columnPropsEqual),
  };
}
