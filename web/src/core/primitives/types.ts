// Contract: Mesh Doc "theme-slot-catalog-v1" §3 (behavioural primitives).
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

export function zoneKey(zone: DropZoneRef): string {
  return `zone:${zone.kind}:${zone.id ?? "root"}`;
}
