import { readFileSync } from "node:fs";
// Contract tests CT-board-01..18 of the slot catalog (Mesh Doc "slots-board" §6), run against
// the `mesh-default` views through a harness that does what the board container does:
// real primitives (Draggable/DropZone/Can/InlineEdit/TaskLink/useRealtime), the transport
// (task API, WebSocket) is the only thing replaced.
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { useCallback, useMemo, useState } from "react";

const apiMock = vi.fn();
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), api: (...a: unknown[]) => apiMock(...a) }));

import { CoreActionsProvider, DndProvider, useRealtime } from "@/core/primitives";
import type { MoveRequest, RealtimeEvent } from "@/core/primitives";
import { useTaskStore } from "@/stores/task";
import { useAgentStore } from "@/stores/agent";
import { useMemberStore } from "@/stores/member";
import { useWebSocketStore } from "@/stores/websocket";
import { useWorkspaceStore } from "@/stores/workspace";
import { resetSlotRegistryForTests } from "@/lib/extension-slots";
import { registerMeshDefaultTheme } from "@/themes/mesh-default";
import { resolveBoardComponents } from "../board-host";
import { buildFieldCatalog, resolveCardFields } from "../board-data";
import type {
  Async,
  BoardCardView,
  BoardColumnData,
  BoardColumnView,
  BoardGroupBy,
  BoardLayoutActions,
  BoardLayoutView,
  SlotContext,
  TaskData,
} from "../types";
import { resolveSlotView } from "../registry";

registerMeshDefaultTheme();

const ctx: SlotContext = { slot: "board.layout", contractVersion: "1.0.0", locale: "en", mode: "light", isMobile: false, workspace: null };

export const mkTask = (id: string, over: Partial<TaskData> = {}): TaskData => ({
  id, shortId: id.slice(0, 8), projectId: "p1", title: `Task ${id}`, hasDescription: false, statusId: "s",
  statusCategory: "todo", priority: "none", assignee: null, reviewer: null, checkedOutBy: null, labels: [],
  dueDate: null, overdue: false, estimatedHours: null, customFields: {}, counts: { subtasks: 0, artifacts: 0, vcsLinks: 0 },
  vcsProviders: [], vcsOwnedProviders: [], pipelineStatus: null, gate: null, runtimeState: null, delegationLevel: null,
  updatedAt: "2026-01-01T00:00:00Z", vcsProvidersKnown: true, checkout: null, recurring: null, falseOpen: null, ...over,
});

export const mkColumn = (id: string, tasks: TaskData[], over: Partial<BoardColumnData> = {}): BoardColumnData => ({
  id, kind: "status", title: id, color: "#888", tasks, totalCount: tasks.length, countLabel: String(tasks.length),
  wipLimit: null, isClosed: false, hasMore: false, loadingMore: false,
  status: { id, name: id, slug: id, color: "#888", category: "todo", position: 0 }, ...over,
});

const fixtureColumns = () => [
  mkColumn("backlog", [mkTask("t1"), mkTask("t2")]),
  mkColumn("progress", [mkTask("t3")]),
  mkColumn("done", []),
];

// jsdom has no layout: place each column on a grid so dnd-kit can find the neighbours.
// `vertical` stacks the columns instead of putting them side by side.
function stubLayout(vertical = false) {
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
    const el = this as HTMLElement;
    const zone = el.closest("[data-column-id]") as HTMLElement | null;
    const cols = Array.from(document.querySelectorAll("[data-column-id]"));
    const ci = zone ? cols.indexOf(zone) : 0;
    const isZone = el.hasAttribute("data-column-id");
    const row = isZone ? 0 : Array.from(zone?.querySelectorAll("[data-task-id]") ?? []).indexOf(el.closest("[data-task-id]") as Element);
    const x = vertical ? 0 : ci * 300;
    const y = (vertical ? ci * 400 : 0) + (isZone ? 0 : 20 + Math.max(row, 0) * 60);
    return { x, y, left: x, top: y, width: 200, height: isZone ? 300 : 50, right: x + 200, bottom: y + (isZone ? 300 : 50), toJSON() {} } as DOMRect;
  });
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type Spy = Mock<(...args: any[]) => any>;
export interface Spies {
  moveTask: Spy;
  openTask: Spy;
  quickEdit: Spy;
  loadMore: Spy;
  retry: Spy;
}

interface MountOpts {
  view?: BoardLayoutView;
  columns?: BoardColumnData[];
  role?: "member" | "viewer";
  groupBy?: BoardGroupBy;
  state?: "ready" | "loading" | "error" | "refreshing";
  dndReason?: "mobile_readonly";
  fieldKeys?: string[];
  moveRejects?: boolean;
}

function Harness({ opts, spies }: { opts: MountOpts; spies: Spies }) {
  const [columns, setColumns] = useState(opts.columns ?? fixtureColumns());
  const catalog = useMemo(
    () => buildFieldCatalog([{ id: "f1", project_id: "p1", name: "Sprint", slug: "sprint", field_type: "number", description: "", options: {}, default_value: null, is_required: false, is_visible_to_agents: true, position: 0, created_at: "" }], (k) => k),
    [],
  );
  const cardFields = useMemo(() => resolveCardFields(catalog, opts.fieldKeys).map((f) => f.key), [catalog, opts.fieldKeys]);
  const components = useMemo(() => resolveBoardComponents(), []);

  // What the container does with a realtime event: the changed task gets a new object, the rest keep theirs.
  useRealtime(
    { kind: "project", id: "p1" },
    useCallback((e: RealtimeEvent) => {
      if (e.type !== "task.updated") return;
      const { id, title } = e.data as { id: string; title: string };
      setColumns((cols) => cols.map((c) => (c.tasks.some((t) => t.id === id) ? { ...c, tasks: c.tasks.map((t) => (t.id === id ? { ...t, title } : t)) } : c)));
    }, []),
  );

  const groupBy = opts.groupBy ?? "status";
  const dndOn = groupBy === "status" && opts.role !== "viewer" && !opts.dndReason;
  const actions = useMemo<BoardLayoutActions>(
    () => ({
      moveTask: (...a) => spies.moveTask(...a),
      openTask: (...a) => spies.openTask(...a),
      quickEdit: (...a) => spies.quickEdit(...a),
      createTask: opts.role === "viewer" ? undefined : () => {},
      loadMore: (...a) => spies.loadMore(...a),
      retry: () => spies.retry(),
    }),
    [spies],
  );
  const coreActions = useMemo(
    () => ({ openTask: (id: string) => spies.openTask(id), quickEdit: (id: string, field: string, value: unknown) => spies.quickEdit(id, { field, value }) }),
    [spies],
  );
  const state = opts.state ?? "ready";
  const asyncColumns: Async<BoardColumnData[]> =
    state === "loading" ? { state: "loading" }
    : state === "error" ? { state: "error", error: { code: "x", message: "Could not load the board" }, retry: () => spies.retry() }
    : state === "refreshing" ? { state: "refreshing", value: columns }
    : { state: "ready", value: columns };
  const View = opts.view ?? resolveSlotView("board.layout");
  // The container's moveTask reports a refusal itself (toast) and resolves; the view never sees the rejection.
  const onMove = async (m: MoveRequest) => {
    if (m.to.kind === "status") await Promise.resolve(spies.moveTask(m.item.id, m.to.id, { index: m.index })).catch(() => {});
  };

  return (
    <CoreActionsProvider actions={coreActions as never}>
      <DndProvider enabled={dndOn} onMove={onMove}>
        <View
          data={{
            project: { id: "p1", slug: "proj", name: "P" }, groupBy, sortBy: "manual", columns: asyncColumns,
            dnd: { enabled: dndOn, reason: opts.dndReason ?? (groupBy !== "status" ? "group_by" : opts.role === "viewer" ? "no_permission" : undefined), nonDroppableColumnIds: columns.filter((c) => c.kind !== "status").map((c) => c.id) },
            availableFields: catalog, cardFields, components,
          }}
          actions={actions}
          themeSettings={{}}
          ctx={ctx}
        />
      </DndProvider>
    </CoreActionsProvider>
  );
}

export function mountBoard(opts: MountOpts = {}) {
  useMemberStore.setState({ myRole: opts.role ?? "member" });
  const spies: Spies = {
    moveTask: vi.fn(opts.moveRejects ? () => Promise.reject(new Error("refused")) : async () => {}),
    openTask: vi.fn(), quickEdit: vi.fn(async () => {}), loadMore: vi.fn(async () => {}), retry: vi.fn(),
  };
  const utils = render(
    <MemoryRouter initialEntries={["/ws/proj"]}>
      <Routes><Route path="/:wsSlug/:projectSlug" element={<Harness opts={opts} spies={spies} />} /></Routes>
    </MemoryRouter>,
  );
  return { spies, ...utils };
}

const card = (id: string) => document.querySelector(`[data-testid="task-card"][data-task-id="${id}"]`) as HTMLElement;
const tick = () => act(async () => { await new Promise((r) => setTimeout(r, 30)); });

/** Pick up with Space, move one zone with an arrow, drop with Space. */
async function keyboardMove(id: string, arrow: "ArrowRight" | "ArrowDown") {
  const el = card(id);
  el.focus();
  fireEvent.keyDown(el, { code: "Space", key: " " });
  await tick();
  fireEvent.keyDown(document, { code: arrow, key: arrow });
  await tick();
  fireEvent.keyDown(document, { code: "Space", key: " " });
  await tick();
}

beforeEach(() => { apiMock.mockReset(); apiMock.mockResolvedValue({ extensions: [] }); stubLayout(); });
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

describe("board.* contract on mesh-default", () => {
  it("CT-board-01: dragging a card to another column calls moveTask once with the right target", async () => {
    const { spies } = mountBoard();
    const el = card("t1");
    fireEvent.pointerDown(el, { button: 0, isPrimary: true, pointerId: 1, clientX: 10, clientY: 30 });
    for (let i = 1; i <= 8; i++) fireEvent.pointerMove(document, { isPrimary: true, pointerId: 1, clientX: 10 + i * 40, clientY: 30 });
    await tick();
    fireEvent.pointerUp(document, { isPrimary: true, pointerId: 1, clientX: 330, clientY: 30 });
    await tick();
    expect(spies.moveTask).toHaveBeenCalledTimes(1);
    expect(spies.moveTask.mock.calls[0]![0]).toBe("t1");
    expect(spies.moveTask.mock.calls[0]![1]).toBe("progress");
    expect(typeof spies.moveTask.mock.calls[0]![2].index).toBe("number");
  });

  it("CT-board-02: without task.move the card has no handle and cannot be moved", async () => {
    const { spies } = mountBoard({ role: "viewer" });
    const el = card("t1");
    expect(el.hasAttribute("aria-roledescription")).toBe(false);
    expect(el.hasAttribute("tabindex")).toBe(false);
    fireEvent.pointerDown(el, { button: 0, isPrimary: true, pointerId: 1, clientX: 10, clientY: 30 });
    for (let i = 1; i <= 8; i++) fireEvent.pointerMove(document, { isPrimary: true, pointerId: 1, clientX: 10 + i * 40, clientY: 30 });
    fireEvent.pointerUp(document, { isPrimary: true, pointerId: 1, clientX: 330, clientY: 30 });
    await tick();
    expect(spies.moveTask).not.toHaveBeenCalled();
  });

  it("CT-board-03: a realtime event updates the card in place, the others keep their order and DOM node", async () => {
    mountBoard();
    const before = card("t1");
    const second = card("t2");
    expect(screen.getByText("Task t1")).toBeTruthy();
    act(() => {
      useWebSocketStore.setState({ lastEvent: { type: "task.updated", channel: "project:p1", data: { id: "t1", title: "Новый" }, timestamp: "1" } as never });
    });
    await waitFor(() => expect(screen.getByText("Новый")).toBeTruthy());
    expect(card("t1")).toBe(before);
    const ids = Array.from(document.querySelectorAll('[data-testid="task-card"]')).map((e) => e.getAttribute("data-task-id"));
    expect(ids).toEqual(["t1", "t2", "t3"]);
    expect(card("t2")).toBe(second);
  });

  it("CT-board-04: keyboard move: pick up, arrow, drop calls moveTask; Escape cancels", async () => {
    const { spies } = mountBoard();
    await keyboardMove("t1", "ArrowRight");
    expect(spies.moveTask).toHaveBeenCalledTimes(1);
    expect(spies.moveTask.mock.calls[0]!.slice(0, 2)).toEqual(["t1", "progress"]);
    expect(document.querySelector('[aria-live]')?.textContent).toMatch(/t1|Task t1|dropped/i);

    const el = card("t3");
    el.focus();
    fireEvent.keyDown(el, { code: "Space", key: " " });
    await tick();
    fireEvent.keyDown(document, { code: "ArrowRight", key: "ArrowRight" });
    await tick();
    fireEvent.keyDown(document, { code: "Escape", key: "Escape" });
    await tick();
    expect(spies.moveTask).toHaveBeenCalledTimes(1);
  });

  it("CT-board-05: grouped by priority nothing is draggable, by pointer or keyboard", async () => {
    const { spies } = mountBoard({ groupBy: "priority", columns: [mkColumn("priority-high", [mkTask("t1")], { kind: "group", status: undefined, group: { priority: "high" } }), mkColumn("priority-low", [], { kind: "group", status: undefined, group: { priority: "low" } })] });
    expect(card("t1").hasAttribute("tabindex")).toBe(false);
    await keyboardMove("t1", "ArrowRight");
    expect(spies.moveTask).not.toHaveBeenCalled();
  });

  it("CT-board-06: a gated card sits once, in Triage, with its real status; nothing can be dropped into Triage", async () => {
    const gated = mkTask("g1", { gate: { class: "hard", reason: "sign-off" } });
    const triage = mkColumn("triage", [gated], { kind: "triage", status: { id: "triage", name: "Triage", slug: "triage", color: "#888", category: "triage", position: 0 }, gateStatusNameByTask: { g1: "In progress" } });
    const { spies } = mountBoard({ columns: [triage, mkColumn("backlog", [mkTask("t1")])] });
    expect(document.querySelectorAll('[data-task-id="g1"]').length).toBe(1);
    expect(screen.getByTestId("human-gate-status").textContent).toMatch(/In progress/);
    await keyboardMove("t1", "ArrowRight"); // t1 is in the 2nd column: ArrowRight has no neighbour; use ArrowLeft below
    fireEvent.keyDown(card("t1"), { code: "Space", key: " " });
    await tick();
    fireEvent.keyDown(document, { code: "ArrowLeft", key: "ArrowLeft" });
    await tick();
    fireEvent.keyDown(document, { code: "Space", key: " " });
    await tick();
    expect(JSON.stringify(spies.moveTask.mock.calls)).not.toContain("triage");
  });

  it("CT-board-07: a closed column with more cards loads on request; the button is off while loading", async () => {
    const { spies, unmount } = mountBoard({ columns: [mkColumn("done", [mkTask("d1")], { isClosed: true, hasMore: true, totalCount: 9, countLabel: "9", closedSlice: { loaded: 1, total: 9, filtersActive: false } })] });
    fireEvent.click(screen.getByRole("button", { name: /load more/i }));
    expect(spies.loadMore).toHaveBeenCalledWith("done");
    unmount();
    mountBoard({ columns: [mkColumn("done", [mkTask("d1")], { isClosed: true, hasMore: true, loadingMore: true, closedSlice: { loaded: 1, total: 9, filtersActive: false } })] });
    expect((within(document.body).getByTestId("closed-slice").querySelector("button") as HTMLButtonElement).disabled).toBe(true);
  });

  it("CT-board-08: loading shows a skeleton, error shows a message and a working retry, refreshing keeps the cards", () => {
    const loading = mountBoard({ state: "loading" });
    expect(document.querySelector(".animate-pulse")).not.toBeNull();
    loading.unmount();
    const err = mountBoard({ state: "error" });
    fireEvent.click(screen.getByRole("button", { name: /retry/i }));
    expect(err.spies.retry).toHaveBeenCalled();
    expect(screen.getByRole("alert").textContent).toMatch(/Could not load/);
    err.unmount();
    mountBoard({ state: "refreshing" });
    expect(card("t1")).not.toBeNull();
    expect(screen.getAllByRole("status").length).toBeGreaterThan(0);
  });

  it("CT-board-09: a status column takes drops (CT-board-01), a Triage column does not (CT-board-06); each carries its id", () => {
    mountBoard();
    expect(document.querySelectorAll('[data-testid="board-column"][data-status-id]').length).toBe(3);
    expect(document.querySelectorAll("[data-mesh-dropzone]").length).toBe(3);
    cleanup();
    const triage = mkColumn("triage", [mkTask("g1", { gate: { class: "hard", reason: "r" } })], { kind: "triage", status: { id: "triage", name: "Triage", slug: "triage", color: "#888", category: "triage", position: 0 } });
    mountBoard({ columns: [triage, mkColumn("backlog", [mkTask("t1")])] });
    const zones = [...document.querySelectorAll("[data-mesh-dropzone]")].map((z) => z.getAttribute("data-mesh-dropzone"));
    expect(zones).toHaveLength(1); // the Triage column is a passive list, not a drop zone
    expect(zones[0]).toContain("backlog");
  });

  it("CT-board-10: only the chosen fields are drawn, in the chosen order; an empty custom field does not break", () => {
    mountBoard({
      fieldKeys: ["priority", "due_date", "cf:sprint"],
      columns: [mkColumn("backlog", [
        mkTask("t1", { priority: "high", dueDate: "2030-01-01T00:00:00Z", labels: ["x-label"], assignee: { id: "u", type: "user", name: "Zed" }, customFields: { sprint: 5 } }),
        mkTask("t2", { priority: "high", customFields: {} }),
      ])],
    });
    const t1 = card("t1");
    expect(t1.textContent).not.toMatch(/x-label/);
    expect(t1.querySelector('[title="Unassigned"], [class*="rounded-full"][class*="select-none"]')).toBeNull();
    const rowTexts = Array.from(t1.querySelectorAll(":scope > div")).map((d) => d.textContent ?? "");
    expect(rowTexts.some((t) => /Sprint:\s*5/.test(t))).toBe(true);
    expect(card("t2").querySelector('[data-field="cf:sprint"]')).toBeNull();
  });

  it("CT-board-11: overdue comes from the data, the view never recomputes it", () => {
    mountBoard({ columns: [mkColumn("backlog", [mkTask("late", { dueDate: "2000-01-01T00:00:00Z", overdue: true }), mkTask("fine", { dueDate: "2000-01-01T00:00:00Z", overdue: false })])] });
    expect(card("late").querySelector(".text-tone-danger-500")).not.toBeNull();
    expect(card("fine").querySelector(".text-tone-danger-500")).toBeNull();
  });

  it("CT-board-12: the title is a real link; a plain click opens the task once, a ctrl-click is left to the browser", () => {
    const { spies } = mountBoard();
    const link = within(card("t1")).getByRole("link");
    expect(link.getAttribute("href")).toContain("t1");
    fireEvent.click(link);
    expect(spies.openTask).toHaveBeenCalledTimes(1);
    expect(spies.openTask.mock.calls[0]![0]).toBe("t1");
    fireEvent.click(link, { ctrlKey: true });
    expect(spies.openTask).toHaveBeenCalledTimes(1);
    fireEvent.click(card("t2").firstElementChild as HTMLElement);
    expect(spies.openTask).toHaveBeenCalledTimes(2);
  });

  it("CT-board-13: the title is not editable in place and the card has no edit button", () => {
    const { unmount } = mountBoard();
    expect(card("t1").querySelector("[data-inline-edit]")).toBeNull();
    expect(card("t1").querySelector("button")).toBeNull();
    unmount();
  });

  it("CT-board-14: a provider-owned task.badge replaces the generic VCS icon on the card; disabled, the generic icon stays", async () => {
    const registry = (enabled: boolean) =>
      apiMock.mockResolvedValue({ extensions: [{ id: "gitlab", name: "GitLab", status: { enabled }, manifest: { capabilities: { ui: ["task.badge"] } } }] });
    const gitlabTask = () => mkTask("g1", { vcsProviders: ["gitlab"], counts: { subtasks: 0, artifacts: 0, vcsLinks: 1 } });
    resetSlotRegistryForTests();
    useWorkspaceStore.setState({ currentWorkspace: { id: "ws-1" } as never });
    registry(true);
    const on = mountBoard({ columns: [mkColumn("backlog", [gitlabTask()])] });
    expect(await screen.findByTestId("slot-badge-gitlab")).toBeTruthy();
    on.unmount();
    resetSlotRegistryForTests();
    registry(false);
    mountBoard({ columns: [mkColumn("backlog", [gitlabTask()])] });
    await tick();
    expect(screen.queryByTestId("slot-badge-gitlab")).toBeNull();
    expect(card("g1").querySelector('[aria-label]:not([aria-label="Unassigned"])')).not.toBeNull();
  });

  it("CT-board-20: the generic VCS icon follows data.vcsOwnedProviders, not extension state", () => {
    const { Card } = resolveBoardComponents();
    const draw = (owned: ("github" | "gitlab")[]) =>
      render(
        <MemoryRouter>
          <Card data={{ task: mkTask("o1", { vcsProviders: ["gitlab"], vcsProvidersKnown: true, vcsOwnedProviders: owned, counts: { subtasks: 0, artifacts: 0, vcsLinks: 1 } }), column: { id: "c", kind: "status" }, fields: [], selected: false, isOverlay: false }} actions={{ openTask: () => {} }} themeSettings={{}} ctx={ctx} />
        </MemoryRouter>,
      );
    const first = draw([]);
    expect(screen.queryByLabelText(/hasVcsLinks|VCS/i)).toBeTruthy();
    first.unmount();
    draw(["gitlab"]);
    expect(screen.queryByLabelText(/hasVcsLinks|VCS/i)).toBeNull();
  });

  it("CT-board-19: the card view draws task.badge from props.data alone, with the task store empty", async () => {
    apiMock.mockResolvedValue({ extensions: [{ id: "gitlab", name: "GitLab", status: { enabled: true }, manifest: { capabilities: { ui: ["task.badge"] } } }] });
    resetSlotRegistryForTests();
    useWorkspaceStore.setState({ currentWorkspace: { id: "ws-1" } as never });
    useTaskStore.setState({ tasks: [], tasksById: {} } as never);
    const { Card } = resolveBoardComponents();
    render(
      <MemoryRouter>
        <Card data={{ task: mkTask("b1", { vcsProviders: ["gitlab"], counts: { subtasks: 0, artifacts: 0, vcsLinks: 1 } }), column: { id: "c", kind: "status" }, fields: [], selected: false, isOverlay: false }} actions={{ openTask: () => {} }} themeSettings={{}} ctx={ctx} />
      </MemoryRouter>,
    );
    expect(await screen.findByTestId("slot-badge-gitlab")).toBeTruthy();
    // the view must not reach for a global store at all (props.data / props.actions / themeSettings only)
    const src = readFileSync("src/themes/mesh-default/board/card.tsx", "utf8");
    expect(src).not.toMatch(/@\/stores\//);
    // The only exception to "props only" is the host of the task.badge insertion point (decision of the product lead, 2026-10-21):
    // exactly `SlotBadges` may come from the extension host; any other store, extension or slot hook/import fails here.
    const extImports = [...src.matchAll(/import\s*\{([^}]*)\}\s*from\s*"@\/components\/extensions\/[^"]*"/g)]
      .flatMap((m) => (m[1] ?? "").split(",").map((n) => n.trim()).filter(Boolean));
    expect(extImports).toEqual(["SlotBadges"]);
    expect(src).not.toMatch(/\buse(Slot\w*|\w*Store|\w*Extension\w*)\s*\(/);
    // Second named exception (decision of the product lead, 2026-10-21): `useTranslation` (ambient i18n) in every board view.
    for (const f of ["card", "column", "layout"]) {
      const v = readFileSync(`src/themes/mesh-default/board/${f}.tsx`, "utf8");
      expect(v).not.toMatch(/@\/stores\//);
      expect(v).not.toMatch(/\b(AssigneeAvatar|AgentShortTag)\b(?!View)/); // the store-backed variants subscribe to stores
      // spec v4: the contract's own primitives are imported from core/primitives only (never from a store-backed component path)
      expect(v).not.toMatch(/from\s*"@\/components\/(task-card|inline-edit|task-link|dnd)[^"]*"/);
      expect(v).not.toMatch(/from\s*"@\/i18n"/); // the global i18n singleton is not an exception
      expect(v).not.toMatch(/\buse(Slot\w*|\w*Store|\w*Extension\w*)\s*\(/);
      const i18n = [...v.matchAll(/import\s*\{([^}]*)\}\s*from\s*"react-i18next"/g)]
        .flatMap((m) => (m[1] ?? "").split(",").map((n) => n.trim()).filter(Boolean));
      expect(i18n.filter((n) => n !== "useTranslation")).toEqual([]);
    }
  });

  it("CT-board-20: the card view takes agent short tags from props.data; a store change alone does not move them", () => {
    const { Card } = resolveBoardComponents();
    const agent = { id: "a1", type: "agent" as const, name: "Alice" };
    const draw = (shortTag?: string) => (
      <MemoryRouter>
        <Card data={{ task: mkTask("s1", { assignee: { ...agent, shortTag } }), column: { id: "c", kind: "status" }, fields: [{ key: "assignee", kind: "person", label: "Assignee", source: "builtin" as const }], selected: false, isOverlay: false }} actions={{ openTask: () => {} }} themeSettings={{}} ctx={ctx} />
      </MemoryRouter>
    );
    useAgentStore.setState({ agents: [{ id: "a1", short_tag: "BEFORE" }] } as never);
    const view = render(draw("PROPS"));
    expect(screen.getByLabelText("Alice · PROPS")).toBeTruthy();
    act(() => useAgentStore.setState({ agents: [{ id: "a1", short_tag: "AFTER" }] } as never));
    expect(screen.getByLabelText("Alice · PROPS")).toBeTruthy();
    expect(screen.queryByLabelText("Alice · AFTER")).toBeNull();
    view.rerender(draw("NEXT"));
    expect(screen.getByLabelText("Alice · NEXT")).toBeTruthy();
    useAgentStore.setState({ agents: [] } as never);
  });

  it("CT-board-21: the column header takes the assignee short tag from props.data; a store change alone does not move it", () => {
    const col = (shortTag: string) => mkColumn("assignee-a1", [], { kind: "group", status: undefined, group: { assignee: { id: "a1", type: "agent", name: "Alice", shortTag } } });
    const comps = resolveBoardComponents();
    const draw = (shortTag: string) => (
      <MemoryRouter>
        <DndProvider enabled={false} onMove={async () => {}}>
          <comps.Column data={{ column: col(shortTag), dnd: { enabled: false }, fields: [], components: comps } as never} actions={{ openTask: () => {}, loadMore: async () => {} } as never} themeSettings={{}} ctx={ctx} />
        </DndProvider>
      </MemoryRouter>
    );
    useAgentStore.setState({ agents: [{ id: "a1", short_tag: "BEFORE" }] } as never);
    const view = render(draw("PROPS"));
    expect(screen.getAllByLabelText("Alice · PROPS").length).toBeGreaterThan(0);
    act(() => useAgentStore.setState({ agents: [{ id: "a1", short_tag: "AFTER" }] } as never));
    expect(screen.queryByLabelText("Alice · AFTER")).toBeNull();
    expect(view.container.textContent).not.toContain("AFTER");
    view.rerender(draw("NEXT"));
    expect(screen.getAllByLabelText("Alice · NEXT").length).toBeGreaterThan(0);
    useAgentStore.setState({ agents: [] } as never);
  });

  it("CT-board-15: the drag preview has no drag handle and no handlers", () => {
    const { Card } = resolveBoardComponents();
    render(
      <MemoryRouter>
        <Card data={{ task: mkTask("o1"), column: { id: "c", kind: "status" }, fields: [], selected: false, isOverlay: true }} actions={{ openTask: () => {} }} themeSettings={{}} ctx={ctx} />
      </MemoryRouter>,
    );
    expect(document.querySelector('[data-testid="task-card"]')).toBeNull();
    expect(document.querySelector("[aria-roledescription]")).toBeNull();
    expect(screen.getByText("Task o1")).toBeTruthy();
  });

  it("CT-board-16: a refused move does not break the view; the card stays", async () => {
    mountBoard({ moveRejects: true });
    await keyboardMove("t1", "ArrowRight").catch(() => {});
    expect(card("t1")).not.toBeNull();
  });

  it("CT-board-21: the add-task button follows actions.createTask (absent without task.create)", () => {
    mountBoard({ role: "member" });
    expect(screen.queryAllByTitle(/addTask|add task/i).length).toBeGreaterThan(0);
    cleanup();
    mountBoard({ role: "viewer" });
    expect(screen.queryAllByTitle(/addTask|add task/i)).toHaveLength(0);
  });

  it("CT-board-17: the column count is totalCount, not the number of loaded cards", () => {
    mountBoard({ columns: [mkColumn("done", [mkTask("d1")], { totalCount: 42, countLabel: "42", isClosed: true })] });
    expect(screen.getByTestId("column-count").textContent).toBe("42");
  });

  it("CT-board-22: a column re-render with equal inputs does not re-render its cards", () => {
    const renders: string[] = [];
    const Counting: BoardCardView = (props) => {
      renders.push(props.data.task.id);
      return <div data-testid="counted" />;
    };
    const Column = resolveSlotView("board.column") as BoardColumnView;
    const tasks = [mkTask("t1"), mkTask("t2")];
    const column = mkColumn("backlog", tasks);
    const open = () => {};
    const themeSettings = {};
    const fields: never[] = []; // the layout memoizes the field list, the container the tasks and ctx
    const shell = () => (
      <MemoryRouter>
        <DndProvider enabled onMove={async () => {}}>
          {/* fresh data/actions objects each pass, same content: what a container re-render hands down */}
          <Column
            data={{ column: { ...column }, index: 0, total: 1, dnd: { enabled: true, nonDroppableColumnIds: [] }, fields, components: { Card: Counting } }}
            actions={{ loadMore: async () => {}, openTask: open, moveTask: async () => {} }}
            themeSettings={themeSettings}
            ctx={ctx}
          />
        </DndProvider>
      </MemoryRouter>
    );
    const { rerender } = render(shell());
    expect(renders).toEqual(["t1", "t2"]);
    rerender(shell());
    expect(renders).toEqual(["t1", "t2"]);
  });

  it("CT-board-18: on a phone the board stays readable; with dnd.reason mobile_readonly there are no handles", () => {
    mountBoard({ dndReason: "mobile_readonly" });
    expect(card("t1")).not.toBeNull();
    expect(card("t1").hasAttribute("tabindex")).toBe(false);
  });
});

// A theme layout that stacks the columns vertically, drawn through the same container: see board-stub-layout.test.tsx.
export { stubLayout, keyboardMove, card as cardEl };
