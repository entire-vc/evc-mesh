import { beforeEach, describe, expect, it, vi } from "vitest";
// mock: external boundary — the HTTP API; the store and pagination run unchanged.
vi.mock("@/lib/api", () => ({ api: vi.fn() }));
import { api } from "@/lib/api";
import { useTaskStore } from "@/stores/task";
import type { Task } from "@/types";

const http = vi.mocked(api);
const PROJECT = "contenthub-fixture";
const fixture = Array.from({ length: 331 }, (_, i) => ({
  id: `task-${i}`, project_id: PROJECT, title: `Task ${i}`, labels: [],
  status_id: i < 282 ? "done" : "todo", priority: "medium", position: i,
}) as unknown as Task);
const page = (items: Task[], n: number, revision = 47, total = 331) => ({
  items, page: n, page_size: 200, total_count: total,
  has_more: n === 1 && total > 200, list_revision: revision,
});
const fetchAll = (project = PROJECT) =>
  useTaskStore.getState().fetchTasks(project, undefined, { allPages: true });
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => { resolve = r; });
  return { promise, resolve };
}
function serve(items = fixture, revision = 47) {
  http.mockImplementation(async (_url, options) => {
    const n = Number(options?.params?.page ?? 1);
    const size = Number(options?.params?.page_size ?? 200);
    return { ...page(items.slice((n - 1) * size, n * size), n, revision, items.length),
      has_more: n * size < items.length, page_size: size };
  });
}
beforeEach(() => {
  http.mockReset();
  useTaskStore.setState({ tasks: [], tasksById: {}, tasksByStatus: {},
    listProjectId: null, listComplete: false, listError: null });
});

describe("complete project snapshots", () => {
  it("keeps Done 282 and page-two cards after four non-Done tasks move to the front", async () => {
    serve();
    await fetchAll();
    expect(useTaskStore.getState().tasksByStatus.done).toHaveLength(282);
    expect(useTaskStore.getState().tasks.some((t) => t.id === "task-281")).toBe(true);
    const updated = [...fixture.slice(282, 286), ...fixture.slice(0, 282), ...fixture.slice(286)];
    serve(updated, 48);
    await fetchAll();
    expect(useTaskStore.getState().tasksByStatus.done).toHaveLength(282);
    expect(new Set(useTaskStore.getState().tasks.map((t) => t.id)).size).toBe(331);
    expect(useTaskStore.getState().listComplete).toBe(true);
    expect(http.mock.calls.every(([, options]) => options?.params?.include_description === "false")).toBe(true);
    expect(http.mock.calls.some(([, options]) => options?.params?.list_revision === 48)).toBe(true);
  });

  it("a background WS refresh of a complete board keeps isLoading off and Done 282", async () => {
    serve();
    await fetchAll();
    const seen: boolean[] = [];
    const unsub = useTaskStore.subscribe((s) => seen.push(s.isLoading));
    await useTaskStore.getState().fetchTasks(PROJECT, undefined, { allPages: true, background: true });
    unsub();
    expect(seen.some(Boolean)).toBe(false);
    expect(useTaskStore.getState().tasksByStatus.done).toHaveLength(282);
    expect(useTaskStore.getState().listComplete).toBe(true);
  });

  it("a failed background refresh keeps the complete board and raises listError", async () => {
    serve();
    await fetchAll();
    http.mockReset();
    http.mockRejectedValue(new Error("boom"));
    await useTaskStore.getState().fetchTasks(PROJECT, undefined, { allPages: true, background: true });
    expect(useTaskStore.getState().listError).toBe("boom");
    expect(useTaskStore.getState().listComplete).toBe(true);
    expect(useTaskStore.getState().tasksByStatus.done).toHaveLength(282);
  });

  it("does not publish a partial page, then restarts a stale revision from page one", async () => {
    const second = deferred<ReturnType<typeof page>>();
    http.mockResolvedValueOnce(page(fixture.slice(0, 200), 1))
      .mockImplementationOnce(() => second.promise);
    const pending = fetchAll();
    await vi.waitFor(() => expect(http).toHaveBeenCalledTimes(2));
    expect(useTaskStore.getState().tasks).toEqual([]);
    expect(useTaskStore.getState().listComplete).toBe(false);
    second.resolve(page(fixture.slice(200), 2, 48));
    serve(fixture, 48);
    await pending;
    expect(useTaskStore.getState().tasks).toHaveLength(331);
    expect(useTaskStore.getState().listError).toBeNull();
  });

  it("recovers from HTTP 410 without mixing revisions", async () => {
    serve(fixture, 48);
    http.mockResolvedValueOnce(page(fixture.slice(0, 200), 1))
      .mockRejectedValueOnce({ status: 410 });
    await fetchAll();
    expect(useTaskStore.getState().tasks).toHaveLength(331);
    expect(http.mock.calls[2]![1]?.params?.page).toBe(1);
    expect(http.mock.calls[2]![1]?.params?.list_revision).toBeUndefined();
  });

  it("retains the last complete snapshot on page-two failure and retry succeeds", async () => {
    serve();
    await fetchAll();
    http.mockResolvedValueOnce(page(fixture.slice(0, 200), 1))
      .mockRejectedValueOnce(new Error("Network unavailable"));
    await fetchAll();
    expect(useTaskStore.getState().tasksByStatus.done).toHaveLength(282);
    expect(useTaskStore.getState().listError).toBe("Network unavailable");
    serve(fixture, 48);
    await fetchAll();
    expect(useTaskStore.getState().listError).toBeNull();
  });

  it.each(["duplicate", "missing"])("rejects %s instead of claiming completeness", async (failure) => {
    serve();
    http.mockImplementation(async (_url, options) => {
      const n = Number(options?.params?.page ?? 1);
      return page(n === 1 ? fixture.slice(0, 200) : failure === "duplicate"
        ? [...fixture.slice(199, 329)] : fixture.slice(200, 330), n,
        47);
    });
    await fetchAll();
    expect(useTaskStore.getState().tasks).toEqual([]);
    expect(useTaskStore.getState().listComplete).toBe(false);
    expect(useTaskStore.getState().listError).toBeTruthy();
    expect(http.mock.calls.length).toBeLessThanOrEqual(6);
  });

  it("loads a never-written project (revision 0) completely without sending a pin", async () => {
    serve(fixture, 0);
    await fetchAll();
    expect(useTaskStore.getState().tasks).toHaveLength(331);
    expect(useTaskStore.getState().listComplete).toBe(true);
    expect(useTaskStore.getState().listError).toBeNull();
    expect(http.mock.calls.every(([, o]) => o?.params?.list_revision === undefined)).toBe(true);
  });

  it("rejects a revision-0 walk when a write bumps the revision mid-walk", async () => {
    http.mockResolvedValueOnce(page(fixture.slice(0, 200), 1, 0))
      .mockResolvedValue(page(fixture.slice(200), 2, 1));
    await fetchAll();
    expect(useTaskStore.getState().listComplete).toBe(false);
    expect(useTaskStore.getState().listError).toBeTruthy();
  });

  it("checks the revision again after the last page before publishing", async () => {
    serve(fixture, 48);
    http.mockResolvedValueOnce(page(fixture.slice(0, 200), 1))
      .mockResolvedValueOnce(page(fixture.slice(200), 2))
      .mockRejectedValueOnce({ status: 410 });
    await fetchAll();
    expect(useTaskStore.getState().tasks).toHaveLength(331);
    expect(http.mock.calls[2]![1]?.params?.list_revision).toBe(47);
    expect(http.mock.calls[3]![1]?.params?.list_revision).toBeUndefined();
  });

  it("ignores old page two after switching projects", async () => {
    const second = deferred<ReturnType<typeof page>>();
    http.mockResolvedValueOnce(page(fixture.slice(0, 200), 1))
      .mockImplementationOnce(() => second.promise);
    const old = fetchAll();
    await vi.waitFor(() => expect(http).toHaveBeenCalledTimes(2));
    http.mockResolvedValueOnce(page([{ ...fixture[0]!, project_id: "new" }], 1, 50, 1));
    await fetchAll("new");
    second.resolve(page(fixture.slice(200), 2));
    await old;
    expect(useTaskStore.getState().tasks.map((t) => t.project_id)).toEqual(["new"]);
    expect(useTaskStore.getState().listProjectId).toBe("new");
    expect(http).toHaveBeenCalledTimes(3);
  });

  it("preserves the explicit single-page List view contract", async () => {
    serve();
    await useTaskStore.getState().fetchTasks(PROJECT, { page: 2, page_size: 50 });
    expect(useTaskStore.getState().tasks).toHaveLength(50);
    expect(useTaskStore.getState().total).toBe(331);
    expect(useTaskStore.getState().hasMore).toBe(true);
    expect(http).toHaveBeenCalledTimes(1);
    expect(useTaskStore.getState().listComplete).toBe(false);
    // A normal List refresh keeps the current page's derived toolbar options
    // available while its loading skeleton is visible.
    const currentPage = useTaskStore.getState().tasks;
    const next = deferred<ReturnType<typeof page>>();
    http.mockImplementationOnce(() => next.promise);
    const pending = useTaskStore.getState().fetchTasks(PROJECT, { page: 3, page_size: 50 });
    expect(useTaskStore.getState().isLoading).toBe(true);
    expect(useTaskStore.getState().tasks).toBe(currentPage);
    next.resolve(page(fixture.slice(100, 150), 3));
    await pending;
    expect(useTaskStore.getState().tasks[0]?.id).toBe("task-100");
  });
});
