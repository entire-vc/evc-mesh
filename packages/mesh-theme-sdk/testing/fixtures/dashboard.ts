import type {
  DashboardLayoutData,
  DashboardWidgetViewData,
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

/** Core registry v1, in its default order (Mesh Doc slots-dashboard §2). */
export const CORE_WIDGET_IDS = ["mentions", "triage", "my-tasks", "sessions"] as const;
export type CoreWidget = (typeof CORE_WIDGET_IDS)[number];

const SIZE = { default: { w: 2, h: 2 } as WidgetSize, min: { w: 1, h: 1 } as WidgetSize, max: { w: 4, h: 3 } as WidgetSize };

const ACTIONS: Record<string, string[]> = {
  mentions: ["openMention", "retry"],
  triage: ["openTask", "retry"],
  "my-tasks": ["openTask", "retry"],
  sessions: ["openAgent", "retry"],
};

export function descriptor(id: string, over: Partial<WidgetDescriptor> = {}): WidgetDescriptor {
  const origin: WidgetDescriptor["origin"] = (CORE_WIDGET_IDS as readonly string[]).includes(id) ? "core" : "theme";
  return {
    id, title: `Widget ${id}`, description: `Description of ${id}`, origin, dataHook: "useWidgetData",
    actions: ACTIONS[id] ?? [], size: SIZE, ...over,
  };
}

/** `dashboard.layout` data without the injected `components` (the harness adds the stand-in Widget). */
export function dashboard(opts: { widgets?: string[] | WidgetDescriptor[] } = {}): Omit<DashboardLayoutData, "components"> {
  const list = (opts.widgets ?? [...CORE_WIDGET_IDS]).map((w) => (typeof w === "string" ? descriptor(w) : w));
  return {
    widgets: list.map((d) => ({ descriptor: d, size: d.size.default, hidden: false as const })),
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
  };
  return (r as unknown as Record<string, WidgetData["result"]>)[id];
}

/** Data of `dashboard.widget.<id>`. */
export function widget(id: string, over: Partial<WidgetData> = {}): WidgetData {
  const d = descriptor(id);
  return { id, descriptor: d, result: widgetResult(id), size: d.size.default, ...over };
}
