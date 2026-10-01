import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api", () => ({ api: vi.fn() }));
import { api } from "@/lib/api";
import { useTaskStore } from "@/stores/task";
import type { Task } from "@/types";

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;

const task = (id: string, project_id: string, status_id: string) =>
  ({ id, project_id, status_id, title: id, labels: [], position: 0 }) as unknown as Task;

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}
const page = (items: Task[]) => ({ items, page: 1, per_page: 200, has_more: false, total: items.length });

beforeEach(() => {
  mockedApi.mockReset();
  useTaskStore.setState({ tasks: [], tasksById: {}, tasksByStatus: {} });
});

describe("fetchTasks — answer of a project the user already left", () => {
  it("a slow Lab response arriving after the Keep one does not overwrite the Keep list", async () => {
    const lab = deferred<ReturnType<typeof page>>();
    const keep = deferred<ReturnType<typeof page>>();
    mockedApi.mockImplementation((url: string) =>
      url.includes("p-lab") ? lab.promise : keep.promise,
    );

    const labCall = useTaskStore.getState().fetchTasks("p-lab");
    const keepCall = useTaskStore.getState().fetchTasks("p-keep");
    keep.resolve(page([task("k1", "p-keep", "keep-prog")]));
    await keepCall;
    lab.resolve(page([task("l1", "p-lab", "lab-prog")]));
    await labCall;

    expect(useTaskStore.getState().tasks.map((t) => t.project_id)).toEqual(["p-keep"]);
    expect(Object.keys(useTaskStore.getState().tasksByStatus)).toEqual(["keep-prog"]);
  });

  it("positive control: the newest request is still applied", async () => {
    mockedApi.mockResolvedValue(page([task("k1", "p-keep", "keep-prog")]));
    await useTaskStore.getState().fetchTasks("p-keep");
    expect(useTaskStore.getState().tasks).toHaveLength(1);
  });
});
