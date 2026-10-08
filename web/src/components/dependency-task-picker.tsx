import { useEffect, useId, useRef, useState } from "react";
import { api, ApiRequestError } from "@/lib/api";
import { apiErrorMessage } from "@/lib/api-error";
import { Input } from "@/components/ui/input";
import { useWorkspaceStore } from "@/stores/workspace";
import type { PaginatedResponse, Project, Task } from "@/types";

export interface DependencyTarget { task: Task; projectName: string }
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const SHORT_ID = /^[0-9a-f]{6,12}$/i;

export function DependencyTaskPicker({ taskId, disabled, selected, onSelect }: {
  taskId: string;
  disabled: boolean;
  selected: DependencyTarget | null;
  onSelect: (target: DependencyTarget | null) => void;
}) {
  const workspaceId = useWorkspaceStore(state => state.currentWorkspace?.id);
  const id = useId();
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<DependencyTarget[]>([]);
  const [phase, setPhase] = useState<"idle" | "loading" | "ready" | "error">("idle");
  const [error, setError] = useState("");
  const [active, setActive] = useState(-1);
  const generation = useRef(0);
  const controller = useRef<AbortController | null>(null);
  const selectRef = useRef(onSelect); selectRef.current = onSelect;

  useEffect(() => {
    selectRef.current(null);
    setQuery(""); setResults([]); setPhase("idle");
  }, [taskId, workspaceId]);

  useEffect(() => {
    const request = ++generation.current;
    const abort = new AbortController(); controller.current = abort;
    const current = () => request === generation.current && !abort.signal.aborted;
    const raw = query.trim();
    if (!raw) { setPhase("idle"); return () => abort.abort(); }
    if (!workspaceId) { setPhase("error"); setError("Choose a workspace before searching for a task."); return () => abort.abort(); }
    setPhase("loading");
    const timer = setTimeout(async () => {
      try {
        const clean = raw.replace(/^#/, "").toLowerCase();
        const exact = UUID.test(clean) || SHORT_ID.test(clean);
        if (raw.startsWith("#") && !SHORT_ID.test(clean)) throw new Error("Enter a short ID with 6–12 hexadecimal characters.");
        const tasks = exact
          ? [await api<Task>(UUID.test(clean) ? `/api/v1/tasks/${clean}` : `/api/v1/tasks/by-short-id/${clean}`, { signal: abort.signal })]
          : (await api<PaginatedResponse<Task>>(`/api/v1/workspaces/${workspaceId}/tasks`, { params: { search: raw, page_size: 10, include_description: "false" }, signal: abort.signal })).items;
        if (!current()) return;
        const projects = new Map<string, Promise<Project>>();
        const hydrated = await Promise.all((tasks ?? []).slice(0, 10).map(async task => {
          if (task.id === taskId) {
            if (exact) throw new Error("A task cannot depend on itself.");
            return null;
          }
          try {
            if (!projects.has(task.project_id)) projects.set(task.project_id, api<Project>(`/api/v1/projects/${task.project_id}`, { signal: abort.signal }));
            const project = await projects.get(task.project_id)!;
            if (project.workspace_id !== workspaceId) throw new Error("Choose a task in the current workspace.");
            return { task, projectName: project.name };
          } catch (err) {
            if (exact) throw err;
            return null;
          }
        }));
        if (current()) { setResults(hydrated.filter((item): item is DependencyTarget => item !== null)); setActive(-1); setPhase("ready"); }
      } catch (err) {
        if (!current()) return;
        if (err instanceof ApiRequestError && err.status === 404) { setResults([]); setPhase("ready"); }
        else {
          setError(err instanceof ApiRequestError && err.status === 400 && /ambiguous/i.test(err.message)
            ? "This short ID is ambiguous. Enter a longer prefix or full UUID."
            : apiErrorMessage(err, "Could not search tasks."));
          setPhase("error");
        }
      }
    }, 250);
    return () => { clearTimeout(timer); abort.abort(); };
  }, [query, taskId, workspaceId]);

  const choose = (target: DependencyTarget) => {
    generation.current++; controller.current?.abort();
    onSelect(target); setResults([]); setPhase("idle"); setActive(-1);
  };

  return <div className="space-y-2">
    <label htmlFor={id} className="block text-xs text-muted-foreground">Find task</label>
    <Input id={id} role="combobox" aria-autocomplete="list" aria-expanded={!selected && results.length > 0}
      aria-controls={`${id}-results`} aria-activedescendant={active >= 0 ? `${id}-option-${active}` : undefined}
      value={query} disabled={disabled} autoFocus placeholder="Title, #shortid, or full UUID" className="h-8 text-xs"
      onChange={e => {
        generation.current++; controller.current?.abort(); onSelect(null);
        setResults([]); setActive(-1); setError(""); setQuery(e.target.value);
      }}
      onKeyDown={e => {
        if (e.key === "ArrowDown" || e.key === "ArrowUp") {
          e.preventDefault(); if (results.length) setActive(index => e.key === "ArrowDown" ? (index + 1) % results.length : (index <= 0 ? results.length : index) - 1);
        } else if (e.key === "Enter" && !selected) {
          e.preventDefault(); if (active >= 0 && results[active]) choose(results[active]);
        } else if (e.key === "Escape") {
          e.preventDefault(); generation.current++; controller.current?.abort(); setResults([]); setActive(-1); setPhase("idle");
        }
      }} />
    {selected && <p className="break-words text-xs">Selected: #{selected.task.id.slice(0, 8)} {selected.task.title} · {selected.projectName}</p>}
    {!selected && phase === "loading" && <p role="status" className="text-xs text-muted-foreground">Searching tasks…</p>}
    {!selected && phase === "error" && <p role="alert" className="text-xs text-destructive">{error}</p>}
    {!selected && phase === "ready" && results.length === 0 && <p role="status" className="text-xs text-muted-foreground">No matching tasks.</p>}
    <div id={`${id}-results`} role="listbox" aria-label="Matching tasks" className="max-h-52 overflow-y-auto">
      {results.map((target, index) => <button key={target.task.id} id={`${id}-option-${index}`} type="button" role="option" aria-selected={index === active}
        disabled={disabled} onClick={() => choose(target)} className={`block w-full rounded p-2 text-left text-xs hover:bg-muted ${index === active ? "bg-muted" : ""}`}>
        <span className="break-words">#{target.task.id.slice(0, 8)} {target.task.title}</span>{" "}<span className="block text-muted-foreground">{target.projectName}</span>
      </button>)}
    </div>
  </div>;
}
