import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { useEffect, useState } from "react";
import { Slot, registerSlotViewOverride } from "../registry";
import "../mesh-default";
import { registerMeshDefaultTheme } from "@/themes/mesh-default";
import { DocTree } from "@/components/doc-tree";
import type {
  Async,
  DocNodeData,
  DocTreeActions,
  DocTreeData,
  DocTreeView,
  DocViewActions,
  DocViewData,
  DocViewDoc,
  SlotContext,
} from "../types";
import type { ProjectDocument } from "@/types";

// CT-doctree-01..07 and CT-docview-01..07 from the spec «Слоты task.row, task.view, nav.*, doc.*,
// auth.screen (контракт v1)», sections 5 and 6, run on the mesh-default views through the `Slot`
// entry. The last two describes draw the same containers with a stub view: the logic is not in the view.

registerMeshDefaultTheme();
afterEach(cleanup);

const ctx = (slot: string): SlotContext => ({
  slot,
  contractVersion: "1.0.0",
  locale: "en",
  mode: "light",
  isMobile: false,
  workspace: null,
});

// ---------------------------------------------------------------------------
// doc.tree
// ---------------------------------------------------------------------------

const node = (id: string, over: Partial<DocNodeData> = {}): DocNodeData => ({
  id,
  title: `Doc ${id}`,
  slug: id,
  depth: 0,
  hasChildren: false,
  expanded: false,
  selected: false,
  updatedAt: "2026-10-01T00:00:00Z",
  href: `/docs/${id}`,
  ...over,
});

// root (expanded) > kid-a, kid-b ; sibling-less
const NODES = [
  node("root", { hasChildren: true, expanded: true }),
  node("kid-a", { depth: 1, hasChildren: true }),
  node("kid-b", { depth: 1 }),
];

const treeActions = (over: Partial<DocTreeActions> = {}): DocTreeActions => ({
  select: vi.fn(),
  toggleExpanded: vi.fn(),
  createChild: vi.fn(),
  rename: vi.fn(),
  move: vi.fn(),
  remove: vi.fn(),
  retry: vi.fn(),
  ...over,
});

function mountTree(nodes: Async<DocNodeData[]>, actions = treeActions()) {
  const data: DocTreeData = { projectId: "p1", nodes };
  render(<Slot name="doc.tree" data={data} actions={actions} themeSettings={{}} ctx={ctx("doc.tree")} />);
  return actions;
}
const ready = (value: DocNodeData[]): Async<DocNodeData[]> => ({ state: "ready", value });

describe("doc.tree contract on mesh-default", () => {
  it("CT-doctree-01 tree / treeitem roles, aria-level by depth, aria-expanded only on nodes with children", () => {
    mountTree(ready(NODES));
    expect(screen.getByRole("tree")).toBeInTheDocument();
    const items = screen.getAllByRole("treeitem");
    expect(items.map((i) => i.getAttribute("aria-level"))).toEqual(["1", "2", "2"]);
    expect(items[0]).toHaveAttribute("aria-expanded", "true");
    expect(items[1]).toHaveAttribute("aria-expanded", "false");
    expect(items[2]).not.toHaveAttribute("aria-expanded");
  });

  it("CT-doctree-02 keyboard: ArrowDown / ArrowRight / ArrowLeft / Enter follow the WAI-ARIA tree pattern", () => {
    const actions = mountTree(ready(NODES));
    const [root, kidA, kidB] = screen.getAllByRole("treeitem");
    root!.focus();
    fireEvent.keyDown(root!, { key: "ArrowDown" });
    expect(kidA).toHaveFocus();
    fireEvent.keyDown(kidA!, { key: "ArrowDown" });
    expect(kidB).toHaveFocus();
    fireEvent.keyDown(kidB!, { key: "ArrowUp" });
    expect(kidA).toHaveFocus();
    // Right on a closed node opens it; Left on an open one closes it.
    fireEvent.keyDown(kidA!, { key: "ArrowRight" });
    expect(actions.toggleExpanded).toHaveBeenCalledWith("kid-a");
    fireEvent.keyDown(root!, { key: "ArrowLeft" });
    expect(actions.toggleExpanded).toHaveBeenCalledWith("root");
    // Left on a leaf or closed node goes to the parent.
    fireEvent.keyDown(kidB!, { key: "ArrowLeft" });
    expect(root).toHaveFocus();
    fireEvent.keyDown(kidA!, { key: "Enter" });
    expect(actions.select).toHaveBeenCalledWith("kid-a");
  });

  it("CT-doctree-03 click / Ctrl+click select(id) / select(id, {newTab}); the link has a real href", () => {
    const actions = mountTree(ready(NODES));
    const link = screen.getByRole("link", { name: "Doc kid-b" });
    expect(link).toHaveAttribute("href", "/docs/kid-b");
    fireEvent.click(link);
    expect(actions.select).toHaveBeenLastCalledWith("kid-b", undefined);
    fireEvent.click(link, { ctrlKey: true });
    expect(actions.select).toHaveBeenLastCalledWith("kid-b", { newTab: true });
  });

  it("CT-doctree-04 a viewer (no node actions) gets no create / rename / move / delete controls", () => {
    mountTree(
      ready(NODES),
      treeActions({ createChild: undefined, rename: undefined, move: undefined, remove: undefined }),
    );
    expect(screen.queryByRole("button", { name: /new page inside/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /actions for/i })).toBeNull();
    // Opening and closing a branch is not an edit.
    expect(screen.getAllByRole("button", { name: /collapse|expand/i }).length).toBeGreaterThan(0);
  });

  it("CT-doctree-05 the selected node carries aria-selected and aria-current", () => {
    mountTree(ready([node("a"), node("b", { selected: true })]));
    const [a, b] = screen.getAllByRole("treeitem");
    expect(b).toHaveAttribute("aria-selected", "true");
    expect(b).toHaveAttribute("aria-current", "page");
    expect(a).toHaveAttribute("aria-selected", "false");
    expect(a).not.toHaveAttribute("aria-current");
  });

  it("CT-doctree-06 a document that appears keeps the expansion and the selection (container, realtime-style list update)", () => {
    const doc = (id: string, parent_id: string | null, position: number): ProjectDocument =>
      ({
        id, project_id: "p1", parent_id, slug: id, title: `Doc ${id}`, storage_key: id, position,
        created_by: "u", created_by_type: "user", created_at: "2026-10-01T00:00:00Z",
        updated_at: "2026-10-01T00:00:00Z", version: 1,
      }) as ProjectDocument;
    const docs = [doc("root", null, 0), doc("kid", "root", 0)];
    const props = {
      projectId: "p1" as const,
      selectedId: "kid",
      onSelect: vi.fn(), onCreateChild: vi.fn(), onRename: vi.fn(), onMove: vi.fn(), onDelete: vi.fn(),
    };
    const { rerender } = render(<DocTree {...props} documents={docs} />);
    expect(screen.getByText("Doc kid")).toBeInTheDocument(); // the branch to the open document is open
    rerender(<DocTree {...props} documents={[...docs, doc("fresh", null, 1)]} />);
    expect(screen.getByText("Doc fresh")).toBeInTheDocument();
    expect(screen.getByText("Doc kid")).toBeInTheDocument();
    const selected = screen.getAllByRole("treeitem").filter((i) => i.getAttribute("aria-selected") === "true");
    expect(selected).toHaveLength(1);
    expect(selected[0]).toHaveTextContent("Doc kid");
  });

  it("CT-doctree-07 loading draws a skeleton; error draws the message and retry calls retry()", () => {
    const { unmount } = render(
      <Slot name="doc.tree" data={{ projectId: "p1", nodes: { state: "loading" } }} actions={treeActions()} themeSettings={{}} ctx={ctx("doc.tree")} />,
    );
    expect(screen.getByTestId("doc-tree-loading")).toBeInTheDocument();
    expect(screen.queryByRole("tree")).toBeNull();
    unmount();
    const actions = treeActions();
    const retry = vi.fn();
    render(
      <Slot
        name="doc.tree"
        data={{ projectId: "p1", nodes: { state: "error", error: { code: "x", message: "Boom" }, retry } }}
        actions={actions}
        themeSettings={{}}
        ctx={ctx("doc.tree")}
      />,
    );
    expect(screen.getByText("Boom")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(retry).toHaveBeenCalledTimes(1);
  });
});

// ---------------------------------------------------------------------------
// doc.view
// ---------------------------------------------------------------------------

const OPEN_DOC: DocViewDoc = {
  id: "d1",
  title: "Runbook",
  slug: "runbook",
  path: [{ label: "Ops", href: "/docs/ops" }, { label: "Runbook", href: "/docs/d1" }],
  updatedAt: "2026-10-01T00:00:00Z",
  updatedBy: null,
  version: 3,
};

const mounts = { editor: 0 };
function Editor() {
  useEffect(() => {
    mounts.editor += 1;
  }, []);
  const [typed, setTyped] = useState("");
  return <textarea aria-label="stub editor" value={typed} onChange={(e) => setTyped(e.target.value)} />;
}
const blocks = (): DocViewData["components"] => ({
  Editor,
  Properties: () => null,
  Comments: () => <section data-testid="comments">comments</section>,
  WatchToggle: () => <button type="button">watch</button>,
  Breadcrumbs: () => <nav aria-label="crumbs">{OPEN_DOC.path.map((p) => p.label).join(" / ")}</nav>,
  CommentsRail: () => <aside data-testid="rail">rail</aside>,
  CommentsToggle: () => null,
  CommentsEntry: () => null,
  Meta: () => <p>meta</p>,
});

const viewData = (over: Partial<DocViewData> = {}): DocViewData => ({
  doc: { state: "ready", value: OPEN_DOC },
  save: { state: "idle" },
  components: blocks(),
  layout: { commentsOpen: false },
  rootHref: "/docs",
  editing: false,
  anchorNotice: null,
  ...over,
});
const viewActions = (over: Partial<DocViewActions> = {}): DocViewActions => ({
  toggleComments: vi.fn(),
  retry: vi.fn(),
  copyUrl: vi.fn(),
  navigate: vi.fn(),
  startEditing: vi.fn(),
  finishEditing: vi.fn(),
  ...over,
});
const mountView = (data: DocViewData, actions = viewActions()) => {
  const el = (d: DocViewData) => (
    <Slot name="doc.view" data={d} actions={actions} themeSettings={{}} ctx={ctx("doc.view")} />
  );
  const r = render(el(data));
  return { actions, rerender: (d: DocViewData) => r.rerender(el(d)) };
};

describe("doc.view contract on mesh-default", () => {
  it("CT-docview-01 ready: the editor exactly once, breadcrumbs and title drawn", () => {
    mountView(viewData());
    expect(screen.getAllByLabelText("stub editor")).toHaveLength(1);
    expect(screen.getByRole("heading", { level: 1, name: "Runbook" })).toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "crumbs" })).toHaveTextContent("Ops / Runbook");
  });

  it("CT-docview-02 save.state saving / saved / error is shown; error carries its message and retry", () => {
    const { actions, rerender } = mountView(viewData({ save: { state: "saving" } }));
    expect(screen.getByText("Saving...")).toBeInTheDocument();
    rerender(viewData({ save: { state: "saved" } }));
    expect(screen.getByText("Saved")).toBeInTheDocument();
    rerender(viewData({ save: { state: "error", message: "disk full" } }));
    expect(screen.getByText(/Not saved: disk full/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(actions.retry).toHaveBeenCalledTimes(1);
  });

  it("CT-docview-03 conflict offers reload / keep-mine through resolveConflict; without it the approved copy stands alone", () => {
    const resolveConflict = vi.fn();
    const { rerender } = mountView(viewData({ save: { state: "conflict" } }), viewActions({ resolveConflict }));
    fireEvent.click(screen.getByRole("button", { name: "Reload" }));
    expect(resolveConflict).toHaveBeenLastCalledWith("reload");
    fireEvent.click(screen.getByRole("button", { name: "Keep mine" }));
    expect(resolveConflict).toHaveBeenLastCalledWith("keep-mine");
    cleanup();
    mountView(viewData({ save: { state: "conflict" } }));
    expect(screen.getByText("this page changed elsewhere")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reload" })).toBeNull();
    void rerender;
  });

  it("CT-docview-04 a viewer: the view adds no edit button of its own", () => {
    mountView(viewData(), viewActions({ startEditing: undefined, finishEditing: undefined, rename: undefined, move: undefined, remove: undefined }));
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Done" })).toBeNull();
  });

  it("CT-docview-05 the comments rail follows layout.commentsOpen", () => {
    const { rerender } = mountView(viewData({ layout: { commentsOpen: false } }));
    expect(screen.queryByTestId("rail")).toBeNull();
    rerender(viewData({ layout: { commentsOpen: true } }));
    expect(screen.getByTestId("rail")).toBeInTheDocument();
    expect(screen.getByTestId("comments")).toBeInTheDocument(); // the discussion under the article stays
  });

  it("CT-docview-06 loading draws a skeleton; error draws the message and retry", () => {
    const { rerender, actions } = mountView(viewData({ doc: { state: "loading" } }));
    expect(screen.queryByRole("heading", { level: 1 })).toBeNull();
    expect(screen.queryByLabelText("stub editor")).toBeNull();
    const retry = vi.fn();
    rerender(viewData({ doc: { state: "error", error: { code: "x", message: "Failed to open document" }, retry } }));
    expect(screen.getByText("Failed to open document")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(retry).toHaveBeenCalledTimes(1);
    void actions;
  });

  it("CT-docview-07 a newer version of the document does not remount the editor or drop what was typed", () => {
    mounts.editor = 0;
    const { rerender } = mountView(viewData());
    fireEvent.change(screen.getByLabelText("stub editor"), { target: { value: "half a sentence" } });
    rerender(viewData({ doc: { state: "ready", value: { ...OPEN_DOC, version: 4, updatedAt: "2026-10-02T00:00:00Z" } } }));
    expect(mounts.editor).toBe(1);
    expect(screen.getByLabelText("stub editor")).toHaveValue("half a sentence");
  });
});

// ---------------------------------------------------------------------------
// The same containers, a different tree view: expansion and selection live in the container.
// ---------------------------------------------------------------------------

describe("doc.tree stub view through the DocTree container", () => {
  it("a view with another markup still selects and expands through the container", () => {
    const StubTree: DocTreeView = ({ data, actions }) => (
      <div data-testid="stub-tree">
        {data.nodes.state === "ready" &&
          data.nodes.value.map((n) => (
            <div key={n.id} data-depth={n.depth}>
              <span>{n.title}</span>
              <button type="button" onClick={() => actions.toggleExpanded(n.id)}>{`toggle ${n.id}`}</button>
              <button type="button" onClick={() => actions.select(n.id)}>{`open ${n.id}`}</button>
            </div>
          ))}
      </div>
    );
    const unregister = registerSlotViewOverride("doc.tree", StubTree);
    try {
      const doc = (id: string, parent_id: string | null): ProjectDocument =>
        ({
          id, project_id: "p1", parent_id, slug: id, title: `Doc ${id}`, storage_key: id, position: 0,
          created_by: "u", created_by_type: "user", created_at: "2026-10-01T00:00:00Z",
          updated_at: "2026-10-01T00:00:00Z", version: 1,
        }) as ProjectDocument;
      const onSelect = vi.fn();
      render(
        <DocTree
          projectId="p1"
          documents={[doc("root", null), doc("kid", "root")]}
          onSelect={onSelect}
          onCreateChild={vi.fn()}
          onRename={vi.fn()}
          onMove={vi.fn()}
          onDelete={vi.fn()}
        />,
      );
      const stub = screen.getByTestId("stub-tree");
      expect(within(stub).queryByText("Doc kid")).toBeNull(); // collapsed by default
      fireEvent.click(within(stub).getByRole("button", { name: "toggle root" }));
      expect(within(stub).getByText("Doc kid")).toBeInTheDocument();
      fireEvent.click(within(stub).getByRole("button", { name: "open kid" }));
      expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ id: "kid" }));
    } finally {
      unregister();
    }
  });

  it("routes createChild(null) from a custom doc.tree view to the container's root-create", () => {
    const StubTree: DocTreeView = ({ actions }) => (
      <div>
        <button type="button" onClick={() => actions.createChild?.(null)}>create root</button>
        <button type="button" onClick={() => actions.createChild?.("missing")}>create under missing</button>
      </div>
    );
    const unregister = registerSlotViewOverride("doc.tree", StubTree);
    try {
      const onCreateChild = vi.fn();
      render(
        <DocTree
          projectId="p1"
          documents={[]}
          onSelect={vi.fn()}
          onCreateChild={onCreateChild}
          onRename={vi.fn()}
          onMove={vi.fn()}
          onDelete={vi.fn()}
        />,
      );
      fireEvent.click(screen.getByRole("button", { name: "create under missing" }));
      expect(onCreateChild).not.toHaveBeenCalled();
      fireEvent.click(screen.getByRole("button", { name: "create root" }));
      expect(onCreateChild).toHaveBeenCalledExactlyOnceWith(null);
    } finally {
      unregister();
    }
  });
});
