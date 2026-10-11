import { beforeEach, describe, expect, it, vi } from "vitest";
// mock: external boundary — the HTTP API; the store and the board loader run unchanged.
vi.mock("@/lib/api", () => ({ api: vi.fn() }));
import { api } from "@/lib/api";
import { __resetBoardQueueForTest, cancelBoardRefresh, useTaskStore } from "@/stores/task";
import type { StatusCategory, Task } from "@/types";

const http = vi.mocked(api);
const PROJECT = "contenthub-fixture";
type Params = Record<string, string | number | undefined>;

const status = (id: string, category: StatusCategory = id as StatusCategory) => ({ id, category });
const STATUSES = [status("backlog"), status("todo"), status("in_progress"), status("review"),
  status("done"), status("cancelled")];
const mk = (i: number, status_id: string, project_id = PROJECT) =>
  ({ id: `task-${i}`, project_id, title: `Task ${i}`, labels: [], status_id, priority: "medium", position: i }) as unknown as Task;

// ContentHub on 11.10: 331 tasks, 282 of them Done.
const contentHub = Array.from({ length: 331 }, (_, i) => mk(i, i < 282 ? "done" : "todo"));

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => { resolve = r; });
  return { promise, resolve };
}

/** A fake API: `ordered` is newest-updated first, the server's default sort. */
function serve(ordered: Task[], opts: { delay?: Promise<unknown> } = {}) {
  http.mockImplementation(async (_url, options) => {
    if (opts.delay) await opts.delay;
    const p = (options?.params ?? {}) as Params;
    const ids = String(p.status ?? "").split(",").filter(Boolean);
    const rows = ids.length ? ordered.filter((t) => ids.includes(t.status_id)) : ordered;
    const n = Number(p.page ?? 1);
    const size = Number(p.page_size ?? 200);
    return { items: rows.slice((n - 1) * size, n * size), page: n, page_size: size,
      total_count: rows.length, has_more: n * size < rows.length, list_revision: 47 };
  });
}
const calls = () => http.mock.calls.map(([, o]) => (o?.params ?? {}) as Params);
const load = (opts?: { background?: boolean }, project = PROJECT) =>
  useTaskStore.getState().fetchBoardTasks(project, STATUSES, opts);
const byStatus = (id: string) => useTaskStore.getState().tasksByStatus[id] ?? [];

beforeEach(() => {
  http.mockReset();
  __resetBoardQueueForTest();
  useTaskStore.setState({ tasks: [], tasksById: {}, tasksByStatus: {}, isLoading: false,
    listProjectId: null, listComplete: false, listError: null,
    closedSlices: {} });
});

describe("board load: open work complete, closed statuses bounded", () => {
  it("a 4000-task project costs open pages + one page per closed status, no full walk", async () => {
    // 300 open (2 pages), 3600 Done, 100 Cancelled.
    const big = Array.from({ length: 4000 }, (_, i) =>
      mk(i, i < 250 ? "backlog" : i < 300 ? "todo" : i < 3900 ? "done" : "cancelled"));
    serve(big);
    await load();
    // ceil(300 / 200) open pages + 1 Done page + 1 Cancelled page.
    expect(http).toHaveBeenCalledTimes(4);
    const s = useTaskStore.getState();
    expect(s.listComplete).toBe(true);
    expect(byStatus("backlog")).toHaveLength(250);
    expect(byStatus("todo")).toHaveLength(50);
    expect(byStatus("done")).toHaveLength(200);
    expect(s.closedSlices.done).toEqual({ pages: 1, loaded: 200, total: 3600, hasMore: true });
    expect(s.closedSlices.cancelled).toEqual({ pages: 1, loaded: 100, total: 100, hasMore: false });
    for (const p of calls()) {
      expect(p.include_description).toBe("false");
      expect(p.list_revision).toBeUndefined();
      // An empty status filter would be the whole project.
      expect(String(p.status)).not.toBe("");
    }
    // A WS refresh is the same bounded plan, not a walk of 20 pages.
    http.mockClear();
    await load({ background: true });
    expect(http).toHaveBeenCalledTimes(4);
  });

  it("keeps the newest Done cards after four non-Done tasks are updated (ContentHub)", async () => {
    serve(contentHub);
    await load();
    const doneBefore = byStatus("done").map((t) => t.id).sort();
    expect(doneBefore).toHaveLength(200);
    expect(byStatus("todo")).toHaveLength(49);
    // Four Todo tasks become the newest-updated: with one query per closed
    // status they cannot push Done cards out of the Done slice.
    const updated = [...contentHub.slice(282, 286), ...contentHub.slice(0, 282), ...contentHub.slice(286)];
    serve(updated);
    await load({ background: true });
    expect(byStatus("done").map((t) => t.id).sort()).toEqual(doneBefore);
    expect(byStatus("todo")).toHaveLength(49);
    expect(useTaskStore.getState().closedSlices.done!.total).toBe(282);
  });

  it("loads more Done only on demand, and a refresh keeps the pages the user loaded", async () => {
    serve(contentHub);
    await load();
    await useTaskStore.getState().loadMoreClosed(PROJECT, "done");
    expect(byStatus("done")).toHaveLength(282);
    expect(useTaskStore.getState().closedSlices.done).toEqual({ pages: 2, loaded: 282, total: 282, hasMore: false });
    expect(useTaskStore.getState().tasks).toHaveLength(331);
    // Nothing more to load: no request.
    http.mockClear();
    await useTaskStore.getState().loadMoreClosed(PROJECT, "done");
    expect(http).not.toHaveBeenCalled();
    await load({ background: true });
    expect(byStatus("done")).toHaveLength(282);
    expect(calls().filter((p) => p.status === "done").map((p) => p.page)).toEqual([1, 2]);
  });

  it("a failed load-more rejects (the column footer shows it) and keeps the column", async () => {
    serve(contentHub);
    await load();
    http.mockRejectedValueOnce(new Error("Network unavailable"));
    await expect(useTaskStore.getState().loadMoreClosed(PROJECT, "done")).rejects.toThrow("Network unavailable");
    expect(byStatus("done")).toHaveLength(200);
    expect(useTaskStore.getState().closedSlices.done!.pages).toBe(1);
  });

  it("a background WS refresh never raises isLoading", async () => {
    serve(contentHub);
    await load();
    const seen: boolean[] = [];
    const unsub = useTaskStore.subscribe((s) => seen.push(s.isLoading));
    await load({ background: true });
    unsub();
    expect(seen.some(Boolean)).toBe(false);
    expect(useTaskStore.getState().listComplete).toBe(true);
  });

  it("a failed background refresh keeps the board and raises listError", async () => {
    serve(contentHub);
    await load();
    http.mockReset();
    http.mockRejectedValue(new Error("boom"));
    await load({ background: true });
    expect(useTaskStore.getState().listError).toBe("boom");
    expect(useTaskStore.getState().listComplete).toBe(true);
    expect(byStatus("done")).toHaveLength(200);
  });

  it("an initial failure publishes nothing and Retry recovers", async () => {
    http.mockRejectedValue(new Error("Network unavailable"));
    await load();
    expect(useTaskStore.getState().listComplete).toBe(false);
    expect(useTaskStore.getState().tasks).toEqual([]);
    expect(useTaskStore.getState().listError).toBe("Network unavailable");
    serve(contentHub);
    await load();
    expect(useTaskStore.getState().listError).toBeNull();
    expect(useTaskStore.getState().listComplete).toBe(true);
  });

  it("coalesces WS refreshes: N events during a load cost exactly one more load", async () => {
    serve(contentHub);
    await load();
    const gate = deferred<void>();
    serve(contentHub, { delay: gate.promise });
    http.mockClear();
    const first = load({ background: true });
    for (let i = 0; i < 5; i++) await load({ background: true });
    gate.resolve();
    await first;
    // Each load: 1 open page (49 Todo) + Done page + Cancelled page = 3; the
    // in-flight one plus exactly one queued rerun = 6, not 3 × 6.
    await vi.waitFor(() => expect(http).toHaveBeenCalledTimes(6));
    await new Promise((r) => setTimeout(r, 20));
    expect(http).toHaveBeenCalledTimes(6);
  });

  it("re-walks open work once when its total moves between pages", async () => {
    const open = Array.from({ length: 250 }, (_, i) => mk(i, "backlog"));
    let walk = 0;
    http.mockImplementation(async (_url, options) => {
      const p = (options?.params ?? {}) as Params;
      const n = Number(p.page);
      if (p.status !== "backlog,todo,in_progress,review") {
        return { items: [], page: n, page_size: 200, total_count: 0, has_more: false };
      }
      if (n === 1) walk++;
      // First walk: a task is created between page one and page two.
      const total = walk === 1 && n === 2 ? 251 : 250;
      return { items: open.slice((n - 1) * 200, n * 200), page: n, page_size: 200,
        total_count: total, has_more: n * 200 < 250 };
    });
    await load();
    expect(walk).toBe(2);
    expect(byStatus("backlog")).toHaveLength(250);
  });

  it("a project with no open statuses sends no unfiltered request", async () => {
    serve(contentHub);
    await useTaskStore.getState().fetchBoardTasks(PROJECT, [status("done")]);
    expect(calls().map((p) => p.status)).toEqual(["done"]);
    await useTaskStore.getState().fetchBoardTasks(PROJECT, []);
    expect(http).toHaveBeenCalledTimes(1);
    expect(useTaskStore.getState().tasks).toEqual([]);
    expect(useTaskStore.getState().listComplete).toBe(true);
  });

  it("rejects an answer for another project instead of showing it", async () => {
    serve(contentHub.map((t) => ({ ...t, project_id: "someone-else" })));
    await load();
    expect(useTaskStore.getState().listComplete).toBe(false);
    expect(useTaskStore.getState().tasks).toEqual([]);
    expect(useTaskStore.getState().listError).toBeTruthy();
  });

  it("ignores an old project's answer after switching projects", async () => {
    const gate = deferred<void>();
    serve(contentHub, { delay: gate.promise });
    const old = load();
    serve([mk(9000, "todo", "new")]);
    await useTaskStore.getState().fetchBoardTasks("new", STATUSES);
    gate.resolve();
    await old;
    expect(useTaskStore.getState().tasks.map((t) => t.project_id)).toEqual(["new"]);
    expect(useTaskStore.getState().listProjectId).toBe("new");
  });

  it("a load-more that lands during a refresh is not collapsed by it", async () => {
    serve(contentHub);
    await load();
    const gate = deferred<void>();
    http.mockImplementation(async (_url, options) => {
      const p = (options?.params ?? {}) as Params;
      const n = Number(p.page);
      // The refresh (page 1 of every query) waits; the load-more page does not.
      if (n === 1) await gate.promise;
      const ids = String(p.status).split(",");
      const rows = contentHub.filter((t) => ids.includes(t.status_id));
      return { items: rows.slice((n - 1) * 200, n * 200), page: n, page_size: 200,
        total_count: rows.length, has_more: n * 200 < rows.length };
    });
    const refresh = load({ background: true });
    await useTaskStore.getState().loadMoreClosed(PROJECT, "done");
    expect(byStatus("done")).toHaveLength(282);
    gate.resolve();
    await refresh;
    await vi.waitFor(() => expect(calls().filter((p) => p.status === "done" && p.page === 2).length).toBe(2));
    await new Promise((r) => setTimeout(r, 0));
    expect(byStatus("done")).toHaveLength(282);
  });

  it("pages open work oldest first and closed statuses newest first", async () => {
    serve(contentHub);
    await load();
    const open = calls().find((p) => String(p.status).includes("todo"))!;
    expect([open.sort_by, open.sort_dir]).toEqual(["created_at", "asc"]);
    const done = calls().find((p) => p.status === "done")!;
    expect(done.sort_by).toBeUndefined(); // handler default: updated_at desc
  });

  it("re-walks open work when the distinct cards do not add up to the total", async () => {
    const open = Array.from({ length: 250 }, (_, i) => mk(i, "backlog"));
    let walk = 0;
    http.mockImplementation(async (_url, options) => {
      const p = (options?.params ?? {}) as Params;
      const n = Number(p.page);
      if (!String(p.status).includes("backlog")) return { items: [], page: n, page_size: 200, total_count: 0, has_more: false };
      if (n === 1) walk++;
      // First walk: page two repeats a card of page one (a border shift), so
      // one card is never seen although every total agrees.
      const rows = walk === 1 && n === 2 ? [open[0]!, ...open.slice(201, 250)] : open.slice((n - 1) * 200, n * 200);
      return { items: rows, page: n, page_size: 200, total_count: 250, has_more: n === 1 };
    });
    await load();
    expect(walk).toBe(2);
    expect(byStatus("backlog")).toHaveLength(250);
  });

  it("a refresh requests all loaded closed pages together, not one after another", async () => {
    const big = Array.from({ length: 1000 }, (_, i) => mk(i, "done"));
    serve(big);
    await load();
    await useTaskStore.getState().loadMoreClosed(PROJECT, "done");
    await useTaskStore.getState().loadMoreClosed(PROJECT, "done");
    const gate = deferred<void>();
    serve(big, { delay: gate.promise });
    http.mockClear();
    const refresh = load({ background: true });
    await vi.waitFor(() => expect(calls().filter((p) => p.status === "done").map((p) => p.page)).toEqual([1, 2, 3]));
    gate.resolve();
    await refresh;
    expect(useTaskStore.getState().closedSlices.done).toEqual({ pages: 3, loaded: 600, total: 1000, hasMore: true });
  });

  it("a double load-more does not skip a page; one racing a reload is dropped", async () => {
    serve(contentHub);
    await load();
    await Promise.all([
      useTaskStore.getState().loadMoreClosed(PROJECT, "done"),
      useTaskStore.getState().loadMoreClosed(PROJECT, "done"),
    ]);
    expect(useTaskStore.getState().closedSlices.done!.pages).toBe(2);
    expect(byStatus("done")).toHaveLength(282);
    // Load-more answer lands after a fresh foreground load replaced the board.
    useTaskStore.setState({ listProjectId: null, listComplete: false, closedSlices: {} });
    await load();
    expect(useTaskStore.getState().closedSlices.done!.pages).toBe(1);
    const gate = deferred<void>();
    serve(contentHub, { delay: gate.promise });
    const more = useTaskStore.getState().loadMoreClosed(PROJECT, "done");
    useTaskStore.setState({ listProjectId: null, listComplete: false });
    const reload = load(); // non-keepSnapshot: clears closedSlices, then republishes page 1
    gate.resolve();
    await Promise.all([more, reload]);
    expect(useTaskStore.getState().closedSlices.done!.pages).toBe(1);
    expect(byStatus("done")).toHaveLength(200);
  });

  it("a queued refresh reads the statuses when it runs, and an empty list publishes nothing", async () => {
    serve(contentHub);
    await load();
    let current: ReturnType<typeof status>[] = [];
    const gate = deferred<void>();
    serve(contentHub, { delay: gate.promise });
    const first = load({ background: true });
    // Event arrives while the statuses are still empty (e.g. right after a switch).
    await useTaskStore.getState().fetchBoardTasks(PROJECT, () => current, { background: true });
    current = STATUSES;
    http.mockClear();
    gate.resolve();
    await first;
    await vi.waitFor(() => expect(http).toHaveBeenCalledTimes(3));
    // And a background load that does see an empty list keeps the board.
    http.mockClear();
    await useTaskStore.getState().fetchBoardTasks(PROJECT, () => [], { background: true });
    expect(http).not.toHaveBeenCalled();
    expect(byStatus("done")).toHaveLength(200);
  });

  it("a failed statuses request is a board error, not an empty board", async () => {
    await useTaskStore.getState().fetchBoardTasks(PROJECT, Promise.reject(new Error("statuses down")));
    expect(useTaskStore.getState().listComplete).toBe(false);
    expect(useTaskStore.getState().listError).toBe("statuses down");
  });

  it("a queued refresh does not outlive the board", async () => {
    serve(contentHub);
    await load();
    const gate = deferred<void>();
    serve(contentHub, { delay: gate.promise });
    const first = load({ background: true });
    await load({ background: true }); // queued
    cancelBoardRefresh(); // BoardPage unmounted
    http.mockClear();
    gate.resolve();
    await first;
    await new Promise((r) => setTimeout(r, 20));
    expect(http).not.toHaveBeenCalled();
  });

  it("an unchanged closed slice keeps its object across refreshes", async () => {
    serve(contentHub);
    await load();
    const before = useTaskStore.getState().closedSlices.done;
    await load({ background: true });
    expect(useTaskStore.getState().closedSlices.done).toBe(before);
  });

  it("preserves the explicit single-page List view contract", async () => {
    serve(contentHub);
    await useTaskStore.getState().fetchTasks(PROJECT, { page: 2, page_size: 50 });
    expect(useTaskStore.getState().tasks).toHaveLength(50);
    expect(useTaskStore.getState().total).toBe(331);
    expect(useTaskStore.getState().hasMore).toBe(true);
    expect(http).toHaveBeenCalledTimes(1);
    expect(useTaskStore.getState().listComplete).toBe(false);
    // A normal List refresh keeps the current page's derived toolbar options
    // available while its loading skeleton is visible.
    const currentPage = useTaskStore.getState().tasks;
    const gate = deferred<void>();
    serve(contentHub, { delay: gate.promise });
    const pending = useTaskStore.getState().fetchTasks(PROJECT, { page: 3, page_size: 50 });
    expect(useTaskStore.getState().isLoading).toBe(true);
    expect(useTaskStore.getState().tasks).toBe(currentPage);
    gate.resolve();
    await pending;
    expect(useTaskStore.getState().tasks[0]?.id).toBe("task-100");
  });
});
