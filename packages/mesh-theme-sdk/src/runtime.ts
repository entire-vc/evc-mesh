// The runtime half of the contract: primitives and data hooks a theme calls. Declarations only, on
// purpose: a theme bundle keeps `@entire-vc/mesh-theme-sdk` external and the host's import map hands
// it the core's one implementation (ADR D10). Everything declared here is mandatory for the host:
// the core builds its export object against `typeof import("@entire-vc/mesh-theme-sdk")`, so a
// declaration the core does not implement, or implements differently, fails its typecheck.
import type { FC, ReactElement } from "react";
import type { DashboardAsync, MentionsData, TaskQuery, TasksData, WidgetViewDataMap, SessionItem } from "./dashboard";
import type {
  CanProps,
  DndProviderProps,
  DraggableProps,
  DropZoneProps,
  Id,
  InlineEditProps,
  Permission,
  RealtimeEvent,
  RealtimeTopic,
  ResourceRef,
  TaskLinkProps,
  TaskBadgesProps,
} from "./primitives";
import type { SlotContext } from "./slots";

// -- behavioural primitives (catalog §3) -------------------------------------------------------

/** The one DnD container of a page: pointer + keyboard sensors, announcements, overlay, move resolution. */
export declare function DndProvider(props: DndProviderProps): ReactElement;

/** Draggable object; draws a handle, aria and keyboard only with the right to move. */
export declare const Draggable: ((props: DraggableProps) => ReactElement) & {
  /** The grab area of a Draggable with `handle="custom"`. */
  Handle: FC<{ children: React.ReactNode }>;
};

/** Drop zone. DOM order of the Draggables inside it is the insertion order. */
export declare function DropZone(props: DropZoneProps): ReactElement;

/** Renders children only with the permission, `fallback` (default null) otherwise. */
export declare function Can(props: CanProps): ReactElement;
export declare function useCan(permission: Permission, resource?: ResourceRef): boolean;

/** Edit in place; without `task.edit` it is just its children. */
export declare function InlineEdit(props: InlineEditProps): ReactElement;

/** A real href with an in-app click that calls `actions.openTask`. */
export declare function TaskLink(props: TaskLinkProps): ReactElement;

/** The insertion point for `task.badge` extensions (a board.card must carry it; CT-board-14, CT-common-04). */
export declare function TaskBadges(props: TaskBadgesProps): ReactElement;

/** Realtime events with the user's rights; unsubscribes on unmount. */
export declare function useRealtime(topic: RealtimeTopic, onEvent: (e: RealtimeEvent) => void): { connected: boolean };

// -- data hooks (slots-dashboard §3, catalog §2) ---------------------------------------------

/** Tasks for a theme's own widget: closed filter set, current user's rights, no fetch in the view. */
export declare function useTasks(q: TaskQuery): DashboardAsync<TasksData>;

/** The registry's data hook for `widgetId`; the id must stay the same for the life of the caller. */
export declare function useWidgetData<K extends keyof WidgetViewDataMap>(widgetId: K): WidgetViewDataMap[K];
export declare function useWidgetData(widgetId: string): DashboardAsync<unknown>;

export declare function useMentions(): DashboardAsync<MentionsData>;
export declare function useMyTasks(): DashboardAsync<TasksData>;
export declare function useTriageTasks(): DashboardAsync<TasksData>;
export declare function useActiveSessions(): DashboardAsync<{ items: SessionItem[] }>;

/**
 * Full context of the slot being rendered, for components nested in a view. For every slot except the
 * dashboard it is the same object the view got as `props.ctx`. A dashboard view's `props.ctx` is the
 * smaller `DashboardSlotCtx`; this hook still returns the full `SlotContext` (slot `dashboard.layout` or
 * `dashboard.widget.<id>`). Throws outside a slot view.
 */
export declare function useSlotContext(): SlotContext;

export type { Id };
