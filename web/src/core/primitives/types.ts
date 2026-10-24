// Moved to the SDK package (packages/mesh-theme-sdk); `zoneKey` is the core's own helper.
import type { DropZoneRef } from "@entire-vc/mesh-theme-sdk";

export type {
  Id, IsoDate, Permission, ResourceRef, DragItem, DropZoneRef, QuickEditField, RealtimeEvent, MoveRequest,
} from "@entire-vc/mesh-theme-sdk";

export function zoneKey(zone: DropZoneRef): string {
  return `zone:${zone.kind}:${zone.id ?? "root"}`;
}
