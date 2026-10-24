// Contract: Mesh Doc "slots-dashboard" (dashboard.layout, dashboard.widget.<id>).
import type { ComponentType } from "react";
import type { MentionSource, Task, TeamDirectoryAgent } from "./api";
import type { Id, Permission } from "./primitives";

export type DashboardAsync<T> =
  | { state: "loading" }
  | { state: "error"; error: Error; retry: () => void }
  | { state: "ready"; value: T };

export type WidgetSize = { w: 1 | 2 | 3 | 4; h: 1 | 2 | 3 };

export type CoreWidgetId = "mentions" | "triage" | "my-tasks" | "sessions";

export interface WidgetDescriptor {
  /** Core id, or "<theme-id>.<name>" for a theme's own widget. */
  id: string;
  title: string;
  description: string;
  origin: "core" | "extension" | "theme";
  dataHook: string;
  actions: string[];
  size: { default: WidgetSize; min: WidgetSize; max: WidgetSize };
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
}

export interface SessionItem {
  agent: TeamDirectoryAgent;
  status: string;
}

export interface MentionsData {
  items: MentionItem[];
  failedSources: MentionSource[];
}
export interface TasksData {
  items: Task[];
  total: number;
  projectNames: Record<Id, string>;
}

export interface WidgetViewDataMap {
  mentions: DashboardAsync<MentionsData>;
  triage: DashboardAsync<TasksData>;
  "my-tasks": DashboardAsync<TasksData>;
  sessions: DashboardAsync<{ items: SessionItem[] }>;
}

export interface WidgetActionsMap {
  mentions: { openMention(item: MentionItem, opts?: { newTab?: boolean }): void; retry(): void };
  triage: { openTask(taskId: Id, opts?: { newTab?: boolean }): void; retry(): void };
  "my-tasks": { openTask(taskId: Id, opts?: { newTab?: boolean }): void; retry(): void };
  sessions: { openAgent(agentId: Id): void; retry(): void };
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

export interface DashboardLayoutData {
  widgets: { descriptor: WidgetDescriptor; size: WidgetSize; hidden: false }[];
  available: WidgetDescriptor[];
  components: { Widget: ComponentType<{ id: string; size?: WidgetSize }> };
  editing: boolean;
}
export interface DashboardLayoutActions {
  openTask(taskId: Id, opts?: { newTab?: boolean }): void;
  setWidgetOrder?(ids: string[]): Promise<void>;
}
export type DashboardLayoutView = DashboardSlotView<DashboardLayoutData, DashboardLayoutActions, DashboardLayoutSettings>;

/** Data of a widget view. A theme's own widget gets no `result`: it reads SDK hooks. */
export interface DashboardWidgetViewData<K extends string = string> {
  id: K;
  descriptor: WidgetDescriptor;
  result: K extends keyof WidgetViewDataMap ? WidgetViewDataMap[K] : undefined;
  size: WidgetSize;
}
export type DashboardWidgetView<K extends string = string> = DashboardSlotView<
  DashboardWidgetViewData<K>,
  K extends keyof WidgetActionsMap ? WidgetActionsMap[K] : Record<string, never>,
  DashboardLayoutSettings
>;

/** Closed filter set for a theme's own widgets (spec §3). v1 serves scope "mine" and gated (triage). */
export interface TaskQuery {
  scope: "workspace" | "project" | "mine";
  projectId?: Id;
  priority?: string[];
  dueBefore?: string;
  gated?: boolean;
  sort?: "updated" | "priority" | "due_date";
  limit?: number;
}
