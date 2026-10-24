import type { HTMLAttributes, ReactNode } from "react";
import type { TaskData } from "./slots";

// Contract: Mesh Doc "theme-slot-catalog-v1" §3 (behavioural primitives): shared ids and the data the primitives exchange.
export type Id = string;
export type IsoDate = string;

export type Permission =
  | "task.move"
  | "task.edit"
  | "task.create"
  | "doc.move"
  | "doc.edit";

export type ResourceRef = { kind: "task" | "doc" | "project"; id: Id };

export type DragItem = { kind: "task"; id: Id } | { kind: "doc"; id: Id };
export type DropZoneRef = { kind: "status"; id: Id } | { kind: "doc-parent"; id: Id | null };

export type QuickEditField =
  | "title"
  | "priority"
  | "due_date"
  | "assignee"
  | "labels"
  | `cf:${string}`;

export interface RealtimeEvent {
  type: string;
  data: Record<string, unknown>;
  at: IsoDate;
}

/** One finished drag, as the container hands it to the core. */
export interface MoveRequest {
  item: DragItem;
  from: DropZoneRef | null;
  to: DropZoneRef;
  /** The item the drop landed on; null = dropped on the zone itself (end of list). */
  overItem: DragItem | null;
  /** Insertion index among the target zone's items without the dragged one. */
  index: number;
}


export type RealtimeTopic = { kind: "project"; id: Id | null | undefined } | { kind: "workspace" };

type PassThrough = Omit<HTMLAttributes<HTMLElement>, "children" | "className"> & {
  [data: `data-${string}`]: string | undefined;
};

export interface DndProviderProps {
  /** Called once per finished drop on a zone; the core decides what the move means. */
  onMove: (move: MoveRequest) => void | Promise<void>;
  /** true on pick-up (pointer past 5px or keyboard), false after drop/cancel (once onMove settled). */
  onDragActiveChange?: (active: boolean) => void;
  renderOverlay?: (item: DragItem) => ReactNode;
  /** false: Draggables and DropZones below render without a handle / accept no drops. */
  enabled?: boolean;
  children: ReactNode;
}

export interface DropZoneProps extends PassThrough {
  zone: DropZoneRef;
  orientation: "vertical" | "horizontal";
  as?: string;
  className?: string | ((s: { isOver: boolean }) => string);
  /**
   * Ids of the Draggables inside, in DOM order. Amendment to the §3 contract:
   * the sortable list needs them up front; scanning the DOM after each render
   * would cost a second render per drop.
   */
  itemIds: readonly Id[];
  /** false: the zone lists its items but accepts no drops (e.g. board not grouped by status). */
  enabled?: boolean;
  children: ReactNode | ((s: { isOver: boolean }) => ReactNode);
}

export interface DraggableProps extends PassThrough {
  item: DragItem;
  as?: string;
  handle?: "self" | "custom";
  /** Name read out by the screen reader while the item is moved (the task title). */
  label?: string;
  /** false: no handle even with the permission (the container is not accepting drags right now). */
  enabled?: boolean;
  children: ReactNode | ((s: { isDragging: boolean; isKeyboardDragging: boolean }) => ReactNode);
}

export interface CanProps {
  permission: Permission;
  resource?: ResourceRef;
  fallback?: ReactNode;
  children: ReactNode;
}

export interface InlineEditProps {
  taskId: Id;
  field: QuickEditField;
  children: ReactNode;
}

export interface TaskLinkProps {
  taskId: Id;
  children: ReactNode;
  className?: string;
}

export interface TaskBadgesProps {
  /** The task whose `task.badge` extensions are drawn (the card's own `data.task`). */
  task: TaskData;
}
