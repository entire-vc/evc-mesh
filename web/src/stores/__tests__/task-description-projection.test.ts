import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api", () => ({
  api: vi.fn(),
}));
import { api } from "@/lib/api";
import { useTaskStore } from "@/stores/task";
import type { Task } from "@/types";

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;

const PROJECT = "11111111-1111-1111-1111-111111111111";
const TASK = "22222222-2222-2222-2222-222222222222";

function taskFixture(overrides: Partial<Task> = {}): Task {
  return {
    id: TASK,
    project_id: PROJECT,
    status_id: "33333333-3333-3333-3333-333333333333",
    title: "A task",
    assignee_id: null,
    assignee_type: "agent",
    priority: "medium",
    labels: [],
    position: 0,
    created_at: "2026-08-20T00:00:00Z",
    updated_at: "2026-08-20T00:00:00Z",
    ...overrides,
  } as Task;
}

beforeEach(() => {
  mockedApi.mockReset();
  useTaskStore.setState({ tasks: [], tasksById: {}, tasksByStatus: {} });
});

describe("board task list — description projection (#32f4c087)", () => {
  it("asks the API to leave descriptions out", async () => {
    mockedApi.mockResolvedValue({ items: [], total_count: 0, page: 1 });

    await useTaskStore.getState().fetchTasks(PROJECT);

    const options = mockedApi.mock.calls[0]?.[1];
    expect(options.params.include_description).toBe("false");
    // The board's page size is the reason this payload was worth trimming at all.
    expect(options.params.page_size).toBe("200");
  });

  it("keeps a description already known for a task the list no longer carries", async () => {
    // The slide-over renders currentTask.description straight out of this cache
    // while its own single-task fetch is in flight. Overwriting the cached task
    // with the list's description-less copy blanks an open task for that window.
    useTaskStore.setState({
      tasksById: { [TASK]: taskFixture({ description: "the full spec" }) },
    });
    mockedApi.mockResolvedValue({
      items: [taskFixture({ title: "A task, renamed", has_description: true })],
      total_count: 1,
      page: 1,
    });

    await useTaskStore.getState().fetchTasks(PROJECT);

    const cached = useTaskStore.getState().tasksById[TASK]!;
    expect(cached.description).toBe("the full spec");
    // ...while everything the list DID send still wins over the stale copy.
    expect(cached.title).toBe("A task, renamed");
    expect(cached.has_description).toBe(true);
  });

  it("does not invent a description for a task it never had one for", async () => {
    mockedApi.mockResolvedValue({
      items: [taskFixture({ has_description: false })],
      total_count: 1,
      page: 1,
    });

    await useTaskStore.getState().fetchTasks(PROJECT);

    expect(useTaskStore.getState().tasksById[TASK]!.description).toBeUndefined();
  });

  it("lets an explicitly sent description overwrite the cached one", async () => {
    // An empty string from the server is a real value ("this task has no
    // description"), not the absence of one, and must not be back-filled.
    useTaskStore.setState({
      tasksById: { [TASK]: taskFixture({ description: "stale text" }) },
    });
    mockedApi.mockResolvedValue({
      items: [taskFixture({ description: "" , has_description: false })],
      total_count: 1,
      page: 1,
    });

    await useTaskStore.getState().fetchTasks(PROJECT);

    expect(useTaskStore.getState().tasksById[TASK]!.description).toBe("");
  });
});
