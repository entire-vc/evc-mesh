// Containers for task.row and task.view. The view under test gets what the page gives it: data held in state,
// actions as spies, and stand-ins for the core's ready blocks (task.view `components`, task.row `components.Badges`).
// A stand-in marks itself (`data-mesh-block` + `data-ct-standin`) so a test counts what the view placed, not what
// it wrapped around it; the comments stand-in keeps its draft in its own state like the core's block does.
import { useEffect, useMemo, useState, type FC } from "react";
import type {
  RealtimeEvent,
  StatusData,
  TaskData,
  TaskRowActions,
  TaskRowData,
  TaskRowView,
  TaskViewActions,
  TaskViewBlockId,
  TaskViewData,
  TaskViewView,
} from "../../src/index";
import { fixtures } from "../fixtures";
import type { ExtensionPoint, RenderEnv, SlotMounter, Spies } from "../types";
import { registerMounter } from "./registry";

/** Id of the extension the core host installs at every point (web/src/test-utils/slot-host.tsx). */
export const EXTENSION_ID = "gitlab";
const EXTENSION_NAME = "GitLab";

const has = (env: RenderEnv<unknown>, p: ExtensionPoint) => (env.scenario.extensions ?? []).includes(p);

// -- task.row ------------------------------------------------------------------------------------------

export type RowData = Omit<TaskRowData, "components">;

function RowHost({ env }: { env: RenderEnv<RowData> }) {
  const d = env.data;
  // The list container owns `selected` / `expanded`: the action flips them and the row gets the new data.
  const [selected, setSelected] = useState(d.selected);
  const [expanded, setExpanded] = useState(d.expanded);
  useEffect(() => setSelected(d.selected), [d.selected]);
  useEffect(() => setExpanded(d.expanded), [d.expanded]);
  const badge = has(env as RenderEnv<unknown>, "task.badge");
  const components = useMemo<TaskRowData["components"]>(
    () => ({ Badges: () => (badge ? <span data-ct-standin="badges" aria-label={EXTENSION_NAME} /> : null) }),
    [badge],
  );
  const a = env.actions as Record<string, (...x: unknown[]) => unknown>;
  const actions = useMemo<TaskRowActions>(() => {
    const out: TaskRowActions = {
      openTask: (o) => void a.openTask!(o),
      toggleSelected: () => {
        a.toggleSelected!();
        setSelected((s) => !s);
      },
    };
    if (d.expandable && a.toggleExpanded) {
      out.toggleExpanded = () => {
        a.toggleExpanded!();
        setExpanded((s) => !s);
      };
    }
    if (a.quickEdit) out.quickEdit = (p) => a.quickEdit!(p) as Promise<void>;
    if (a.changeStatus) out.changeStatus = (id) => a.changeStatus!(id) as Promise<void>;
    return out;
  }, [a, d.expandable]);
  const View = env.view as unknown as TaskRowView;
  return (
    <table>
      <tbody>
        <View data={{ ...d, selected, expanded, components }} actions={actions} themeSettings={env.themeSettings} ctx={env.ctx} />
      </tbody>
    </table>
  );
}

const statusById = (statuses: StatusData[], id: unknown) => statuses.find((s) => s.id === id);

function reduceTask<T extends TaskData>(t: T, e: RealtimeEvent): T {
  const p = e.data as { id?: string; title?: string; statusId?: string; priority?: TaskData["priority"] };
  if (e.type !== "task.updated" || p.id !== t.id) return t;
  return {
    ...t,
    ...(p.title ? { title: p.title } : {}),
    ...(p.statusId ? { statusId: p.statusId } : {}),
    ...(p.priority ? { priority: p.priority } : {}),
  };
}

const rowMounter: SlotMounter<RowData> = {
  slot: "task.row",
  defaults: () => fixtures.row(),
  actions: [
    { name: "openTask" },
    { name: "toggleSelected" },
    { name: "toggleExpanded" },
    { name: "quickEdit", needs: "edit", async: true },
    { name: "changeStatus", needs: "move", async: true },
  ],
  reduce(d, e) {
    const task = reduceTask(d.task, e);
    if (task === d.task) return d;
    return { ...d, task, status: statusById(d.statuses, task.statusId) ?? d.status };
  },
  render: (env) => <RowHost env={env} />,
};

// -- task.view -----------------------------------------------------------------------------------------

export type ViewData = Omit<TaskViewData, "components">;

const STANDIN_BLOCKS: TaskViewBlockId[] = ["header", "description", "properties", "subtasks", "dependencies", "artifacts", "vcs", "activity"];

/** A core block: the view decides where it goes, the block itself is the core's. */
const block = (id: TaskViewBlockId): FC => {
  const B: FC = () => (
    <div data-mesh-block={id} data-ct-standin={id}>
      {id}
    </div>
  );
  B.displayName = `CoreBlock(${id})`;
  return B;
};

function makeComponents(env: RenderEnv<ViewData>): TaskViewData["components"] {
  const a = env.actions as Record<string, (...x: unknown[]) => unknown>;
  const actionsExt = has(env as RenderEnv<unknown>, "task.actions");
  const panelExt = has(env as RenderEnv<unknown>, "task.panel");
  const out = Object.fromEntries(STANDIN_BLOCKS.map((id) => [id, block(id)])) as Record<TaskViewBlockId, FC>;
  // The comment feed keeps the draft in its own state (CT-view-07, CT-view-09).
  out.comments = () => {
    const [draft, setDraft] = useState("");
    return (
      <div data-mesh-block="comments" data-ct-standin="comments">
        <textarea aria-label="Comment draft" value={draft} onChange={(e) => setDraft(e.target.value)} />
      </div>
    );
  };
  // The subtask list opens a subtask through the core's action.
  out.subtasks = () => (
    <div data-mesh-block="subtasks" data-ct-standin="subtasks">
      <button type="button" onClick={() => a.openTask!("sub-1")}>
        Subtask sub-1
      </button>
    </div>
  );
  // The core's top bar: close (slide-over), the extension `task.actions` items. An extension that asks for its
  // panel (the GitLab action does `openPanel`) is replayed once on arrival, so a view that hosts `task.panel`
  // shows it without a click.
  const TopBar: TaskViewData["components"]["TopBar"] = ({ openPanel }) => {
    useEffect(() => {
      if (panelExt) openPanel(EXTENSION_ID);
      // eslint-disable-next-line react-hooks/exhaustive-deps
    }, []);
    return (
      <div data-ct-standin="topbar">
        {actionsExt ? (
          <button type="button" data-extension-slot="task.actions" onClick={() => openPanel(EXTENSION_ID)}>
            Open {EXTENSION_NAME}
          </button>
        ) : null}
        {a.close ? (
          <button type="button" aria-label="Close" onClick={() => a.close!()}>
            ×
          </button>
        ) : null}
      </div>
    );
  };
  const ExtensionPanel: TaskViewData["components"]["ExtensionPanel"] = ({ extensionId, name }) =>
    panelExt ? (
      <div data-extension-slot="task.panel" data-extension-id={extensionId}>
        {name}
      </div>
    ) : null;
  return { ...out, TopBar, DependencyBadge: () => null, ExtensionPanel };
}

function ViewHost({ env }: { env: RenderEnv<ViewData> }) {
  // One set of blocks per mount: a block that remounted would lose its state.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const components = useMemo(() => makeComponents(env), []);
  const a = env.actions as Record<string, (...x: unknown[]) => unknown>;
  const slideOver = env.data.presentation === "slide-over";
  const actions = useMemo<TaskViewActions>(() => {
    const out: TaskViewActions = { openTask: (id, o) => void a.openTask!(id, o), retry: () => void a.retry!() };
    if (slideOver && a.close) out.close = () => void a.close!();
    if (a.quickEdit) out.quickEdit = (p) => a.quickEdit!(p) as Promise<void>;
    if (a.changeStatus) out.changeStatus = (id) => a.changeStatus!(id) as Promise<void>;
    if (a.assign) out.assign = (x) => a.assign!(x) as Promise<void>;
    return out;
  }, [a, slideOver]);
  const View = env.view as unknown as TaskViewView;
  return <View data={{ ...env.data, components }} actions={actions} themeSettings={env.themeSettings} ctx={env.ctx} />;
}

const valueOf = (d: ViewData) => (d.task.state === "ready" || d.task.state === "refreshing" ? d.task.value : null);

const viewMounter: SlotMounter<ViewData> = {
  slot: "task.view",
  defaults: () => fixtures.view(),
  actions: [
    { name: "openTask" },
    { name: "retry" },
    { name: "close" },
    { name: "quickEdit", needs: "edit", async: true },
    { name: "changeStatus", needs: "move", async: true },
    { name: "assign", needs: "edit", async: true },
  ],
  reduce(d, e) {
    const v = valueOf(d);
    if (!v || d.task.state === "error" || d.task.state === "loading") return d;
    const next = reduceTask(v, e);
    if (next === v) return d;
    return { ...d, task: { state: d.task.state, value: next }, status: statusById(d.statuses, next.statusId) ?? d.status };
  },
  render: (env) => <ViewHost env={env} />,
  asyncData(d, state, spies: Spies) {
    const v = valueOf(d);
    if (state === "loading") return { ...d, task: { state: "loading" }, status: null };
    if (state === "error") {
      return { ...d, status: null, task: { state: "error", error: { code: "x", message: "Could not load the task" }, retry: () => void spies.retry?.() } };
    }
    return v ? { ...d, task: { state: "refreshing", value: v } } : d;
  },
};

registerMounter(rowMounter);
registerMounter(viewMounter);
