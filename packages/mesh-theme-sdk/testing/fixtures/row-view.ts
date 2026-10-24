// Fixtures for task.row and task.view (Mesh Doc "slots-task-nav-doc-auth" §1, §2).
import type { CustomFieldDefinition, StatusData, TaskData, TaskRowColumn, TaskRowData, TaskViewBlockId, TaskViewData } from "../../src/index";
import { NOW, status, task } from "./common";

export const STATUSES: StatusData[] = [
  status("s1", { name: "Todo", slug: "todo", category: "todo", position: 0 }),
  status("s2", { name: "Doing", slug: "doing", color: "#00f", category: "in_progress", position: 1 }),
];

export const SPRINT_DEF: CustomFieldDefinition = {
  id: "f1", project_id: "p1", name: "Sprint", slug: "sprint", field_type: "number", description: "", options: {},
  default_value: null, is_required: false, is_visible_to_agents: true, position: 0, created_at: NOW,
};

const BUILTIN: Record<string, { label: string; kind: TaskRowColumn["field"]["kind"] }> = {
  title: { label: "Title", kind: "text" },
  status: { label: "Status", kind: "select" },
  priority: { label: "Priority", kind: "select" },
  assignee: { label: "Assignee", kind: "person" },
  due_date: { label: "Due date", kind: "date" },
  labels: { label: "Labels", kind: "multiselect" },
};

/** `columns` as the core builds them (title first, then the picked keys in the given order). */
export function rowColumns(keys: string[]): TaskRowColumn[] {
  return keys.map((key): TaskRowColumn => {
    if (key === "cf:sprint") {
      return { key, label: SPRINT_DEF.name, field: { key, label: SPRINT_DEF.name, kind: "number", source: "custom", projectId: "p1" }, customField: SPRINT_DEF };
    }
    const b = BUILTIN[key] ?? { label: key, kind: "text" as const };
    return { key, label: b.label, field: { key, label: b.label, kind: b.kind, source: "builtin" } };
  });
}

/** `task.row` data without the injected `components` (the mounter adds them). */
export function row(over: Partial<Omit<TaskRowData, "components">> = {}): Omit<TaskRowData, "components"> {
  return {
    task: task("t1"),
    status: STATUSES[0]!,
    columns: rowColumns(["title", "status", "priority", "due_date"]),
    selected: false,
    focused: false,
    depth: 0,
    expandable: false,
    expanded: false,
    statuses: STATUSES,
    expanding: false,
    createdAt: NOW,
    ...over,
  };
}

/** Blocks the core offers today (`vcs` is drawn inside `properties`, not offered as a block). */
export const VIEW_BLOCKS: TaskViewBlockId[] = ["header", "properties", "description", "comments", "subtasks", "dependencies", "artifacts", "activity"];

export const fullTask = (over: Partial<TaskData> = {}): TaskData & { descriptionMarkdown: string } => ({
  ...task("t1", { hasDescription: true, counts: { subtasks: 1, artifacts: 0, vcsLinks: 0 }, ...over }),
  descriptionMarkdown: "body",
});

/** `task.view` data without the injected `components` (the mounter adds them). */
export function view(over: Partial<Omit<TaskViewData, "components">> = {}): Omit<TaskViewData, "components"> {
  return {
    task: { state: "ready", value: fullTask() },
    status: STATUSES[0]!,
    statuses: STATUSES,
    presentation: "page",
    blocks: VIEW_BLOCKS,
    requestedBlock: null,
    ...over,
  };
}
