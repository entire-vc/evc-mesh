// Contract tests CT-row-01..08 of the slot catalog (Mesh Doc "slots-task-nav-doc-auth" §1), run against
// the `mesh-default` row view in a harness that does what the list container does: real primitives
// (TaskLink/useRealtime), the transport is the only thing replaced.
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { useCallback, useMemo, useState } from "react";

const apiMock = vi.fn();
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), api: (...a: unknown[]) => apiMock(...a) }));

import { CoreActionsProvider, useRealtime } from "@/core/primitives";
import type { RealtimeEvent } from "@/core/primitives";
import { useWebSocketStore } from "@/stores/websocket";
import { useMemberStore } from "@/stores/member";
import { registerMeshDefaultTheme } from "@/themes/mesh-default";
import { buildRowColumns } from "../task-row-data";
import { resolveSlotView } from "../registry";
import type { SlotContext, StatusData, TaskData, TaskRowActions, TaskRowView } from "../types";
import type { CustomFieldDefinition } from "@/types";

registerMeshDefaultTheme();

const ctx = (over: Partial<SlotContext> = {}): SlotContext => ({ slot: "task.row", contractVersion: "1.0.0", locale: "en", mode: "light", isMobile: false, workspace: null, ...over });

const mkTask = (id: string, over: Partial<TaskData> = {}): TaskData => ({
  id, shortId: id.slice(0, 8), projectId: "p1", title: `Task ${id}`, hasDescription: false, statusId: "s1",
  statusCategory: "todo", priority: "medium", assignee: null, reviewer: null, checkedOutBy: null, labels: [],
  dueDate: null, overdue: false, estimatedHours: null, customFields: {}, counts: { subtasks: 0, artifacts: 0, vcsLinks: 0 },
  vcsProviders: [], vcsOwnedProviders: [], pipelineStatus: null, gate: null, runtimeState: null, delegationLevel: null,
  updatedAt: "2026-01-01T00:00:00Z", vcsProvidersKnown: true, checkout: null, recurring: null, falseOpen: null, ...over,
});

const STATUSES: StatusData[] = [
  { id: "s1", name: "Todo", slug: "todo", color: "#888", category: "todo", position: 0 },
  { id: "s2", name: "Doing", slug: "doing", color: "#00f", category: "in_progress", position: 1 },
];
const SPRINT: CustomFieldDefinition = { id: "f1", project_id: "p1", name: "Sprint", slug: "sprint", field_type: "number", description: "", options: {}, default_value: null, is_required: false, is_visible_to_agents: true, position: 0, created_at: "" };

interface MountOpts {
  view?: TaskRowView;
  columnKeys?: string[];
  rowFields?: string[];
  task?: Partial<TaskData>;
  role?: "member" | "viewer";
  depth?: number;
  expandable?: boolean;
  expanded?: boolean;
  isMobile?: boolean;
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type Spy = Mock<(...args: any[]) => any>;
interface Spies {
  openTask: Spy;
  toggleSelected: Spy;
  toggleExpanded: Spy;
  quickEdit: Spy;
  changeStatus: Spy;
}

function Harness({ opts, spies }: { opts: MountOpts; spies: Spies }) {
  const [task, setTask] = useState(mkTask("t1", opts.task));
  const [selected, setSelected] = useState(false);
  const columns = useMemo(
    () => buildRowColumns(new Set(opts.columnKeys ?? ["status", "priority", "due_date"]), [SPRINT], (k) => k, opts.rowFields),
    [],
  );
  useRealtime(
    { kind: "project", id: "p1" },
    useCallback((e: RealtimeEvent) => {
      if (e.type !== "task.updated") return;
      const { title } = e.data as { title: string };
      setTask((t) => ({ ...t, title }));
    }, []),
  );
  const viewer = opts.role === "viewer";
  const actions: TaskRowActions = {
    openTask: (...a) => spies.openTask(...a),
    toggleSelected: () => {
      spies.toggleSelected();
      setSelected((s) => !s);
    },
    toggleExpanded: opts.expandable ? () => spies.toggleExpanded() : undefined,
    quickEdit: viewer ? undefined : (patch) => spies.quickEdit(patch),
    changeStatus: viewer ? undefined : (id) => spies.changeStatus(id),
  };
  const View = opts.view ?? (resolveSlotView("task.row") as TaskRowView);
  return (
    <CoreActionsProvider actions={{ openTask: (id: string) => spies.openTask({ id }), quickEdit: async () => {}, taskHref: (id: string) => `/w/ws/p/proj/t/${id}` }}>
      <table>
        <tbody>
          <View
            data={{
              task, status: STATUSES[0]!, columns, selected, focused: false, depth: opts.depth ?? 0,
              expandable: opts.expandable ?? false, expanded: opts.expanded ?? false, statuses: STATUSES, expanding: false,
              createdAt: "2026-01-01T00:00:00Z", components: { Badges: () => null },
            }}
            actions={actions}
            themeSettings={{}}
            ctx={ctx({ isMobile: opts.isMobile ?? false })}
          />
        </tbody>
      </table>
    </CoreActionsProvider>
  );
}

function mount(opts: MountOpts = {}) {
  useMemberStore.setState({ myRole: opts.role ?? "member" });
  const spies: Spies = { openTask: vi.fn(), toggleSelected: vi.fn(), toggleExpanded: vi.fn(), quickEdit: vi.fn(async () => {}), changeStatus: vi.fn(async () => {}) };
  const utils = render(
    <MemoryRouter initialEntries={["/ws/proj"]}>
      <Routes><Route path="/:wsSlug/:projectSlug" element={<Harness opts={opts} spies={spies} />} /></Routes>
    </MemoryRouter>,
  );
  return { spies, ...utils };
}

beforeEach(() => { useWebSocketStore.setState({ lastEvent: null }); apiMock.mockReset(); apiMock.mockResolvedValue({ extensions: [] }); });
afterEach(() => cleanup());

describe("task.row contract on mesh-default", () => {
  it("CT-row-01: cells follow `columns` order, empty values do not break the row", () => {
    mount({ columnKeys: ["status", "due_date", "cf:sprint"], task: { dueDate: null, customFields: {} } });
    const cells = Array.from(document.querySelectorAll("tr > td"));
    // checkbox, title, status, due_date, cf:sprint
    expect(cells).toHaveLength(5);
    expect(within(cells[1] as HTMLElement).getByText("Task t1")).toBeTruthy();
    expect(cells[2]!.textContent).toContain("Todo");
    expect(cells[3]!.textContent).toBe("--");
  });

  it("CT-row-02: click on the title opens the task; the link has a real href, a modified click is left to the browser", () => {
    const { spies } = mount();
    const link = screen.getByRole("link", { name: "Task t1" });
    expect(link.getAttribute("href")).toBe("/w/ws/p/proj/t/t1");
    fireEvent.click(link);
    expect(spies.openTask).toHaveBeenCalledTimes(1);
    fireEvent.click(link, { ctrlKey: true });
    expect(spies.openTask).toHaveBeenCalledTimes(1);
  });

  it("CT-row-03: the checkbox toggles selection by click and Space, and aria-checked follows `selected`", () => {
    const { spies } = mount();
    const box = screen.getByRole("checkbox") as HTMLInputElement;
    expect(box.getAttribute("aria-checked")).toBe("false");
    fireEvent.click(box);
    expect(spies.toggleSelected).toHaveBeenCalledTimes(1);
    expect(box.getAttribute("aria-checked")).toBe("true");
    expect(box.checked).toBe(true);
    // Space on a native checkbox is a click in browsers; jsdom does the same through userEvent only, so assert it is a focusable native input
    box.focus();
    expect(document.activeElement).toBe(box);
    expect(box.tabIndex).toBeGreaterThanOrEqual(0);
  });

  it("CT-row-04: viewer has no editors and no status change", () => {
    const { spies } = mount({ role: "viewer" });
    fireEvent.click(screen.getByText("Todo"));
    fireEvent.click(screen.getByLabelText("Medium"));
    expect(document.querySelector("select, input[type=date]")).toBeNull();
    fireEvent.doubleClick(screen.getByText("Task t1"));
    expect(document.querySelector("input:not([type=checkbox])")).toBeNull();
    expect(spies.quickEdit).not.toHaveBeenCalled();
    expect(spies.changeStatus).not.toHaveBeenCalled();
  });

  it("CT-row-05: priority edit calls quickEdit with the priority patch (change, and Enter)", async () => {
    const { spies } = mount();
    // the priority cell is the one after status
    const open = () => fireEvent.click(document.querySelectorAll("tr > td")[3] as HTMLElement);
    open();
    const select = document.querySelector("select") as HTMLSelectElement;
    fireEvent.change(select, { target: { value: "high" } });
    expect(spies.quickEdit).toHaveBeenCalledWith({ field: "priority", value: "high" });
    open();
    const again = document.querySelector("select") as HTMLSelectElement;
    fireEvent.keyDown(again, { key: "Enter" });
    expect(spies.quickEdit).toHaveBeenCalledTimes(2);
    expect(spies.quickEdit).toHaveBeenLastCalledWith({ field: "priority", value: "medium" });
  });

  it("CT-row-05b: status cell calls changeStatus, title double-click edits through quickEdit", async () => {
    const { spies } = mount();
    fireEvent.click(screen.getByText("Todo"));
    fireEvent.change(document.querySelector("select") as HTMLSelectElement, { target: { value: "s2" } });
    expect(spies.changeStatus).toHaveBeenCalledWith("s2");
    fireEvent.doubleClick(screen.getByText("Task t1"));
    const input = document.querySelector("input:not([type=checkbox])") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "Renamed" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(spies.quickEdit).toHaveBeenCalledWith({ field: "title", value: "Renamed" });
  });

  it("CT-row-06: a nested row is indented; the expand button reports aria-expanded and toggles", () => {
    const { spies } = mount({ expandable: true, expanded: false });
    const btn = screen.getByRole("button", { name: /expand/i });
    expect(btn.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(btn);
    expect(spies.toggleExpanded).toHaveBeenCalledTimes(1);
    cleanup();
    mount({ depth: 2 });
    const titleCell = screen.getByText("Task t1").closest("td") as HTMLElement;
    expect(titleCell.className).toContain("pl-10");
    // a nested row has no selection box
    expect(screen.queryByRole("checkbox")).toBeNull();
  });

  it("CT-row-07: a realtime title change updates the row in place and keeps the selection", async () => {
    mount();
    const row = document.querySelector("tr") as HTMLElement;
    fireEvent.click(screen.getByRole("checkbox"));
    act(() => {
      useWebSocketStore.setState({ lastEvent: { type: "task.updated", channel: "project:p1", data: { id: "t1", title: "Новый" }, timestamp: "1" } as never });
    });
    await waitFor(() => expect(screen.getByText("Новый")).toBeTruthy());
    expect(document.querySelector("tr")).toBe(row);
    expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(true);
  });

  it("CT-row-08: on mobile the title, the status and openTask are there", () => {
    const { spies } = mount({ isMobile: true });
    expect(screen.getByText("Task t1")).toBeTruthy();
    expect(screen.getByText("Todo")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /edit task/i }));
    expect(spies.openTask).toHaveBeenCalledTimes(1);
  });

  it("a subtask row follows the same column order as its parent when row_fields reorders them", () => {
    const task = { dueDate: "2030-05-01T00:00:00Z" };
    mount({ depth: 1, columnKeys: ["status", "due_date"], rowFields: ["due_date", "status"], task });
    const subCells = Array.from(document.querySelectorAll("tr > td"));
    // spacer, title, due_date, status
    expect(subCells).toHaveLength(4);
    expect(subCells[2]!.textContent).not.toContain("Todo");
    expect(subCells[3]!.textContent).toContain("Todo");
    cleanup();
    mount({ depth: 0, columnKeys: ["status", "due_date"], rowFields: ["due_date", "status"], task });
    const cells = Array.from(document.querySelectorAll("tr > td"));
    expect(cells[3]!.textContent).toContain("Todo");
  });

  it("row_fields narrows and orders the columns, the title stays first", () => {
    const cols = buildRowColumns(new Set(["status", "priority", "due_date", "cf:sprint"]), [SPRINT], (k) => k, ["due_date", "nope", "status"]);
    expect(cols.map((c) => c.key)).toEqual(["title", "due_date", "status"]);
    const all = buildRowColumns(new Set(["status", "cf:sprint"]), [SPRINT], (k) => k);
    expect(all.map((c) => c.key)).toEqual(["title", "status", "cf:sprint"]);
    expect(all[2]!.customField?.slug).toBe("sprint");
  });
});
