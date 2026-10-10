import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api", () => ({ api: vi.fn() }));
import { api } from "@/lib/api";
import { __resetLocalMovesForTest, useTaskStore } from "@/stores/task";
import type { Task } from "@/types";

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;

const task = (id: string, status_id: string, position: number) =>
  ({ id, project_id: "p1", status_id, title: id, labels: [], position }) as unknown as Task;

function deferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function seed(tasks: Task[]) {
  useTaskStore.setState({
    tasks,
    tasksById: Object.fromEntries(tasks.map((t) => [t.id, t])),
    tasksByStatus: {},
  });
  useTaskStore.getState().groupByStatus();
}

const columnIds = (statusId: string) =>
  (useTaskStore.getState().tasksByStatus[statusId] ?? []).map((t) => t.id);

beforeEach(() => {
  mockedApi.mockReset();
  __resetLocalMovesForTest();
  seed([task("a", "todo", 1), task("b", "todo", 2), task("c", "doing", 1)]);
});

/*
 * A board drop used to wait for POST /move before touching local state: for
 * the whole round trip the card was drawn back in its source column, then
 * jumped to the target when the answer landed. How much of that the user (and
 * the perf-counters board.drag gate) saw depended only on server latency.
 */
describe("moveTask — local state moves before the server answers", () => {
  it("the card is in the target column while POST /move is still in flight", async () => {
    const pending = deferred<unknown>();
    mockedApi.mockReturnValue(pending.promise);

    const call = useTaskStore.getState().moveTask("a", { status_id: "doing", position: 2 });

    expect(columnIds("todo")).toEqual(["b"]);
    expect(columnIds("doing")).toEqual(["c", "a"]);
    expect(useTaskStore.getState().tasksById.a!.status_id).toBe("doing");

    const shownBeforeAnswer = useTaskStore.getState().tasksByStatus;
    pending.resolve({});
    await call;
    expect(columnIds("doing")).toEqual(["c", "a"]);
    // The answer changes nothing on screen, so it must not cost a re-render.
    expect(useTaskStore.getState().tasksByStatus).toBe(shownBeforeAnswer);
  });

  it("a refused move puts the card back where it was and still rejects", async () => {
    const pending = deferred<unknown>();
    mockedApi.mockReturnValue(pending.promise);

    const call = useTaskStore.getState().moveTask("a", { status_id: "doing", position: 2 });
    expect(columnIds("doing")).toEqual(["c", "a"]);

    pending.reject(new Error("409 review gate"));
    await expect(call).rejects.toThrow("409 review gate");

    expect(columnIds("todo")).toEqual(["a", "b"]);
    expect(columnIds("doing")).toEqual(["c"]);
    expect(useTaskStore.getState().tasksById.a).toMatchObject({ status_id: "todo", position: 1 });
  });

  it("rollback restores only the moved card, not an update that landed meanwhile", async () => {
    const pending = deferred<unknown>();
    mockedApi.mockReturnValue(pending.promise);

    const call = useTaskStore.getState().moveTask("a", { status_id: "doing", position: 2 });
    // A live update to another card arrives while the move is in flight.
    useTaskStore.setState((s) => ({
      tasks: s.tasks.map((t) => (t.id === "b" ? { ...t, title: "renamed" } : t)),
    }));

    pending.reject(new Error("boom"));
    await expect(call).rejects.toThrow("boom");

    expect(useTaskStore.getState().tasks.find((t) => t.id === "b")!.title).toBe("renamed");
    expect(columnIds("todo")).toEqual(["a", "b"]);
  });

  it("positive control: a move without status_id still calls the API and changes nothing locally", async () => {
    mockedApi.mockResolvedValue({});
    await useTaskStore.getState().moveTask("a", { position: 5 } as never);
    expect(mockedApi).toHaveBeenCalledWith("/api/v1/tasks/a/move", {
      method: "POST",
      body: { position: 5 },
    });
    expect(columnIds("todo")).toEqual(["a", "b"]);
  });
});

const page = (items: Task[]) => ({ items, page: 1, per_page: 200, has_more: false, total: items.length });

/*
 * The same snap-back, by another road (authed-e2e job 160044): a background
 * refetch (WS status_change -> debounced fetchTasks) that the server answered
 * from its pre-move state, landing after the drop.
 */
describe("moveTask — a task list fetched before the move was confirmed", () => {
  it("does not put the card back in its source column", async () => {
    const list = deferred<ReturnType<typeof page>>();
    const move = deferred<unknown>();
    mockedApi.mockImplementation((url: string) =>
      url.endsWith("/move") ? move.promise : list.promise,
    );

    const fetching = useTaskStore.getState().fetchTasks("p1");
    const moving = useTaskStore.getState().moveTask("a", { status_id: "doing", position: 2 });
    move.resolve({});
    await moving;
    // The server read the list before it committed the move.
    list.resolve(page([task("a", "todo", 1), task("b", "todo", 2), task("c", "doing", 1)]));
    await fetching;

    expect(columnIds("doing")).toEqual(["c", "a"]);
    expect(columnIds("todo")).toEqual(["b"]);
  });

  it("positive control: a list fetched after the move was confirmed is the truth", async () => {
    mockedApi.mockResolvedValueOnce({});
    await useTaskStore.getState().moveTask("a", { status_id: "doing", position: 2 });
    // Someone else moved it back afterwards; the newer server state wins.
    mockedApi.mockResolvedValueOnce(page([task("a", "todo", 1), task("b", "todo", 2), task("c", "doing", 1)]));
    await useTaskStore.getState().fetchTasks("p1");

    expect(columnIds("todo")).toEqual(["a", "b"]);
  });
});

/*
 * Two moves of the same card in flight at once (drop, then drop again before
 * the first answer). Each refusal must fall back to the newest move still in
 * flight, else to the last state the server accepted — never to the other
 * move's optimistic destination (codex-review on !1145).
 */
describe("moveTask — overlapping moves of one card", () => {
  function twoMoves() {
    const first = deferred<unknown>();
    const second = deferred<unknown>();
    mockedApi.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    const a = useTaskStore.getState().moveTask("a", { status_id: "doing", position: 2 });
    const b = useTaskStore.getState().moveTask("a", { status_id: "done", position: 1 });
    return { first, second, a, b };
  }

  it("both refused: the card goes back where the server last had it", async () => {
    const { first, second, a, b } = twoMoves();
    expect(columnIds("done")).toEqual(["a"]);

    first.reject(new Error("first refused"));
    await expect(a).rejects.toThrow("first refused");
    expect(columnIds("done")).toEqual(["a"]);

    second.reject(new Error("second refused"));
    await expect(b).rejects.toThrow("second refused");
    expect(columnIds("todo")).toEqual(["a", "b"]);
    expect(columnIds("doing")).toEqual(["c"]);
    expect(columnIds("done")).toEqual([]);
  });

  it("the later one refused, the earlier accepted: the card shows the accepted move", async () => {
    const { first, second, a, b } = twoMoves();
    second.reject(new Error("second refused"));
    await expect(b).rejects.toThrow("second refused");
    // The first is still in flight: show it, not the pre-drag column.
    expect(columnIds("doing")).toEqual(["c", "a"]);

    first.resolve({});
    await a;
    expect(columnIds("doing")).toEqual(["c", "a"]);
  });

  it("the earlier one refused after the later was accepted: the later move stays", async () => {
    const { first, second, a, b } = twoMoves();
    second.resolve({});
    await b;
    first.reject(new Error("first refused"));
    await expect(a).rejects.toThrow("first refused");
    expect(columnIds("done")).toEqual(["a"]);
  });
});
