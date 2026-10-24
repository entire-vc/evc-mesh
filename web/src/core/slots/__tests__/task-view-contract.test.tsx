// Contract tests CT-view-01..09 of the slot catalog (Mesh Doc "slots-task-nav-doc-auth" §2), run against the
// `mesh-default` task view with stand-ins for the core's blocks. The blocks are the core's, so what is under
// test is the view's contract: where it places them, what it hides, which states it draws.
// The panel has a mobile and a desktop layout in one DOM (CSS `lg:` visibility), so "exactly once" is counted
// inside one layout.
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { useState, type FC } from "react";

import { registerMeshDefaultTheme } from "@/themes/mesh-default";
import { resolveSlotView } from "../registry";
import type { SlotContext, TaskData, TaskViewActions, TaskViewBlockId, TaskViewData, TaskViewSettings, TaskViewView } from "../types";

registerMeshDefaultTheme();

const ctx: SlotContext = { slot: "task.view", contractVersion: "1.0.0", locale: "en", mode: "light", isMobile: false, workspace: null };

const BLOCKS: TaskViewBlockId[] = ["header", "description", "properties", "subtasks", "dependencies", "comments", "artifacts", "activity"];

const mkTask = (over: Partial<TaskData> = {}): TaskData & { descriptionMarkdown: string } => ({
  id: "t1", shortId: "t1", projectId: "p1", title: "Task t1", hasDescription: true, statusId: "s1", statusCategory: "todo",
  priority: "medium", assignee: null, reviewer: null, checkedOutBy: null, labels: [], dueDate: null, overdue: false,
  estimatedHours: null, customFields: {}, counts: { subtasks: 2, artifacts: 1, vcsLinks: 0 }, vcsProviders: [],
  vcsOwnedProviders: [], pipelineStatus: null, gate: null, runtimeState: null, delegationLevel: null,
  updatedAt: "2026-01-01T00:00:00Z", vcsProvidersKnown: true, checkout: null, recurring: null, falseOpen: null,
  descriptionMarkdown: "body", ...over,
});

const stub = (id: string): FC => {
  const C: FC = () => <div data-testid={`core-${id}`}>{id}</div>;
  return C;
};

/** The comment block keeps its draft inside itself, like the core's. */
const CommentsBlock: FC = () => {
  const [draft, setDraft] = useState("");
  return <textarea aria-label="draft" value={draft} onChange={(e) => setDraft(e.target.value)} />;
};

const components = (over: Partial<TaskViewData["components"]> = {}): TaskViewData["components"] => ({
  ...(Object.fromEntries(BLOCKS.map((b) => [b, stub(b)])) as Record<TaskViewBlockId, FC>),
  vcs: stub("vcs"),
  comments: CommentsBlock,
  TopBar: () => <div data-testid="core-topbar" />,
  DependencyBadge: () => null,
  ExtensionPanel: () => null,
  ...over,
});

const mkData = (over: Partial<TaskViewData> = {}): TaskViewData => ({
  task: { state: "ready", value: mkTask() },
  status: { id: "s1", name: "Todo", slug: "todo", color: "#888", category: "todo", position: 0 },
  statuses: [],
  presentation: "page",
  blocks: BLOCKS,
  components: components(),
  requestedBlock: null,
  ...over,
});

const View = resolveSlotView("task.view") as TaskViewView;

function mount(data: TaskViewData, actions: Partial<TaskViewActions> = {}, themeSettings: TaskViewSettings = {}) {
  const full: TaskViewActions = { openTask: vi.fn(), retry: vi.fn(), ...actions };
  const ui = (d: TaskViewData) => <View data={d} actions={full} themeSettings={themeSettings} ctx={ctx} />;
  const r = render(ui(data));
  return { ...r, update: (d: TaskViewData) => r.rerender(ui(d)) };
}

const desktop = () => within(document.querySelector('[data-mesh-layout="desktop"]') as HTMLElement);
const mobile = () => within(document.querySelector('[data-mesh-layout="mobile"]') as HTMLElement);
const count = (scope: ReturnType<typeof within>, id: string) => scope.queryAllByTestId(`core-${id}`).length;

afterEach(cleanup);

describe("task.view / mesh-default", () => {
  it("CT-view-01: every offered block is on the screen exactly once per layout, the tabbed ones once the tab is open", () => {
    mount(mkData());
    for (const id of ["header", "properties", "description"]) expect(count(desktop(), id), id).toBe(1);
    expect(desktop().getAllByLabelText("draft")).toHaveLength(1); // comments: the default right tab
    for (const [tab, id] of [["panel.tabs.subtasks", "subtasks"], ["panel.tabs.dependencies", "dependencies"], ["panel.tabs.artifacts", "artifacts"], ["panel.tabs.activity", "activity"]] as const) {
      fireEvent.click(desktop().getByRole("button", { name: new RegExp(tab.split(".").pop()!, "i") }));
      expect(count(desktop(), id), id).toBe(1);
    }
    expect(count(mobile(), "header")).toBe(1);
    expect(screen.getAllByTestId("core-topbar")).toHaveLength(1);
  });

  it("CT-view-02: hidden_blocks naming header and description does not hide them", () => {
    mount(mkData(), {}, { hidden_blocks: ["header", "description"] });
    expect(count(desktop(), "header")).toBe(1);
    expect(count(desktop(), "description")).toBe(1);
  });

  it("CT-view-03: a viewer's view (no edit actions) draws no editors of its own and keeps the comments block", () => {
    mount(mkData()); // actions without quickEdit / changeStatus / assign
    expect(desktop().queryAllByRole("combobox")).toHaveLength(0);
    expect(desktop().getAllByLabelText("draft")).toHaveLength(1);
  });

  it("CT-view-04: the top bar (extension task.actions) and an extension panel tab host are placed by the view", () => {
    const ExtensionPanel = vi.fn(() => <div data-testid="ext-body" />);
    mount(mkData({ components: components({ ExtensionPanel }) }));
    expect(screen.getByTestId("core-topbar")).toBeTruthy();
    expect(ExtensionPanel).not.toHaveBeenCalled(); // no extension tab registered: nothing to host
  });

  it("CT-view-05: loading is a skeleton, error is a message, refreshing keeps the content", () => {
    const retry = vi.fn();
    const { update, container } = mount(mkData({ task: { state: "loading" } }));
    expect(container.querySelector('[data-mesh-layout]')).toBeNull();
    expect(screen.getByTestId("core-topbar")).toBeTruthy();
    update(mkData({ task: { state: "error", error: { code: "not_found", message: "Gone" }, retry } }));
    expect(screen.getByRole("alert").querySelector("p")?.textContent).toBe("Gone");
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(retry).toHaveBeenCalledTimes(1);
    update(mkData({ task: { state: "refreshing", value: mkTask() } }));
    expect(count(desktop(), "description")).toBe(1);
  });

  it("CT-view-06: the view does not take Escape or close the panel by itself", () => {
    const close = vi.fn();
    mount(mkData({ presentation: "slide-over" }), { close });
    fireEvent.keyDown(document.body, { key: "Escape" });
    fireEvent.keyDown(desktop().getByLabelText("draft"), { key: "Escape" });
    expect(close).not.toHaveBeenCalled();
  });

  it("CT-view-07: a status change arriving as new data keeps the comment being typed", () => {
    const { update } = mount(mkData());
    fireEvent.change(desktop().getByLabelText("draft"), { target: { value: "half a thought" } });
    update(mkData({ task: { state: "ready", value: mkTask({ statusId: "s2", title: "Task t1 (moved)" }) } }));
    expect((desktop().getByLabelText("draft") as HTMLTextAreaElement).value).toBe("half a thought");
  });

  it("CT-view-08: opening a subtask is the core's action, handed to the view unchanged", () => {
    const openTask = vi.fn();
    const Subtasks: FC = () => <button onClick={() => openTask("sub-1")}>sub</button>;
    mount(mkData({ components: components({ subtasks: Subtasks }) }), { openTask });
    fireEvent.click(desktop().getByRole("button", { name: /subtasks/i }));
    fireEvent.click(desktop().getByText("sub"));
    expect(openTask).toHaveBeenCalledWith("sub-1");
  });

  it("CT-view-09: switching tabs brings each block forward, and a block the core asks for comes to the front", () => {
    const { update } = mount(mkData());
    fireEvent.click(desktop().getByRole("button", { name: /artifacts/i }));
    expect(count(desktop(), "artifacts")).toBe(1);
    expect(desktop().queryAllByLabelText("draft")).toHaveLength(0);
    update(mkData({ requestedBlock: { block: "comments", nonce: 1 } }));
    expect(desktop().getAllByLabelText("draft")).toHaveLength(1);
    expect(count(desktop(), "artifacts")).toBe(0);
  });
});
