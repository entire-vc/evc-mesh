import { afterEach, beforeEach, describe, it, expect, vi } from "vitest";

vi.mock("@/lib/api", () => ({
  api: vi.fn(),
}));
import { api } from "@/lib/api";
import { useSparkStore } from "@/stores/spark";

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;

// #4a3195a5: the server now resolves the catalog base URL (and whether Spark
// is enabled at all) PER WORKSPACE via an optional workspace_id query param,
// mirroring what install() already sent in its body. These tests prove the
// store actually threads it through on the three read actions — without
// this, a workspace's own is_active=false would have nothing to bite on and
// every request would silently fall back to the instance-wide env URL.
describe("useSparkStore — workspace_id threading (#4a3195a5)", () => {
  beforeEach(() => {
    useSparkStore.setState({
      agents: [],
      popularAgents: [],
      selectedAgent: null,
      isLoading: false,
      isInstalling: false,
      error: null,
      lastInstallResult: null,
    });
    mockedApi.mockReset();
  });
  afterEach(() => vi.clearAllMocks());

  it("search() includes workspace_id in the query params when given one", async () => {
    mockedApi.mockResolvedValueOnce({ items: [], count: 0 });

    await useSparkStore.getState().search("q", [], 20, undefined, "ws-123");

    expect(mockedApi).toHaveBeenCalledWith(
      "/api/v1/spark/agents",
      expect.objectContaining({ params: expect.objectContaining({ workspace_id: "ws-123" }) }),
    );
  });

  it("search() omits workspace_id when none is given", async () => {
    mockedApi.mockResolvedValueOnce({ items: [], count: 0 });

    await useSparkStore.getState().search("q", []);

    expect(mockedApi).toHaveBeenCalledTimes(1);
    const call = mockedApi.mock.calls[0] as [string, { params?: Record<string, unknown> }];
    expect(call[1].params).not.toHaveProperty("workspace_id");
  });

  it("fetchPopular() includes workspace_id when given one", async () => {
    mockedApi.mockResolvedValueOnce({ items: [], count: 0 });

    await useSparkStore.getState().fetchPopular(20, "ws-456");

    expect(mockedApi).toHaveBeenCalledWith(
      "/api/v1/spark/agents/popular",
      expect.objectContaining({ params: expect.objectContaining({ workspace_id: "ws-456" }) }),
    );
  });

  it("fetchAgent() includes workspace_id as a query param when given one", async () => {
    mockedApi.mockResolvedValueOnce({ id: "a1" });

    await useSparkStore.getState().fetchAgent("a1", "ws-789");

    expect(mockedApi).toHaveBeenCalledWith(
      "/api/v1/spark/agents/a1",
      expect.objectContaining({ params: { workspace_id: "ws-789" } }),
    );
  });
});
