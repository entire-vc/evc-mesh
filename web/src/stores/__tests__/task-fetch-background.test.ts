import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api", () => ({ api: vi.fn() }));
import { api } from "@/lib/api";
import { useTaskStore } from "@/stores/task";
import type { Task } from "@/types";

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;
const task = (id: string) =>
  ({ id, project_id: "p", status_id: "s", title: id, labels: [], position: 0 }) as unknown as Task;
const page = (items: Task[]) => ({ items, page: 1, per_page: 200, has_more: false, total: items.length });

beforeEach(() => {
  mockedApi.mockReset();
  useTaskStore.setState({ tasks: [task("a")], tasksById: {}, tasksByStatus: {}, isLoading: false });
});

describe("fetchTasks — background refresh (WS event)", () => {
  it("never raises isLoading, so the board is not remounted into skeletons", async () => {
    const seen: boolean[] = [];
    const unsub = useTaskStore.subscribe((s) => seen.push(s.isLoading));
    mockedApi.mockResolvedValue(page([task("a"), task("b")]));
    await useTaskStore.getState().fetchTasks("p", undefined, { background: true });
    unsub();
    expect(seen).not.toContain(true);
    expect(useTaskStore.getState().tasks.map((t) => t.id)).toEqual(["a", "b"]);
  });

  it("a failed background refresh keeps the visible tasks", async () => {
    mockedApi.mockRejectedValue(new Error("boom"));
    await useTaskStore.getState().fetchTasks("p", undefined, { background: true });
    expect(useTaskStore.getState().tasks.map((t) => t.id)).toEqual(["a"]);
  });

  it("positive control: a normal fetch does raise isLoading", async () => {
    const seen: boolean[] = [];
    const unsub = useTaskStore.subscribe((s) => seen.push(s.isLoading));
    mockedApi.mockResolvedValue(page([task("a")]));
    await useTaskStore.getState().fetchTasks("p");
    unsub();
    expect(seen).toContain(true);
  });

  it("a successful background refresh does not write isLoading at all", async () => {
    useTaskStore.setState({ isLoading: true });
    mockedApi.mockResolvedValue(page([task("a")]));
    await useTaskStore.getState().fetchTasks("p", undefined, { background: true });
    expect(useTaskStore.getState().isLoading).toBe(true);
  });

  it("a failed background refresh does not write isLoading either", async () => {
    useTaskStore.setState({ isLoading: true });
    mockedApi.mockRejectedValue(new Error("boom"));
    await useTaskStore.getState().fetchTasks("p", undefined, { background: true });
    expect(useTaskStore.getState().isLoading).toBe(true);
  });

  it("a background refresh started during a foreground fetch does not strand isLoading", async () => {
    let release!: (v: unknown) => void;
    mockedApi.mockImplementationOnce(() => new Promise((r) => (release = r)));
    const fg = useTaskStore.getState().fetchTasks("p");
    mockedApi.mockRejectedValueOnce(new Error("bg boom"));
    await useTaskStore.getState().fetchTasks("p", undefined, { background: true });
    release(page([task("a"), task("b")]));
    await fg;
    expect(useTaskStore.getState().isLoading).toBe(false);
    expect(useTaskStore.getState().tasks.map((x) => x.id)).toEqual(["a", "b"]);
  });
});
