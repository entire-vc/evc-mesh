// Container for doc.tree / doc.view. The tree gets a flat list of the visible nodes; the view gets the core blocks
// (`components`) as stand-ins drawn here: each renders `data-mesh-block="<name>"`, so a test can count and find them
// without knowing the view's markup. The Editor stand-in keeps what was typed in its own state and stamps each
// instance (`data-instance`): a view that remounts it loses both (CT-docview-07).
import { createContext, useContext, useState, type ComponentType } from "react";
import type {
  ActorRef,
  Async,
  DocNodeData,
  DocTreeData,
  DocViewActions,
  DocViewData,
  DocViewDoc,
  RealtimeEvent,
  SlotContext,
} from "../../src/index";
import { fixtures } from "../fixtures";
import type { RenderEnv, Scenario, SlotMounter, Spies } from "../types";
import { registerMounter } from "./registry";

type Props<D, A> = { data: D; actions: A; themeSettings: Record<string, unknown>; ctx: SlotContext };

/** The error state with nothing to show: the view draws the message and a retry that reaches `actions.retry`. */
const errorOf = <T,>(message: string, spies: Spies): Async<T> => ({
  state: "error", error: { code: "x", message }, retry: () => void spies.retry?.(),
});

const valueOf = <T,>(a: Async<T>): T | undefined => (a.state === "loading" ? undefined : a.value);

// -- doc.tree ------------------------------------------------------------------------------------------

function treeReduce(data: DocTreeData, e: RealtimeEvent): DocTreeData {
  if (e.type !== "document.created") return data;
  const list = valueOf(data.nodes);
  if (!list) return data;
  const { id, title } = e.data as { id: string; title?: string };
  if (list.some((n) => n.id === id)) return data;
  // A new top-level page: appended in place; the other nodes keep their objects (expansion, selection untouched).
  const fresh: DocNodeData = fixtures.docNode(id, title ? { title } : {});
  return { ...data, nodes: { state: "ready", value: [...list, fresh] } };
}

const treeMounter: SlotMounter<DocTreeData> = {
  slot: "doc.tree",
  defaults: () => fixtures.docTree(),
  actions: [
    { name: "select" },
    { name: "toggleExpanded" },
    { name: "createChild", needs: "create" },
    { name: "rename", needs: "edit" },
    { name: "move", needs: "edit" },
    { name: "remove", needs: "edit" },
    { name: "retry" },
  ],
  reduce: treeReduce,
  render(env: RenderEnv<DocTreeData>) {
    const View = env.view as unknown as ComponentType<Props<DocTreeData, unknown>>;
    return <View data={env.data} actions={env.actions} themeSettings={env.themeSettings} ctx={env.ctx} />;
  },
  asyncData(data, state, spies) {
    const value = valueOf(data.nodes);
    if (state === "loading") return { ...data, nodes: { state: "loading" } };
    if (state === "error") return { ...data, nodes: errorOf("Could not load the pages", spies) };
    return { ...data, nodes: { state: "refreshing", value: value ?? [] } };
  },
};

// -- doc.view ------------------------------------------------------------------------------------------

export type DocViewFrame = Omit<DocViewData, "components">;

/** What the stand-in blocks read: the core blocks know the open document and the page actions. */
const BlockCtx = createContext<{ data: DocViewFrame; actions: Partial<DocViewActions> } | null>(null);
const useBlock = () => useContext(BlockCtx)!;

let instances = 0;

function Editor() {
  const [instance] = useState(() => ++instances);
  const [typed, setTyped] = useState("");
  return (
    <div data-mesh-block="Editor" data-instance={instance}>
      <textarea aria-label="Document body" value={typed} onChange={(e) => setTyped(e.target.value)} />
    </div>
  );
}

function Breadcrumbs() {
  const { data } = useBlock();
  const doc = valueOf(data.doc);
  return (
    <nav data-mesh-block="Breadcrumbs" aria-label="Breadcrumbs">
      {(doc?.path ?? []).map((p) => (
        <a key={p.href} href={p.href}>{p.label}</a>
      ))}
    </nav>
  );
}

function Meta() {
  const { data } = useBlock();
  const doc = valueOf(data.doc);
  return (
    <p data-mesh-block="Meta">
      {doc ? `Updated ${doc.updatedAt}${doc.updatedBy ? ` by ${doc.updatedBy.name}` : ""}` : ""}
    </p>
  );
}

function CommentsToggle() {
  const { actions } = useBlock();
  return (
    <button type="button" data-mesh-block="CommentsToggle" onClick={() => actions.toggleComments?.()}>
      Comments
    </button>
  );
}

const STANDINS: DocViewData["components"] = {
  Editor,
  Properties: () => <div data-mesh-block="Properties" />,
  Comments: () => (
    <div data-mesh-block="Comments">
      <p>Discussion</p>
    </div>
  ),
  WatchToggle: () => (
    <button type="button" data-mesh-block="WatchToggle">Watch</button>
  ),
  Breadcrumbs,
  CommentsRail: () => (
    <div data-mesh-block="CommentsRail">
      <p>Margin comments</p>
    </div>
  ),
  CommentsToggle,
  CommentsEntry: () => <span data-mesh-block="CommentsEntry" />,
  Meta,
};

function viewDefaults(): DocViewFrame {
  return {
    doc: { state: "ready", value: fixtures.doc() },
    save: { state: "idle" },
    layout: { commentsOpen: false },
    rootHref: "/docs",
    editing: false,
    anchorNotice: null,
  };
}

function viewReduce(data: DocViewFrame, e: RealtimeEvent): DocViewFrame {
  if (e.type !== "document.updated") return data;
  const doc = valueOf(data.doc);
  const d = e.data as { id: string; version?: number; updatedAt?: string; updatedBy?: ActorRef | null };
  if (!doc || doc.id !== d.id) return data;
  const next: DocViewDoc = {
    ...doc,
    version: d.version ?? doc.version + 1,
    updatedAt: d.updatedAt ?? doc.updatedAt,
    updatedBy: d.updatedBy === undefined ? doc.updatedBy : d.updatedBy,
  };
  return { ...data, doc: { state: "ready", value: next } };
}

const viewMounter: SlotMounter<DocViewFrame> = {
  slot: "doc.view",
  defaults: (_s: Scenario<DocViewFrame>) => viewDefaults(),
  actions: [
    { name: "toggleComments" },
    { name: "retry" },
    { name: "exportDoc" },
    { name: "resolveConflict", needs: "edit" },
    { name: "startEditing", needs: "edit" },
    { name: "finishEditing", needs: "edit" },
    { name: "rename", needs: "edit" },
    { name: "move", needs: "edit" },
    { name: "remove", needs: "edit" },
    { name: "copyUrl" },
    { name: "share" },
    { name: "navigate" },
  ],
  reduce: viewReduce,
  render(env: RenderEnv<DocViewFrame>) {
    const View = env.view as unknown as ComponentType<Props<DocViewData, unknown>>;
    const actions = env.actions as Partial<DocViewActions>;
    return (
      <BlockCtx.Provider value={{ data: env.data, actions }}>
        <View data={{ ...env.data, components: STANDINS }} actions={actions} themeSettings={env.themeSettings} ctx={env.ctx} />
      </BlockCtx.Provider>
    );
  },
  asyncData(data, state, spies) {
    const value = valueOf(data.doc);
    if (state === "loading") return { ...data, doc: { state: "loading" } };
    if (state === "error") return { ...data, doc: errorOf("Could not open the document", spies) };
    return { ...data, doc: { state: "refreshing", value: value ?? fixtures.doc() } };
  },
};

registerMounter(treeMounter);
registerMounter(viewMounter);
