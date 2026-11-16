// Instance settings of the task widgets -> the server reads (TaskQuery, triage options). Unknown or out-of-range
// values fall back to the defaults, so a hand-edited or stale setting never breaks a widget.
import type { StatusCategory } from "@entire-vc/mesh-theme-sdk";
import type { TaskQuery } from "../../types";
import type { TriageQueueOptions } from "../../tasks/hooks";

export const STATUS_CATEGORIES: StatusCategory[] = ["backlog", "triage", "todo", "in_progress", "review", "done", "cancelled"];
export const PRIORITIES = ["urgent", "high", "medium", "low", "none"] as const;
export const SORTS = ["updated", "priority", "due_date"] as const;
export const SCOPES = ["mine", "project", "workspace"] as const;

type Settings = Readonly<Record<string, unknown>>;

const num = (s: Settings, k: string, def: number, lo: number, hi: number) => {
  const v = s[k];
  return typeof v === "number" && Number.isFinite(v) ? Math.min(hi, Math.max(lo, Math.round(v))) : def;
};
const str = (s: Settings, k: string) => (typeof s[k] === "string" && s[k] ? (s[k] as string) : undefined);
function oneOf<T extends string>(s: Settings, k: string, allowed: readonly T[], def: T): T {
  return allowed.includes(s[k] as T) ? (s[k] as T) : def;
}
function listOf<T extends string>(s: Settings, k: string, allowed: readonly T[]): T[] | undefined {
  const v = s[k];
  if (!Array.isArray(v)) return undefined;
  const out = v.filter((x): x is T => allowed.includes(x as T));
  return out.length ? out : undefined;
}

/** my-tasks: my open tasks (or the picked status categories), last changed first; limit 5..50. */
export function myTasksQuery(s: Settings = {}): TaskQuery {
  return {
    scope: "mine",
    projectId: str(s, "projectId"),
    statusCategories: listOf(s, "statuses", STATUS_CATEGORIES),
    sort: "updated",
    limit: num(s, "limit", 10, 5, 50),
  };
}

/** triage-queue: limit 1..50, one project or all, only live hard gates. */
export function triageQueueOptions(s: Settings = {}): TriageQueueOptions {
  return { limit: num(s, "limit", 10, 1, 50), projectId: str(s, "projectId"), hardGatesOnly: s.hardGatesOnly === true };
}

/** task-list: every TaskQuery field. Scope "project" without a project reads the workspace (never an empty query). */
export function taskListQuery(s: Settings = {}): TaskQuery {
  const projectId = str(s, "projectId");
  const scope = oneOf(s, "scope", SCOPES, "mine");
  return {
    scope: scope === "project" && !projectId ? "workspace" : scope,
    projectId,
    statusCategories: listOf(s, "statuses", STATUS_CATEGORIES),
    priority: listOf(s, "priority", PRIORITIES),
    dueBefore: str(s, "dueBefore"),
    gated: s.gated === true || undefined,
    sort: oneOf(s, "sort", SORTS, "updated"),
    limit: num(s, "limit", 20, 1, 50),
  };
}
