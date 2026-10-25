import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
vi.mock("@/lib/api", async (orig) => {
  const real = await orig<typeof import("@/lib/api")>();
  return { ...real, api: vi.fn(async (path: string) => {
    if (/\/projects\/p1\/tasks$/.test(path)) return { items: TASKS, total_count: 2, page: 1, per_page: 200, has_more: false };
    if (/\/projects\/p1\/statuses$/.test(path)) return STATUSES;
    if (/custom-fields/.test(path)) return [];
    return { items: [] };
  }) };
});
import { Profiler, type ReactNode } from "react";
const commits: Record<string, number> = {};
vi.mock("@/lib/perf-profiler", () => ({ PERF_PROFILER: true, PerfProfiler: ({ id, children }: { id: string; children: ReactNode }) => <Profiler id={id} onRender={() => { commits[id] = (commits[id] ?? 0) + 1; }}>{children}</Profiler> }));
import { BoardPage } from "@/pages/board";
import { registerMeshDefaultTheme } from "@/themes/mesh-default";
import { resolveSlotView } from "../registry";
import type { BoardCardView, TaskData } from "../types";
import { useProjectStore } from "@/stores/project";
import { useTaskStore } from "@/stores/task";
import type { Task, TaskStatus } from "@/types";
const mkStatus = (id: string, position: number): TaskStatus => ({ id, project_id: "p1", name: id, slug: id, color: "#000", position, category: "todo", is_default: position === 0, auto_transition: {} });
const STATUSES = [mkStatus("s1", 0), mkStatus("s2", 1)];
const mkTask = (id: string, status = "s1"): Task => ({ id, project_id: "p1", title: `T ${id}`, status_id: status, position: 1, priority: "medium", labels: [] }) as unknown as Task;
const TASKS = [mkTask("t1"), mkTask("t2", "s2")];
// Opening a task from a board card must not re-render the cards: the perf gate (task.open, board_card_commits <= 0) counts it.
// The title primitives (TaskLink/InlineEdit) used to read router context and to open the editor on the same click.
describe("board card commits on task open", () => {
  beforeEach(() => { useProjectStore.setState({ currentProject: { id: "p1", slug: "proj", name: "P" } as never, statuses: STATUSES }); useTaskStore.setState({ tasks: [], tasksByStatus: {} }); });
  afterEach(cleanup);
  it("a title click opens the task without a card commit and without opening the inline editor", async () => {
    render(<MemoryRouter initialEntries={["/ws/proj"]}><Routes><Route path="/:wsSlug/:projectSlug/*" element={<BoardPage />} /><Route path="/:wsSlug/:projectSlug/t/:id" element={<div>task page</div>} /></Routes></MemoryRouter>);
    await screen.findAllByTestId("task-card");
    await act(async () => { await new Promise((r) => setTimeout(r, 100)); });
    const before = commits["board-card"] ?? 0;
    await act(async () => { const a = document.querySelector('[data-testid="task-card"] a') as HTMLElement; fireEvent.click(a); await new Promise((r) => setTimeout(r, 100)); });
    expect(before).toBeGreaterThan(0);
    expect((commits["board-card"] ?? 0) - before).toBe(0);
    expect(document.querySelector("[data-inline-edit-open]")).toBeNull();
  });
  it("a modified click on the title (new tab) does not open the inline editor either", async () => {
    render(<MemoryRouter initialEntries={["/ws/proj"]}><Routes><Route path="/:wsSlug/:projectSlug/*" element={<BoardPage />} /></Routes></MemoryRouter>);
    await screen.findAllByTestId("task-card");
    await act(async () => { fireEvent.click(document.querySelector('[data-testid="task-card"] a') as HTMLElement, { ctrlKey: true }); await new Promise((r) => setTimeout(r, 50)); });
    expect(document.querySelector("[data-inline-edit-open]")).toBeNull();
  });
  it("a board card has no edit pencil and no inline title editor; a title click opens the task", async () => {
    render(<MemoryRouter initialEntries={["/ws/proj"]}><Routes><Route path="/:wsSlug/:projectSlug/*" element={<BoardPage />} /><Route path="/:wsSlug/:projectSlug/t/:id" element={<div>task page</div>} /></Routes></MemoryRouter>);
    const cards = await screen.findAllByTestId("task-card");
    await act(async () => { await new Promise((r) => setTimeout(r, 100)); });
    expect(cards[0]?.querySelector("button")).toBeNull();
    expect(cards[0]?.querySelector("[data-inline-edit]")).toBeNull();
    await act(async () => { fireEvent.click(cards[0]?.querySelector("a") as HTMLElement); await new Promise((r) => setTimeout(r, 100)); });
    expect(document.querySelector("[data-inline-edit-open]")).toBeNull();
  });
  it("the drag overlay card is not profiled as a board card (board.drag counts one commit more otherwise)", () => {
    registerMeshDefaultTheme();
    const Card = resolveSlotView("board.card") as BoardCardView;
    const task: TaskData = { id: "o1", shortId: "o1", projectId: "p1", title: "Overlay", hasDescription: false, statusId: "s", statusCategory: "todo", priority: "none", assignee: null, reviewer: null, checkedOutBy: null, labels: [], dueDate: null, overdue: false, estimatedHours: null, customFields: {}, counts: { subtasks: 0, artifacts: 0, vcsLinks: 0 }, vcsProviders: [], vcsOwnedProviders: [], pipelineStatus: null, gate: null, runtimeState: null, delegationLevel: null, updatedAt: "2026-01-01T00:00:00Z", vcsProvidersKnown: true, checkout: null, recurring: null, falseOpen: null };
    const before = commits["board-card"] ?? 0;
    render(
      <MemoryRouter>
        <Card data={{ task, column: { id: "c", kind: "status" }, fields: [], selected: false, isOverlay: true }} actions={{ openTask: () => {} }} themeSettings={{}} ctx={{ slot: "board.card", contractVersion: "1.0.0", locale: "en", mode: "light", isMobile: false, workspace: null }} />
      </MemoryRouter>,
    );
    expect(screen.getByText("Overlay")).toBeTruthy();
    expect((commits["board-card"] ?? 0) - before).toBe(0);
  });
});
