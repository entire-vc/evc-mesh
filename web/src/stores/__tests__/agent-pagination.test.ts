import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api", () => ({ api: vi.fn(), getAccessToken: vi.fn(() => null) }));
import { api } from "@/lib/api";
import { useAgentStore } from "@/stores/agent";

const page = (from: number, n: number, hasMore: boolean) => ({
  items: Array.from({ length: n }, (_, i) => ({ id: `a-${from + i}`, name: `agent-${from + i}` })),
  has_more: hasMore,
});

describe("useAgentStore.fetchAgents", () => {
  beforeEach(() => {
    vi.mocked(api).mockReset();
    useAgentStore.setState({ agents: [] });
  });

  it("reads every page: agents past the first page (62 > 50) are in the store", async () => {
    vi.mocked(api).mockImplementation((path: string) =>
      Promise.resolve(path.includes("page=2") ? page(50, 12, false) : page(0, 50, true)));
    await useAgentStore.getState().fetchAgents("ws-1");
    const ids = useAgentStore.getState().agents.map((a) => a.id);
    expect(ids).toHaveLength(62);
    expect(ids).toContain("a-61");
  });

  it("fetchAgent upserts an agent that is not in the loaded list", async () => {
    vi.mocked(api).mockResolvedValue({ id: "x", name: "robin-w2" });
    await useAgentStore.getState().fetchAgent("x");
    expect(useAgentStore.getState().agents.map((a) => a.id)).toEqual(["x"]);
  });
});
