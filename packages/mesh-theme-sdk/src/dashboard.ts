// Contract: Mesh Doc "slots-dashboard" (dashboard.layout, dashboard.widget.<id>).
import type { ComponentType } from "react";
import type { MentionSource, StatusCategory, Task, TeamDirectoryAgent } from "./api";
import type { Id, Permission } from "./primitives";

export type DashboardAsync<T> =
  | { state: "loading" }
  | { state: "error"; error: Error; retry: () => void }
  | { state: "ready"; value: T };

/** Current contract version of the dashboard.layout slot (slot-contracts: web/src/core/themes/versions.ts, internal/themes/contract.go). */
export const DASHBOARD_LAYOUT_VERSION = "2.0.0";

/** Grid units of dashboard.layout 2.0.0: 12 columns, rows of the same height as one v1 row unit. */
export type GridUnit = 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9 | 10 | 11 | 12;
export type WidgetSize = { w: GridUnit; h: GridUnit };

/** dashboard.layout 1.x size: 1..4 columns, 1..3 rows. */
export type WidgetSizeV1 = { w: 1 | 2 | 3 | 4; h: 1 | 2 | 3 };

export type CoreWidgetId = "mentions" | "triage" | "my-tasks" | "sessions" | "triage-queue" | "task-list" | "my-comments" | "recent-comments";

export type WidgetOrigin = "core" | "extension" | "theme" | "external";
export type WidgetScope = "workspace" | "project" | "user";

/** JSON Schema of a widget's settings; a property picks its control with `x-widget` (SchemaForm). */
export interface WidgetSettingsSchema {
  type: "object";
  properties: Record<string, { type?: string; title?: string; "x-widget"?: string; [k: string]: unknown }>;
  required?: string[];
  [k: string]: unknown;
}

/** Widget descriptor of dashboard.layout 2.0.0. */
export interface WidgetDescriptor {
  /** Core id, or "<theme-id>.<name>" for a theme's own widget. */
  id: string;
  title: string;
  description: string;
  category: string;
  origin: WidgetOrigin;
  /** In grid cells (12 columns, w 1..12, h 1..12). */
  size: { default: WidgetSize; min: WidgetSize; max: WidgetSize };
  settings?: WidgetSettingsSchema;
  scope: WidgetScope;
  requires?: Permission[];
  actions: string[];
  /** Only the four legacy widgets (and a v1 theme's own widget) have one: they read their data through a core hook. */
  dataHook?: string;
}

/** A widget placed on the grid. `x` is 0-based, `x + w <= 12`. */
export interface WidgetInstance {
  id: string;
  /** Descriptor id. */
  type: string;
  x: number;
  y: number;
  w: number;
  h: number;
  settings: Record<string, unknown>;
}

/** Props of a v2 widget view (a group's `views` entry). */
export interface WidgetViewProps<S = Record<string, unknown>, A = Record<string, unknown>> {
  instance: WidgetInstance;
  settings: S;
  size: WidgetSize;
  actions: A;
}

/** Value of the SchemaForm `period` control. */
export type PeriodValue = { days: 7 | 30 | 90 } | { from: string; to: string };
/** Value of the SchemaForm `event-type-picker` control. */
export type EventTypeValue = string[];

/** Widget descriptor of dashboard.layout 1.x (kept for the v1 adapter). */
export interface WidgetDescriptorV1 {
  id: string;
  title: string;
  description: string;
  origin: "core" | "extension" | "theme";
  dataHook: string;
  actions: string[];
  size: { default: WidgetSizeV1; min: WidgetSizeV1; max: WidgetSizeV1 };
  requires?: Permission[];
}

export interface MentionItem {
  id: Id;
  source: MentionSource;
  title: string;
  at: string;
  seen: boolean;
  /** Where the row points today; null = no honest target, fall back to the activity page. */
  href: string | null;
  target: { kind: "task"; taskId: Id; commentId: Id } | { kind: "document"; docId: Id; projectSlug?: string };
  /** Who mentioned you. */
  authorId?: Id;
  authorName?: string;
  /** Comment text (markdown), unclipped. */
  body?: string;
  /** The comment asks a question (contains "❓"). */
  question?: boolean;
  /** new = not shown to the user yet; shown = listed but not opened; opened = seen on the server. */
  state?: "new" | "shown" | "opened";
  /** Arrived since the user's last visit to the mentions list. */
  fresh?: boolean;
  projectId?: Id;
}

/** One comment as the comment views return it (my-comments, recent-comments). */
export interface CommentItem {
  id: Id;
  taskId: Id;
  title: string;
  projectId: Id;
  projectName: string;
  /** The project's icon value (emoji, `icon:<name>`, legacy text or ""); draw it with EntityIcon. */
  projectIcon?: string;
  body: string;
  authorId: Id;
  authorName: string;
  authorKind: string;
  internal: boolean;
  at: string;
  href: string | null;
}

export interface CommentsData {
  items: CommentItem[];
  /** More pages behind the cursor ("Ещё"). */
  hasMore: boolean;
  loadingMore: boolean;
}

export interface SessionItem {
  agent: TeamDirectoryAgent;
  status: string;
}

export interface MentionsData {
  items: MentionItem[];
  failedSources: MentionSource[];
  /** Unseen among the loaded rows. */
  unseen?: number;
}
export interface TasksData {
  items: Task[];
  total: number;
  projectNames: Record<Id, string>;
  /** Project icon values by id (emoji, `icon:<name>`, legacy text or ""); draw them with EntityIcon. */
  projectIcons?: Record<Id, string>;
}

export interface WidgetViewDataMap {
  mentions: DashboardAsync<MentionsData>;
  triage: DashboardAsync<TasksData>;
  "my-tasks": DashboardAsync<TasksData>;
  sessions: DashboardAsync<{ items: SessionItem[] }>;
  /** The triage inbox in server order; `total` is the whole queue. */
  "triage-queue": DashboardAsync<TasksData>;
  /** Tasks by the instance's TaskQuery; `total` counts every match. */
  "task-list": DashboardAsync<TasksData>;
  "my-comments": DashboardAsync<CommentsData>;
  "recent-comments": DashboardAsync<CommentsData>;
}

export interface WidgetActionsMap {
  mentions: { openMention(item: MentionItem, opts?: { newTab?: boolean }): void; retry(): void };
  "my-comments": { openComment(item: CommentItem, opts?: { newTab?: boolean }): void; loadMore(): void; retry(): void };
  "recent-comments": { openComment(item: CommentItem, opts?: { newTab?: boolean }): void; loadMore(): void; retry(): void };
  triage: { openTask(taskId: Id, opts?: { newTab?: boolean }): void; retry(): void };
  "my-tasks": { openTask(taskId: Id, opts?: { newTab?: boolean }): void; retry(): void };
  sessions: { openAgent(agentId: Id): void; retry(): void };
  "triage-queue": {
    openTask(taskId: Id, opts?: { newTab?: boolean }): void;
    /** Opens the core's move dialog (project, then a status outside triage). Present only with `task.move`. */
    moveTask?(taskId: Id): void;
    retry(): void;
  };
  "task-list": { openTask(taskId: Id, opts?: { newTab?: boolean }): void; retry(): void };
}

export interface DashboardSlotCtx {
  isMobile: boolean;
  wsSlug?: string;
}

export type DashboardSlotView<D, A, S> = ComponentType<{ data: D; actions: A; settings: S; ctx: DashboardSlotCtx }>;

export interface DashboardLayoutSettings {
  widgets?: string[];
  columns?: 1 | 2 | 3 | 4;
  layout_variant?: string;
  [themeKey: string]: unknown;
}

/** dashboard.layout 2.0.0 data. */
export interface DashboardLayoutData {
  columns: 12;
  widgets: { descriptor: WidgetDescriptor; instance: WidgetInstance }[];
  /** Descriptors the user may add (allowed, not placed yet). */
  available: WidgetDescriptor[];
  components: { Widget: ComponentType<{ instance: WidgetInstance }> };
  editing: boolean;
  canEdit: boolean;
}
/** Edit actions exist only when `canEdit`. */
export interface DashboardLayoutActions {
  openTask(taskId: Id, opts?: { newTab?: boolean }): void;
  moveWidget?(id: string, to: { x: number; y: number }): Promise<void>;
  resizeWidget?(id: string, to: { w: number; h: number }): Promise<void>;
  addWidget?(type: string, at?: { x: number; y: number }): Promise<void>;
  removeWidget?(id: string): Promise<void>;
  updateWidgetSettings?(id: string, settings: Record<string, unknown>): Promise<void>;
}
export type DashboardLayoutView = DashboardSlotView<DashboardLayoutData, DashboardLayoutActions, DashboardLayoutSettings>;

/** dashboard.layout 1.x data, served to a theme that declared `^1` (ADR D6: one more release). */
export interface DashboardLayoutDataV1 {
  widgets: { descriptor: WidgetDescriptorV1; size: WidgetSizeV1; hidden: false }[];
  available: WidgetDescriptorV1[];
  components: { Widget: ComponentType<{ id: string; size?: WidgetSizeV1 }> };
  editing: boolean;
}
export interface DashboardLayoutActionsV1 {
  openTask(taskId: Id, opts?: { newTab?: boolean }): void;
  setWidgetOrder?(ids: string[]): Promise<void>;
}
export type DashboardLayoutViewV1 = DashboardSlotView<DashboardLayoutDataV1, DashboardLayoutActionsV1, DashboardLayoutSettings>;

// -- the v1 adapter ------------------------------------------------------------------------------

const clampTo = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));
export const sizeToV1 = (s: { w: number; h: number }): WidgetSizeV1 => ({
  w: clampTo(Math.ceil(s.w / 3), 1, 4) as WidgetSizeV1["w"],
  h: clampTo(s.h, 1, 3) as WidgetSizeV1["h"],
});
const toV2Size = (s: WidgetSizeV1): WidgetSize => ({ w: (s.w * 3) as GridUnit, h: s.h });

export function descriptorToV1(d: WidgetDescriptor): WidgetDescriptorV1 {
  return {
    id: d.id, title: d.title, description: d.description,
    origin: d.origin === "external" ? "extension" : d.origin,
    dataHook: d.dataHook ?? "", actions: d.actions,
    size: { default: sizeToV1(d.size.default), min: sizeToV1(d.size.min), max: sizeToV1(d.size.max) },
    ...(d.requires ? { requires: d.requires } : {}),
  };
}

/** A v1 descriptor (a v1 theme's own widget) as a v2 one: columns x3, rows as they are. */
export function descriptorFromV1(d: WidgetDescriptorV1): WidgetDescriptor {
  return {
    id: d.id, title: d.title, description: d.description, category: d.origin, origin: d.origin,
    scope: "user", actions: d.actions, dataHook: d.dataHook,
    size: { default: toV2Size(d.size.default), min: toV2Size(d.size.min), max: toV2Size(d.size.max) },
    ...(d.requires ? { requires: d.requires } : {}),
  };
}

/**
 * dashboard.layout 2 -> 1: widgets in `(y, x)` order, sizes cut to the v1 limits, `editing` false (the v1 view cannot
 * edit). `Widget` is the host's v1 component: it looks an instance up by descriptor id.
 */
export function toLayoutV1(
  data: Omit<DashboardLayoutData, "components">,
  Widget: DashboardLayoutDataV1["components"]["Widget"],
): DashboardLayoutDataV1 {
  const ordered = [...data.widgets].sort((a, b) => a.instance.y - b.instance.y || a.instance.x - b.instance.x);
  return {
    widgets: ordered.map((w) => ({ descriptor: descriptorToV1(w.descriptor), size: sizeToV1(w.instance), hidden: false as const })),
    available: data.available.map(descriptorToV1),
    components: { Widget },
    editing: false,
  };
}

/** Edit actions of v2 are not passed to a v1 view. */
export function toActionsV1(a: DashboardLayoutActions): DashboardLayoutActionsV1 {
  return { openTask: a.openTask };
}

/** Data of a widget view. A theme's own widget gets no `result`: it reads SDK hooks. */
export interface DashboardWidgetViewData<K extends string = string> {
  id: K;
  descriptor: WidgetDescriptorV1;
  result: K extends keyof WidgetViewDataMap ? WidgetViewDataMap[K] : undefined;
  size: WidgetSizeV1;
}
export type DashboardWidgetView<K extends string = string> = DashboardSlotView<
  DashboardWidgetViewData<K>,
  K extends keyof WidgetActionsMap ? WidgetActionsMap[K] : Record<string, never>,
  DashboardLayoutSettings
>;

/**
 * Closed filter set for dashboard task widgets (spec §3, PRD §6.3). Every field is applied on the server
 * over the projects the viewer may read, so `total` is the real number of matches.
 */
export interface TaskQuery {
  scope: "workspace" | "project" | "mine";
  /** Required for scope "project"; narrows "mine" and "workspace" to one project. */
  projectId?: Id;
  /** Status categories to keep. Omitted: every category except done and cancelled. */
  statusCategories?: StatusCategory[];
  priority?: string[];
  /** Keeps tasks due strictly before this date (YYYY-MM-DD or an ISO time); undated tasks never match. */
  dueBefore?: string;
  /** Only the triage queue: a triage status or a live human gate. */
  gated?: boolean;
  sort?: "updated" | "priority" | "due_date";
  /** 1..50, default 50. */
  limit?: number;
}
