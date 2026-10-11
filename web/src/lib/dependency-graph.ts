import { api, ApiRequestError } from "@/lib/api";
import type { Priority, StatusCategory } from "@/types";

export interface DependencyNode {
  id: string;
  availability: "available" | "restricted" | "missing";
  project_id?: string;
  project_name?: string;
  project_slug?: string;
  title?: string;
  status_id?: string;
  status_name?: string;
  status_category?: StatusCategory;
  priority?: Priority;
  assignee_name?: string;
  parent_task_id?: string;
  human_gate?: boolean;
  start_after?: string;
  blockers: number;
  unfinished_blockers: number;
  unknown_blockers: number;
  dependents: number;
  cycle?: boolean;
}
export interface DependencyBranch {
  ids: string[];
  total_count: number;
  completed_count: number;
  unknown_count: number;
  page: number;
  has_more: boolean;
}
export type BranchName = "before" | "after" | "relations";
export interface DependencyGraph {
  mode: string;
  focus?: string;
  tasks: DependencyNode[];
  dependencies: { id: string; task_id: string; depends_on_task_id: string; dependency_type: string }[];
  before: DependencyBranch;
  after: DependencyBranch;
  relations: DependencyBranch;
  total_count: number;
  scope_total_count: number;
  completed_count: number;
  unknown_count: number;
  page: number;
  page_size: number;
  has_more: boolean;
  complete: boolean;
  visibility_complete: boolean;
  revision: string;
  cycle_ids: string[];
  cycle_count: number;
}

export function blockerSummary(node: DependencyNode, graph?: DependencyGraph): string {
  if (node.availability !== "available" || node.unknown_blockers > 0) return "There is an unavailable dependency";
  if (node.unfinished_blockers > 0) return `${node.unfinished_blockers} unfinished direct blockers`;
  if (!graph || !graph.visibility_complete || graph.before.has_more || graph.before.unknown_count > 0 ||
      graph.before.ids.length < graph.before.total_count) return "Dependency check is incomplete";
  return "No unfinished direct blockers";
}
export function mergeGraphPages(previous: DependencyGraph, next: DependencyGraph): DependencyGraph {
  if (previous.revision !== next.revision) throw new ApiRequestError("Graph revision changed", "CONFLICT", 409);
  const branch = (name: BranchName): DependencyBranch => ({ ...next[name], ids: [...new Set([...previous[name].ids, ...next[name].ids])] });
  return { ...next, tasks: [...new Map([...previous.tasks, ...next.tasks].map(t => [t.id, t])).values()],
    dependencies: [...new Map([...previous.dependencies, ...next.dependencies].map(e => [e.id, e])).values()],
    before: branch("before"), after: branch("after"), relations: branch("relations") };
}
export function graphPage(value: string | null, fallback = 1): number {
  const n = Number(value);
  return Number.isInteger(n) && n > 0 && n <= 1_000_000 ? n : fallback;
}
export interface GraphScope {
  focus: string; search: string; completed: boolean; page: number; before: number; after: number; relations: number;
}

// Deep links replay the requested pages under one revision. Stale scopes stop
// issuing continuation reads; changed revisions are restarted by the view.
export async function loadDependencyGraph(projectId: string, scope: GraphScope,
  receive: (graph: DependencyGraph) => void, cancelled: () => boolean, signal?: AbortSignal): Promise<void> {
  const path = `/api/v1/projects/${projectId}/dependency-graph`;
  let graph: DependencyGraph | undefined;
  let page = 1;
  const pages = { before: 1, after: 1, relations: scope.relations > 0 ? 1 : 0 };
  while (!cancelled()) {
    const result = await api<DependencyGraph>(path, { signal, params: scope.focus ? {
      focus: scope.focus, before_page: pages.before, after_page: pages.after,
      relations_page: pages.relations || undefined, completed: String(scope.completed), revision: graph?.revision,
    } : { mode: "overview", page, page_size: 50, search: scope.search || undefined,
      completed: String(scope.completed), revision: graph?.revision } });
    if (cancelled()) return;
    graph = graph ? mergeGraphPages(graph, result) : result;
    receive(graph);
    if (!scope.focus) {
      if (page >= scope.page || !result.has_more) return;
      page++;
    } else {
      let more = false;
      for (const name of ["before", "after", "relations"] as const) {
        if (pages[name] < scope[name] && result[name].has_more) { pages[name]++; more = true; }
      }
      if (!more) return;
    }
  }
}
