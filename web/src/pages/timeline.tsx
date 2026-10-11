import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router";
import { ArrowDown, ArrowLeft, ArrowRight, ArrowUp, GitBranch, Loader2 } from "lucide-react";
import { ApiRequestError } from "@/lib/api";
import { cn } from "@/lib/cn";
import { blockerSummary, graphPage, loadDependencyGraph, type BranchName,
  type DependencyGraph, type DependencyNode } from "@/lib/dependency-graph";
import { useProjectStore } from "@/stores/project";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { TaskSlideOver } from "@/components/task-slide-over";

const statusColor: Record<string, string> = {
  backlog: "bg-gray-400", triage: "bg-amber-500", todo: "bg-blue-500", in_progress: "bg-amber-500",
  review: "bg-violet-500", done: "bg-green-600", cancelled: "bg-gray-400",
};

function savedPosition(key: string): number | undefined {
  try { return JSON.parse(sessionStorage.getItem("dependency-positions") || "{}")[key]; }
  catch { return undefined; }
}
function savePosition(key: string, value: number) {
  try {
    const positions = JSON.parse(sessionStorage.getItem("dependency-positions") || "{}");
    delete positions[key];
    positions[key] = value;
    sessionStorage.setItem("dependency-positions", JSON.stringify(Object.fromEntries(Object.entries(positions).slice(-20))));
  } catch { /* Scrolling remains usable when browser storage is unavailable. */ }
}

function NodeCard({ node, selected, currentProjectId, focus, open, filterHidden = false }: {
  node: DependencyNode; selected?: boolean; currentProjectId: string;
  focus: (id: string) => void; open: (id: string) => void; filterHidden?: boolean;
}) {
  const available = node.availability === "available";
  return <article data-dependency-node={node.id} className={cn("min-w-0 rounded-xl border border-border bg-card p-4 text-sm shadow-sm",
    selected && "border-primary ring-1 ring-primary/20")}>
    {available ? <>
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
        <span className="inline-flex items-center gap-1.5"><span aria-hidden="true" className={cn("h-2 w-2 rounded-full", statusColor[node.status_category ?? ""])} />
          {node.status_name || node.status_category}</span><span className="break-words">{node.assignee_name || "Unassigned"}</span>
      </div>
      <button type="button" onClick={() => focus(node.id)} aria-current={selected ? "true" : undefined}
        className="min-h-11 w-full break-words text-left text-base font-semibold leading-6 hover:text-primary focus-visible:outline-2 focus-visible:outline-primary">
        {node.title}
      </button>
      <p className="mt-2 text-xs text-muted-foreground">#{node.id.slice(0, 8)} · {node.priority || "none"}</p>
      {node.project_id !== currentProjectId && <p className="mt-2 break-words text-xs">Other project: {node.project_name || node.project_slug}</p>}
      {node.cycle && <p className="mt-2 text-amber-700 dark:text-amber-400">Cycle detected</p>}
      {node.human_gate && <p className="mt-2">Human approval required</p>}
      {node.start_after && <p className="mt-2 break-words">Scheduled after {new Date(node.start_after).toLocaleString(undefined)}</p>}
      {filterHidden && <p className="mt-2 text-xs text-muted-foreground">Outside the overview search; kept to preserve the chain.</p>}
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <Button variant="ghost" size="sm" className="min-h-11 whitespace-normal" onClick={() => focus(node.id)}>
          {selected ? "In focus" : "Follow chain →"}</Button>
        <Button variant="outline" size="sm" className="min-h-11 whitespace-normal" onClick={() => open(node.id)}>Open task</Button>
      </div>
    </> : <>
      <p className="font-medium">{node.availability === "restricted" ? "Unavailable dependency" : "Missing dependency"}</p>
      <p className="mt-2 text-muted-foreground">Dependency check is incomplete. Task details cannot be established.</p>
    </>}
  </article>;
}

export function TimelinePage() {
  const currentProject = useProjectStore(s => s.currentProject);
  const [query, setQuery] = useSearchParams();
  const focus = query.get("focus") || "";
  const search = query.get("search") || "";
  const completed = query.get("completed") === "true";
  const page = graphPage(query.get("page")), before = graphPage(query.get("before")), after = graphPage(query.get("after")), relations = graphPage(query.get("relations"), 0);
  const scope = useMemo(() => ({ focus, search, completed, page, before, after, relations }), [focus, search, completed, page, before, after, relations]);
  const requestKey = JSON.stringify([currentProject?.id, scope]);
  const [snapshot, setSnapshot] = useState<{ key: string; graph: DependencyGraph } | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState("");
  const [retry, setRetry] = useState(0);
  const [panelId, setPanelId] = useState<string | null>(null);
  const [cycleState, setCycleState] = useState<{ key: string; page: number }>({ key: "", page: 0 });
  const revealSearch = query.get("reveal") === "true";
  const history = useRef<string[]>([]);
  const cache = useRef(new Map<string, DependencyGraph>());
  const root = useRef<HTMLDivElement>(null);
  const positions = useRef(new Map<string, number>());
  const positionKey = requestKey + revealSearch;
  const graph = snapshot?.key === requestKey ? snapshot.graph : cache.current.get(requestKey);
  // The members page belongs to one graph revision. It is reset while rendering (not in an effect), so a click made right after the graph appears cannot be overwritten by a late reset.
  const cycleKey = `${requestKey}|${graph?.revision}`;
  if (cycleState.key !== cycleKey) setCycleState({ key: cycleKey, page: 0 });
  const cyclePage = cycleState.key === cycleKey ? cycleState.page : 0;
  const setCyclePage = (next: number | ((n: number) => number)) =>
    setCycleState(prev => ({ key: cycleKey, page: typeof next === "function" ? next(prev.key === cycleKey ? prev.page : 0) : next }));
  useLayoutEffect(() => {
    const main = root.current?.closest("main");
    if (!main || loading || !graph) return;
    const saved = positions.current.get(positionKey) ?? savedPosition(positionKey) ?? 0;
    main.scrollTop = Number.isFinite(saved) ? saved : 0;
    const save = () => {
      positions.current.set(positionKey, main.scrollTop);
      if (positions.current.size > 20) positions.current.delete(positions.current.keys().next().value!);
      savePosition(positionKey, main.scrollTop);
    };
    main.addEventListener("scroll", save);
    return () => main.removeEventListener("scroll", save);
  }, [positionKey, graph, loading]);
  const projectId = currentProject?.id;
  const queryRef = useRef(query);
  queryRef.current = query;

  function update(values: Record<string, string | null>, replace = false) {
    const next = new URLSearchParams(queryRef.current);
    for (const [key, value] of Object.entries(values)) { if (value === null) next.delete(key); else next.set(key, value); }
    setQuery(next, { replace });
  }
  function select(id: string) {
    if (id === focus) return;
    history.current.push(query.toString());
    update({ focus: id, before: null, after: null, relations: null, reveal: null });
  }

  useEffect(() => {
    if (!projectId) return;
    let cancelled = false;
    const controller = new AbortController();
    setLoading(true); setError(null);
    const cached = cache.current.get(requestKey);
    if (cached) setSnapshot({ key: requestKey, graph: cached });
    let latest: DependencyGraph | undefined;
    loadDependencyGraph(projectId, scope, data => {
      latest = data; setSnapshot({ key: requestKey, graph: data });
    }, () => cancelled, controller.signal)
      .then(() => {
        if (!cancelled && latest) {
          cache.current.set(requestKey, latest);
          if (cache.current.size > 20) cache.current.delete(cache.current.keys().next().value!);
        }
      })
      .catch(cause => {
        if (cancelled) return;
        if (cause instanceof ApiRequestError && cause.status === 409) {
          setSnapshot(null);
          cache.current.clear();
          setNotice("Dependencies changed. Pagination restarted with the latest data.");
          const next = new URLSearchParams(queryRef.current);
          for (const key of ["page", "before", "after", "relations"]) next.delete(key);
          setQuery(next, { replace: true });
          setRetry(n => n + 1);
        } else setError(cause instanceof ApiRequestError && cause.status === 404
          ? "Selected task was deleted or is unavailable." : "Could not load dependencies. Dependency check is incomplete.");
      }).finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; controller.abort(); };
  }, [projectId, scope, requestKey, retry, setQuery]);

  const nodes = new Map(graph?.tasks.map(t => [t.id, t]) ?? []);
  const center = focus ? nodes.get(focus) : undefined;
  const cycleTitle = (id: string) => nodes.get(id)?.availability === "available" ? nodes.get(id)?.title : "Unavailable task";
  const matchesSearch = (node: DependencyNode) => !search || `${node.title || ""} ${node.id}`.toLowerCase().includes(search.toLowerCase());
  const renderNode = (node: DependencyNode, selected = false) => <NodeCard key={node.id} node={node} selected={selected}
    currentProjectId={projectId!} focus={select} open={setPanelId} filterHidden={Boolean(focus && !matchesSearch(node))} />;

  function branch(name: BranchName, title: string) {
    if (!graph) return null;
    const data = graph[name];
    const list = data.ids.map(id => nodes.get(id)).filter((n): n is DependencyNode => Boolean(n));
    const hidden = list.filter(n => n.availability === "available" && !matchesSearch(n));
    const visible = revealSearch ? list : list.filter(n => n.availability !== "available" || matchesSearch(n));
    return <section aria-label={title} className="min-w-0 space-y-3">
      <h2 className="text-sm font-semibold">{title} <span className="text-muted-foreground">({data.total_count})</span></h2>
      {visible.map(n => <div key={n.id}>{name === "relations" && <p className="mb-1 text-xs text-muted-foreground">{
        center?.parent_task_id === n.id ? "Parent task" : n.parent_task_id === focus ? "Subtask" :
          graph!.dependencies.some(e => e.dependency_type === "is_child_of" && e.task_id === focus && e.depends_on_task_id === n.id)
            ? "Parent task" : graph!.dependencies.some(e => e.dependency_type === "is_child_of" && e.task_id === n.id && e.depends_on_task_id === focus)
              ? "Subtask" : "Related task"}</p>}{renderNode(n)}</div>)}
      {hidden.length > 0 && !revealSearch && <Button variant="outline" className="min-h-11 w-full whitespace-normal" onClick={() => update({ reveal: "true" })}>
        {hidden.length} hidden by search · reveal</Button>}
      {!completed && name !== "relations" && data.completed_count > 0 && <Button variant="outline" className="min-h-11 w-full whitespace-normal" onClick={() => update({ completed: "true", before: null, after: null, relations: null })}>
        ✓ {data.completed_count} completed · expand original links</Button>}
      {data.total_count === 0 && <p className="rounded-lg border border-border border-dashed p-4 text-sm text-muted-foreground">No direct {name === "before" ? "blockers" : name === "after" ? "dependents" : "relations"}</p>}
      {data.unknown_count > 0 && <p className="text-sm text-amber-700 dark:text-amber-400">{data.unknown_count} unavailable or missing</p>}
      {(data.has_more || (name === "relations" && scope.relations === 0 && data.total_count > 0)) && <Button variant="outline"
        disabled={loading} className="min-h-11 w-full whitespace-normal" onClick={() => update({ [name]: String((name === "relations" ? scope.relations : data.page) + 1) })}>
        Show more {name === "relations" ? "relations" : name === "before" ? "blockers" : "dependents"} · {Math.max(0, data.total_count - list.length - (!completed ? data.completed_count : 0))} remaining</Button>}
    </section>;
  }

  const groups = new Map<string, DependencyNode[]>();
  if (!focus && graph) for (const node of graph.tasks.slice((graph.page - 1) * graph.page_size)) {
    const key = node.parent_task_id || "";
    groups.set(key, [...(groups.get(key) ?? []), node]);
  }

  if (!currentProject) return <p className="py-12 text-center text-muted-foreground">Project not found</p>;
  return <div ref={root} className="min-w-0 space-y-5" data-dependencies-view>
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div><h1 className="flex items-center gap-2 text-xl font-semibold"><GitBranch className="h-5 w-5" /> Dependencies</h1>
        <p className="mt-1 text-sm text-muted-foreground">Follow one chain at a time. Blocker → task → dependent.</p></div>
      <Button variant="outline" disabled={loading} onClick={() => { setNotice(""); setRetry(n => n + 1); }}>Refresh</Button>
    </div>
    <div className="flex flex-wrap items-center gap-3 rounded-xl border border-border bg-card p-3">
      <Button variant={focus ? "outline" : "secondary"} onClick={() => { history.current.push(query.toString()); update({ focus: null, before: null, after: null, relations: null }); }}>Overview</Button>
      {focus && <span className="text-sm font-medium">Task chain</span>}
      <label className="min-w-0 flex-1 basis-48 text-sm">Search title or ID<Input value={search} className="mt-1 w-full"
        placeholder="Find a task" onChange={e => update({ search: e.target.value || null, page: null, reveal: null }, true)} /></label>
      <label className="flex min-h-11 items-center gap-2 text-sm"><input type="checkbox" checked={completed}
        onChange={e => update({ completed: e.target.checked ? "true" : null, page: null, before: null, after: null, relations: null })} /> Show completed</label>
    </div>
    {notice && <p role="status" className="rounded-lg border border-border p-3 text-sm">{notice}</p>}
    {error && <div role="alert" className="rounded-lg border border-destructive p-4 text-sm"><p>{error}</p>
      <Button variant="outline" className="mt-3" onClick={() => setRetry(n => n + 1)}>Retry</Button></div>}
    {loading && <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground"><Loader2 className="h-4 w-4 animate-spin" /> Loading dependencies…</p>}
    {graph && <>
      <p className="text-sm text-muted-foreground" aria-live="polite">{focus ? `${graph.total_count} unique tasks in this neighborhood` : `${graph.tasks.length} of ${graph.total_count} matching tasks loaded · Page ${graph.page}`} · {graph.completed_count} completed
        {!graph.visibility_complete && " · Visibility incomplete"}{loading && " · Loading continuation"}{error && " · Partial data"}</p>
      {graph.cycle_count > 0 && <div role="status" className="space-y-2 rounded-lg border border-amber-500 p-3 text-sm">
        <p>Cycle detected · {graph.cycle_count} tasks in the cycle. Blocking order cannot be established.</p>
        <p className="text-muted-foreground">Available cycle tasks in this view:</p>
        {graph.cycle_ids.map(id => nodes.get(id)).filter((n): n is DependencyNode => Boolean(n && n.availability === "available")).map(n =>
          <Button key={n.id} variant="ghost" className="min-h-11 max-w-full whitespace-normal break-words" onClick={() => select(n.id)}>{n.title}</Button>)}
        {graph.dependencies.filter(e => e.dependency_type === "blocks" && graph.cycle_ids.includes(e.task_id) && graph.cycle_ids.includes(e.depends_on_task_id)).map(e =>
          <p key={e.id} className="break-words text-xs">{cycleTitle(e.depends_on_task_id)} {e.task_id === e.depends_on_task_id ? "blocks itself (self-loop)" : `blocks ${cycleTitle(e.task_id)}`}</p>)}
        <p className="text-xs text-muted-foreground">Edges shown for loaded direct neighbors. Inspect a member to continue the cycle.</p>
        {cyclePage === 0 ? <Button variant="outline" onClick={() => setCyclePage(1)}>Inspect cycle members</Button> : <>
          <ul className="space-y-2" aria-label="Cycle members">
            {graph.cycle_ids.slice((cyclePage - 1) * 10, cyclePage * 10).map(id => {
              const node = nodes.get(id);
              return <li key={id} className="break-words">{node && node.availability !== "available" ? "Unavailable dependency" :
                <Button variant="ghost" className="min-h-11 max-w-full whitespace-normal break-all" onClick={() => select(id)}>
                  {node?.title || `Inspect task #${id}`}</Button>}</li>;
            })}
          </ul>
          <div className="flex flex-wrap items-center gap-2">
            {cyclePage > 1 && <Button variant="outline" onClick={() => setCyclePage(n => n - 1)}>Previous members</Button>}
            {cyclePage * 10 < graph.cycle_ids.length && <Button variant="outline" onClick={() => setCyclePage(n => n + 1)}>Next 10 members</Button>}
            <Button variant="ghost" onClick={() => setCyclePage(0)}>Hide members</Button>
          </div>
        </>}
      </div>}
      {focus && center ? <>
        <div className="grid min-w-0 grid-cols-1 items-start gap-4 lg:grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)_auto_minmax(0,1fr)]">
          <div className="order-3 min-w-0 lg:order-1">{branch("before", "What blocks this task")}</div>
          <div aria-hidden="true" className="order-2 flex items-center justify-center gap-2 pt-3 text-xs text-muted-foreground"><ArrowRight className="hidden lg:block" /><ArrowUp className="lg:hidden" /><span className="lg:hidden">Blockers → selected task</span></div>
          <section className="order-1 min-w-0 space-y-3 lg:order-3" aria-label="Selected task"><h2 className="text-sm font-semibold">Selected task</h2>{renderNode(center, true)}
            <p className="text-sm" aria-live="polite">{loading || error ? "Dependency check is incomplete" : blockerSummary(center, graph)}</p>
            <p className="text-xs text-muted-foreground">Other workflow and scheduling limits still apply.</p></section>
          <div aria-hidden="true" className="order-4 flex items-center justify-center gap-2 pt-3 text-xs text-muted-foreground"><ArrowRight className="hidden lg:block" /><ArrowDown className="lg:hidden" /><span className="lg:hidden">Selected task → dependents</span></div>
          <div className="order-5 min-w-0">{branch("after", "What waits for this task")}</div>
        </div>
        <div className="border-t border-border pt-4">{branch("relations", "Parent, subtasks and related tasks")}</div>
      </> : !focus ? <>
        {[...groups].map(([parent, list]) => <section key={parent} aria-label={parent ? `Parent ${parent}` : "Without an epic"} className="space-y-3">
          <h2 className="break-words text-sm font-semibold">{parent ? `Parent: ${nodes.get(parent)?.title || `#${parent.slice(0, 8)}`}` : "Without an epic"}
            <span className="ml-2 text-muted-foreground">{list.length} unique loaded tasks</span></h2>
          <div className="grid min-w-0 grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">{list.map(node => <div key={node.id} className="min-w-0">
            {renderNode(node)}<p className="mt-2 text-xs text-muted-foreground">{node.unfinished_blockers} unfinished direct blockers · {node.unknown_blockers} unknown · {node.dependents} direct dependents</p>
          </div>)}</div>
        </section>)}
        {graph.tasks.length === 0 && <p className="rounded-xl border border-border border-dashed p-8 text-center">{search ? "No matching tasks" : "No open tasks"}</p>}
        {!completed && graph.completed_count > 0 && <Button variant="outline" className="min-h-11 whitespace-normal" onClick={() => update({ completed: "true", page: null })}>✓ {graph.completed_count} completed · expand</Button>}
        {graph.page > 1 && <Button variant="outline" disabled={loading} onClick={() => update({ page: String(graph.page - 1) })}>Previous page</Button>}
        {graph.has_more && <Button variant="outline" disabled={loading} onClick={() => update({ page: String(graph.page + 1) })}>Load next 50 · {Math.max(0, graph.total_count - graph.tasks.length)} remaining</Button>}
      </> : null}
    </>}
    <Button variant="ghost" disabled={history.current.length === 0} onClick={() => {
      const previous = history.current.pop(); if (previous !== undefined) setQuery(new URLSearchParams(previous));
    }}><ArrowLeft className="mr-2 h-4 w-4" /> Previous focus</Button>
    {panelId && <TaskSlideOver taskId={panelId} onClose={() => setPanelId(null)} onTaskUpdated={() => setRetry(n => n + 1)} />}
  </div>;
}
