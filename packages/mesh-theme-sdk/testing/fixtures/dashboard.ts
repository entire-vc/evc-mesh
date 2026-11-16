import { descriptorToV1 } from "../../src/dashboard";
import type {
  DashboardLayoutData,
  DashboardLayoutDataV1,
  DashboardWidgetViewData,
  CommentItem,
  WidgetDescriptorV1,
  WidgetInstance,
  Id,
  MentionItem,
  SessionItem,
  Task,
  TeamDirectoryAgent,
  WidgetDescriptor,
  WidgetSize,
  WidgetViewDataMap,
} from "../../src/index";
import { NOW } from "./common";

/** Core registry v1, in its default order (Mesh Doc slots-dashboard §2), then the task and comment groups (PRD §5.8). */
export const CORE_WIDGET_IDS = ["mentions", "triage", "my-tasks", "sessions", "triage-queue", "task-list", "my-comments", "recent-comments"] as const;
export type CoreWidget = (typeof CORE_WIDGET_IDS)[number];

// dashboard.layout 2: cells of a 12-column grid. The four core widgets are 6 wide (half the dashboard), 1..3 rows.
const SIZE = { default: { w: 6, h: 2 } as WidgetSize, min: { w: 3, h: 1 } as WidgetSize, max: { w: 12, h: 3 } as WidgetSize };

const ACTIONS: Record<string, string[]> = {
  mentions: ["openMention", "retry"],
  triage: ["openTask", "retry"],
  "my-tasks": ["openTask", "quickEdit", "retry"],
  sessions: ["openAgent", "retry"],
  "triage-queue": ["openTask", "moveTask", "retry"],
  "task-list": ["openTask", "quickEdit", "retry"],
  "my-comments": ["openComment", "loadMore", "retry"],
  "recent-comments": ["openComment", "loadMore", "retry"],
};

export function descriptor(id: string, over: Partial<WidgetDescriptor> = {}): WidgetDescriptor {
  const origin: WidgetDescriptor["origin"] = (CORE_WIDGET_IDS as readonly string[]).includes(id) ? "core" : "theme";
  return {
    id, title: `Widget ${id}`, description: `Description of ${id}`, category: "test", origin, scope: "user",
    actions: ACTIONS[id] ?? [], size: SIZE, ...over,
  };
}

/** The v1 form of a descriptor (the `dashboard.widget` slot and `dashboard.layout` 1.x still speak it). */
export const descriptorV1 = (id: string, over: Partial<WidgetDescriptor> = {}): WidgetDescriptorV1 => descriptorToV1({ ...descriptor(id, over), dataHook: "useWidgetData" });

/** Two widgets per row, as the core's default placement does it: `y` grows by the row height. */
export function instances(list: readonly WidgetDescriptor[]): WidgetInstance[] {
  return list.map((d, i) => ({ id: d.id, type: d.id, x: (i % 2) * 6, y: Math.floor(i / 2) * d.size.default.h, w: d.size.default.w, h: d.size.default.h, settings: {} }));
}

/** `dashboard.layout` 2 data without the injected `components` (the harness adds the stand-in Widget). */
export function dashboard(opts: { widgets?: string[] | WidgetDescriptor[]; editable?: boolean } = {}): Omit<DashboardLayoutData, "components"> {
  const list = (opts.widgets ?? [...CORE_WIDGET_IDS]).map((w) => (typeof w === "string" ? descriptor(w) : w));
  const inst = instances(list);
  return {
    columns: 12,
    widgets: list.map((d, i) => ({ descriptor: d, instance: inst[i]! })),
    available: [],
    editing: false,
    canEdit: opts.editable ?? false,
  };
}

/** `dashboard.layout` 1.x data (a theme that declared `^1`). */
export function dashboardV1(opts: { widgets?: string[] | WidgetDescriptor[] } = {}): Omit<DashboardLayoutDataV1, "components"> {
  const list = (opts.widgets ?? [...CORE_WIDGET_IDS]).map((w) => (typeof w === "string" ? descriptor(w) : w));
  return {
    widgets: list.map((d) => ({ descriptor: descriptorToV1({ ...d, dataHook: "useWidgetData" }), size: descriptorToV1(d).size.default, hidden: false as const })),
    available: [],
    editing: false,
  };
}

/** A task as the API returns it (dashboard widgets get `Task`, not the board's `TaskData`); empty fields by default. */
export function apiTask(id: Id, over: Partial<Task> = {}): Task {
  return {
    id, project_id: "p1", status_id: "s1", title: `Task ${id}`, has_description: false, assignee_id: null,
    assignee_type: "unassigned", priority: "none", parent_task_id: null, position: 0, due_date: null,
    estimated_hours: null, custom_fields: null, labels: null, created_by: "u1", created_by_type: "user",
    created_at: NOW, updated_at: NOW, completed_at: null, human_gate: false, ...over,
  };
}

export function mention(id: Id, source: MentionItem["source"], over: Partial<MentionItem> = {}): MentionItem {
  const target: MentionItem["target"] =
    source === "task" ? { kind: "task", taskId: `task-of-${id}`, commentId: id } : { kind: "document", docId: `doc-of-${id}`, projectSlug: "proj" };
  return {
    id, source, title: `Mention ${id}`, at: NOW, seen: false,
    href: source === "task" ? `/t/task-of-${id}?comment=${id}` : `/w/ws/p/proj/docs/doc-of-${id}`, target, ...over,
  };
}

export function comment(id: Id, over: Partial<CommentItem> = {}): CommentItem {
  return {
    id, taskId: `task-of-${id}`, title: `Commented task ${id}`, projectId: "p1", projectName: "Project", body: `Comment ${id}`,
    authorId: "u1", authorName: "Author", authorKind: "user", internal: false, at: NOW, href: `/t/task-of-${id}?comment=${id}`, ...over,
  };
}

export function agent(id: Id, over: Partial<TeamDirectoryAgent> = {}): TeamDirectoryAgent {
  return {
    id, name: `Agent ${id}`, slug: id, status: "online", agent_type: "claude_code", role: "developer", capabilities: [],
    responsibility_zone: "", escalation_to: null, accepts_from: [], max_concurrent_tasks: 1, working_hours: "",
    profile_description: "", current_tasks: 0, projects: [], ...over,
  };
}

/** Data of any `dashboard.widget.<id>`: `result` typed by the core ids, undefined for a theme's own widget. */
export type WidgetData = Omit<DashboardWidgetViewData, "result"> & { result: WidgetViewDataMap[CoreWidget] | undefined };

export const session = (a: TeamDirectoryAgent, status = "online"): SessionItem => ({ agent: a, status });

/** Ready result of a core widget with two rows (mentions: one per source). Unknown id = a theme widget: no result. */
export function widgetResult(id: string): WidgetData["result"] {
  const r: WidgetViewDataMap = {
    mentions: { state: "ready", value: { items: [mention("c1", "task"), mention("c2", "document", { seen: true })], failedSources: [] } },
    triage: { state: "ready", value: { items: [apiTask("t1", { human_gate: true }), apiTask("t2", { human_gate: true })], total: 2, projectNames: { p1: "Project" } } },
    "my-tasks": { state: "ready", value: { items: [apiTask("t1"), apiTask("t2")], total: 2, projectNames: { p1: "Project" } } },
    sessions: { state: "ready", value: { items: [session(agent("a1")), session(agent("a2"), "busy")] } },
    "triage-queue": { state: "ready", value: { items: [apiTask("t1", { human_gate: true, priority: "high" }), apiTask("t2", { human_gate: true })], total: 7, projectNames: { p1: "Project" } } },
    "task-list": { state: "ready", value: { items: [apiTask("t1", { due_date: "2026-11-20" }), apiTask("t2")], total: 12, projectNames: { p1: "Project" } } },
    "my-comments": { state: "ready", value: { items: [comment("m1"), comment("m2")], hasMore: false, loadingMore: false } },
    "recent-comments": { state: "ready", value: { items: [comment("r1"), comment("r2")], hasMore: false, loadingMore: false } },
  };
  return (r as unknown as Record<string, WidgetData["result"]>)[id];
}

/** Data of `dashboard.widget.<id>`. */
export function widget(id: string, over: Partial<WidgetData> = {}): WidgetData {
  const d = descriptorV1(id);
  return { id, descriptor: d, result: widgetResult(id), size: d.size.default, ...over };
}
