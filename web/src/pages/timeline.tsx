import * as i18nFmt from "@/i18n/format";
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router";
import { useTranslation } from "react-i18next";
import i18n from "@/i18n";
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
  const { t } = useTranslation("settings");
  const available = node.availability === "available";
  return <article data-dependency-node={node.id} className={cn("min-w-0 rounded-xl border border-border bg-card p-4 text-sm shadow-sm",
    selected && "border-primary ring-1 ring-primary/20")}>
    {available ? <>
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
        <span className="inline-flex items-center gap-1.5"><span aria-hidden="true" className={cn("h-2 w-2 rounded-full", statusColor[node.status_category ?? ""])} />
          {node.status_name || node.status_category}</span><span className="break-words">{node.assignee_name || t("timeline.node.unassigned")}</span>
      </div>
      <button type="button" onClick={() => focus(node.id)} aria-current={selected ? "true" : undefined}
        className="min-h-11 w-full break-words text-left text-base font-semibold leading-6 hover:text-primary focus-visible:outline-2 focus-visible:outline-primary">
        {node.title}
      </button>
      <p className="mt-2 text-xs text-muted-foreground">#{node.id.slice(0, 8)} · {t(`timeline.priority.${node.priority || "none"}`, { defaultValue: node.priority || "none" })}</p>
      {node.project_id !== currentProjectId && <p className="mt-2 break-words text-xs">{t("timeline.node.otherProject", { name: node.project_name || node.project_slug })}</p>}
      {node.cycle && <p className="mt-2 text-amber-700 dark:text-amber-400">{t("timeline.node.cycleDetected")}</p>}
      {node.human_gate && <p className="mt-2">{t("timeline.node.humanApproval")}</p>}
      {node.start_after && <p className="mt-2 break-words">{t("timeline.node.scheduledAfter", { time: i18nFmt.formatDateTime(node.start_after) })}</p>}
      {filterHidden && <p className="mt-2 text-xs text-muted-foreground">{t("timeline.node.filterHidden")}</p>}
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <Button variant="ghost" size="sm" className="min-h-11 whitespace-normal" onClick={() => focus(node.id)}>
          {selected ? t("timeline.node.inFocus") : t("timeline.node.followChain")}</Button>
        <Button variant="outline" size="sm" className="min-h-11 whitespace-normal" onClick={() => open(node.id)}>{t("timeline.node.openTask")}</Button>
      </div>
    </> : <>
      <p className="font-medium">{node.availability === "restricted" ? t("timeline.node.unavailableDependency") : t("timeline.node.missingDependency")}</p>
      <p className="mt-2 text-muted-foreground">{t("timeline.node.checkIncomplete")}</p>
    </>}
  </article>;
}

export function TimelinePage() {
  const { t } = useTranslation("settings");
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
          setNotice(i18n.t("settings:timeline.notice.changed"));
          const next = new URLSearchParams(queryRef.current);
          for (const key of ["page", "before", "after", "relations"]) next.delete(key);
          setQuery(next, { replace: true });
          setRetry(n => n + 1);
        } else setError(cause instanceof ApiRequestError && cause.status === 404
          ? i18n.t("settings:timeline.error.deleted") : i18n.t("settings:timeline.error.load"));
      }).finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; controller.abort(); };
  }, [projectId, scope, requestKey, retry, setQuery]);

  const nodes = new Map(graph?.tasks.map(t => [t.id, t]) ?? []);
  const center = focus ? nodes.get(focus) : undefined;
  const cycleTitle = (id: string) => nodes.get(id)?.availability === "available" ? nodes.get(id)?.title : t("timeline.cycle.unavailableTask");
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
        center?.parent_task_id === n.id ? t("timeline.relation.parent") : n.parent_task_id === focus ? t("timeline.relation.subtask") :
          graph!.dependencies.some(e => e.dependency_type === "is_child_of" && e.task_id === focus && e.depends_on_task_id === n.id)
            ? t("timeline.relation.parent") : graph!.dependencies.some(e => e.dependency_type === "is_child_of" && e.task_id === n.id && e.depends_on_task_id === focus)
              ? t("timeline.relation.subtask") : t("timeline.relation.related")}</p>}{renderNode(n)}</div>)}
      {hidden.length > 0 && !revealSearch && <Button variant="outline" className="min-h-11 w-full whitespace-normal" onClick={() => update({ reveal: "true" })}>
        {t("timeline.branch.hidden", { n: hidden.length })}</Button>}
      {!completed && name !== "relations" && data.completed_count > 0 && <Button variant="outline" className="min-h-11 w-full whitespace-normal" onClick={() => update({ completed: "true", before: null, after: null, relations: null })}>
        {t("timeline.branch.expandOriginal", { n: data.completed_count })}</Button>}
      {data.total_count === 0 && <p className="rounded-lg border border-border border-dashed p-4 text-sm text-muted-foreground">{t(`timeline.branch.noDirect.${name}`)}</p>}
      {data.unknown_count > 0 && <p className="text-sm text-amber-700 dark:text-amber-400">{t("timeline.branch.unknown", { n: data.unknown_count })}</p>}
      {(data.has_more || (name === "relations" && scope.relations === 0 && data.total_count > 0)) && <Button variant="outline"
        disabled={loading} className="min-h-11 w-full whitespace-normal" onClick={() => update({ [name]: String((name === "relations" ? scope.relations : data.page) + 1) })}>
        {t(`timeline.branch.showMore.${name}`, { n: Math.max(0, data.total_count - list.length - (!completed ? data.completed_count : 0)) })}</Button>}
    </section>;
  }

  const groups = new Map<string, DependencyNode[]>();
  if (!focus && graph) for (const node of graph.tasks.slice((graph.page - 1) * graph.page_size)) {
    const key = node.parent_task_id || "";
    groups.set(key, [...(groups.get(key) ?? []), node]);
  }

  if (!currentProject) return <p className="py-12 text-center text-muted-foreground">{t("timeline.page.notFound")}</p>;
  return <div ref={root} className="min-w-0 space-y-5" data-dependencies-view>
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div><h1 className="flex items-center gap-2 text-xl font-semibold"><GitBranch className="h-5 w-5" /> {t("timeline.page.heading")}</h1>
        <p className="mt-1 text-sm text-muted-foreground">{t("timeline.page.subtitle")}</p></div>
      <Button variant="outline" disabled={loading} onClick={() => { setNotice(""); setRetry(n => n + 1); }}>{t("timeline.page.refresh")}</Button>
    </div>
    <div className="flex flex-wrap items-center gap-3 rounded-xl border border-border bg-card p-3">
      <Button variant={focus ? "outline" : "secondary"} onClick={() => { history.current.push(query.toString()); update({ focus: null, before: null, after: null, relations: null }); }}>{t("timeline.page.overview")}</Button>
      {focus && <span className="text-sm font-medium">{t("timeline.page.taskChain")}</span>}
      <label className="min-w-0 flex-1 basis-48 text-sm">{t("timeline.page.searchLabel")}<Input value={search} className="mt-1 w-full"
        placeholder={t("timeline.page.searchPlaceholder")} onChange={e => update({ search: e.target.value || null, page: null, reveal: null }, true)} /></label>
      <label className="flex min-h-11 items-center gap-2 text-sm"><input type="checkbox" checked={completed}
        onChange={e => update({ completed: e.target.checked ? "true" : null, page: null, before: null, after: null, relations: null })} /> {t("timeline.page.showCompleted")}</label>
    </div>
    {notice && <p role="status" className="rounded-lg border border-border p-3 text-sm">{notice}</p>}
    {error && <div role="alert" className="rounded-lg border border-destructive p-4 text-sm"><p>{error}</p>
      <Button variant="outline" className="mt-3" onClick={() => setRetry(n => n + 1)}>{t("timeline.page.retry")}</Button></div>}
    {loading && <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground"><Loader2 className="h-4 w-4 animate-spin" /> {t("timeline.page.loading")}</p>}
    {graph && <>
      <p className="text-sm text-muted-foreground" aria-live="polite">{focus ? t("timeline.summary.neighborhood", { total: graph.total_count }) : t("timeline.summary.overview", { loaded: graph.tasks.length, total: graph.total_count, page: graph.page })} · {t("timeline.summary.completed", { n: graph.completed_count })}
        {!graph.visibility_complete && t("timeline.summary.visibilityIncomplete")}{loading && t("timeline.summary.loadingContinuation")}{error && t("timeline.summary.partial")}</p>
      {graph.cycle_count > 0 && <div role="status" className="space-y-2 rounded-lg border border-amber-500 p-3 text-sm">
        <p>{t("timeline.cycle.detected", { n: graph.cycle_count })}</p>
        <p className="text-muted-foreground">{t("timeline.cycle.available")}</p>
        {graph.cycle_ids.map(id => nodes.get(id)).filter((n): n is DependencyNode => Boolean(n && n.availability === "available")).map(n =>
          <Button key={n.id} variant="ghost" className="min-h-11 max-w-full whitespace-normal break-words" onClick={() => select(n.id)}>{n.title}</Button>)}
        {graph.dependencies.filter(e => e.dependency_type === "blocks" && graph.cycle_ids.includes(e.task_id) && graph.cycle_ids.includes(e.depends_on_task_id)).map(e =>
          <p key={e.id} className="break-words text-xs">{cycleTitle(e.depends_on_task_id)} {e.task_id === e.depends_on_task_id ? t("timeline.cycle.selfLoop") : t("timeline.cycle.blocks", { title: cycleTitle(e.task_id) })}</p>)}
        <p className="text-xs text-muted-foreground">{t("timeline.cycle.edgesNote")}</p>
        {cyclePage === 0 ? <Button variant="outline" onClick={() => setCyclePage(1)}>{t("timeline.cycle.inspectMembers")}</Button> : <>
          <ul className="space-y-2" aria-label={t("timeline.cycle.membersLabel")}>
            {graph.cycle_ids.slice((cyclePage - 1) * 10, cyclePage * 10).map(id => {
              const node = nodes.get(id);
              return <li key={id} className="break-words">{node && node.availability !== "available" ? t("timeline.node.unavailableDependency") :
                <Button variant="ghost" className="min-h-11 max-w-full whitespace-normal break-all" onClick={() => select(id)}>
                  {node?.title || t("timeline.cycle.inspectTask", { id })}</Button>}</li>;
            })}
          </ul>
          <div className="flex flex-wrap items-center gap-2">
            {cyclePage > 1 && <Button variant="outline" onClick={() => setCyclePage(n => n - 1)}>{t("timeline.cycle.previousMembers")}</Button>}
            {cyclePage * 10 < graph.cycle_ids.length && <Button variant="outline" onClick={() => setCyclePage(n => n + 1)}>{t("timeline.cycle.nextMembers")}</Button>}
            <Button variant="ghost" onClick={() => setCyclePage(0)}>{t("timeline.cycle.hideMembers")}</Button>
          </div>
        </>}
      </div>}
      {focus && center ? <>
        <div className="grid min-w-0 grid-cols-1 items-start gap-4 lg:grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)_auto_minmax(0,1fr)]">
          <div className="order-3 min-w-0 lg:order-1">{branch("before", t("timeline.layout.before"))}</div>
          <div aria-hidden="true" className="order-2 flex items-center justify-center gap-2 pt-3 text-xs text-muted-foreground"><ArrowRight className="hidden lg:block" /><ArrowUp className="lg:hidden" /><span className="lg:hidden">{t("timeline.layout.blockersToSelected")}</span></div>
          <section className="order-1 min-w-0 space-y-3 lg:order-3" aria-label={t("timeline.layout.selectedTask")}><h2 className="text-sm font-semibold">{t("timeline.layout.selectedTask")}</h2>{renderNode(center, true)}
            <p className="text-sm" aria-live="polite">{loading || error ? t("timeline.layout.checkIncomplete") : blockerSummary(center, graph)}</p>
            <p className="text-xs text-muted-foreground">{t("timeline.layout.otherLimits")}</p></section>
          <div aria-hidden="true" className="order-4 flex items-center justify-center gap-2 pt-3 text-xs text-muted-foreground"><ArrowRight className="hidden lg:block" /><ArrowDown className="lg:hidden" /><span className="lg:hidden">{t("timeline.layout.selectedToDependents")}</span></div>
          <div className="order-5 min-w-0">{branch("after", t("timeline.layout.after"))}</div>
        </div>
        <div className="border-t border-border pt-4">{branch("relations", t("timeline.layout.relations"))}</div>
      </> : !focus ? <>
        {[...groups].map(([parent, list]) => <section key={parent} aria-label={parent ? t("timeline.overview.parentLabel", { id: parent }) : t("timeline.overview.withoutEpic")} className="space-y-3">
          <h2 className="break-words text-sm font-semibold">{parent ? t("timeline.overview.parentTitle", { title: nodes.get(parent)?.title || `#${parent.slice(0, 8)}` }) : t("timeline.overview.withoutEpic")}
            <span className="ml-2 text-muted-foreground">{t("timeline.overview.uniqueLoaded", { n: list.length })}</span></h2>
          <div className="grid min-w-0 grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">{list.map(node => <div key={node.id} className="min-w-0">
            {renderNode(node)}<p className="mt-2 text-xs text-muted-foreground">{t("timeline.overview.blockersLine", { blockers: node.unfinished_blockers, unknown: node.unknown_blockers, dependents: node.dependents })}</p>
          </div>)}</div>
        </section>)}
        {graph.tasks.length === 0 && <p className="rounded-xl border border-border border-dashed p-8 text-center">{search ? t("timeline.overview.noMatching") : t("timeline.overview.noOpen")}</p>}
        {!completed && graph.completed_count > 0 && <Button variant="outline" className="min-h-11 whitespace-normal" onClick={() => update({ completed: "true", page: null })}>{t("timeline.overview.completedExpand", { n: graph.completed_count })}</Button>}
        {graph.page > 1 && <Button variant="outline" disabled={loading} onClick={() => update({ page: String(graph.page - 1) })}>{t("timeline.overview.previousPage")}</Button>}
        {graph.has_more && <Button variant="outline" disabled={loading} onClick={() => update({ page: String(graph.page + 1) })}>{t("timeline.overview.loadNext", { n: Math.max(0, graph.total_count - graph.tasks.length) })}</Button>}
      </> : null}
    </>}
    <Button variant="ghost" disabled={history.current.length === 0} onClick={() => {
      const previous = history.current.pop(); if (previous !== undefined) setQuery(new URLSearchParams(previous));
    }}><ArrowLeft className="mr-2 h-4 w-4" /> {t("timeline.page.previousFocus")}</Button>
    {panelId && <TaskSlideOver taskId={panelId} onClose={() => setPanelId(null)} onTaskUpdated={() => setRetry(n => n + 1)} />}
  </div>;
}
