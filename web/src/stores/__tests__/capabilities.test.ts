import { afterEach, beforeEach, describe, it, expect, vi } from "vitest";

vi.mock("@/lib/api", () => ({
  api: vi.fn(),
}));
import { api } from "@/lib/api";
import { useCapabilitiesStore } from "@/stores/capabilities";

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;

describe("useCapabilitiesStore", () => {
  beforeEach(() => {
    // Reset store state between tests — zustand stores are module-singletons.
    useCapabilitiesStore.setState({ sparkEnabled: true, sparkUrl: "", fetched: false });
    mockedApi.mockReset();
  });
  afterEach(() => vi.clearAllMocks());

  it("defaults to sparkEnabled=true, sparkUrl='' before any fetch", () => {
    expect(useCapabilitiesStore.getState().sparkEnabled).toBe(true);
    expect(useCapabilitiesStore.getState().sparkUrl).toBe("");
  });

  it("adopts spark_url from /api/version — never a hardcoded vendor domain", async () => {
    mockedApi.mockResolvedValueOnce({ spark_enabled: true, spark_url: "https://spark.example.com" });

    await useCapabilitiesStore.getState().fetch();

    expect(useCapabilitiesStore.getState().sparkUrl).toBe("https://spark.example.com");
  });

  it("leaves sparkUrl empty when the server has none configured", async () => {
    mockedApi.mockResolvedValueOnce({ spark_enabled: true });

    await useCapabilitiesStore.getState().fetch();

    expect(useCapabilitiesStore.getState().sparkUrl).toBe("");
  });

  it("adopts spark_enabled=false from /api/version", async () => {
    mockedApi.mockResolvedValueOnce({ spark_enabled: false });

    await useCapabilitiesStore.getState().fetch();

    expect(useCapabilitiesStore.getState().sparkEnabled).toBe(false);
    expect(useCapabilitiesStore.getState().fetched).toBe(true);
    expect(mockedApi).toHaveBeenCalledWith("/api/version");
  });

  it("adopts spark_enabled=true from /api/version", async () => {
    mockedApi.mockResolvedValueOnce({ spark_enabled: true });

    await useCapabilitiesStore.getState().fetch();

    expect(useCapabilitiesStore.getState().sparkEnabled).toBe(true);
  });

  // The server route registration (cmd/api/main.go: routes only exist when
  // MESH_SPARK_ENABLED=true) is the real gate — a network failure here must
  // fail OPEN (leave the nav link visible) rather than hide a feature that
  // might actually be enabled, matching the same choice web/src/pages/spark.tsx
  // already makes for the same reason.
  it("fails open (sparkEnabled stays true) when the fetch rejects", async () => {
    mockedApi.mockRejectedValueOnce(new Error("network error"));

    await useCapabilitiesStore.getState().fetch();

    expect(useCapabilitiesStore.getState().sparkEnabled).toBe(true);
    expect(useCapabilitiesStore.getState().sparkUrl).toBe("");
    expect(useCapabilitiesStore.getState().fetched).toBe(true);
  });

  it("only calls the API once even if fetch() is invoked repeatedly", async () => {
    mockedApi.mockResolvedValue({ spark_enabled: false });

    await useCapabilitiesStore.getState().fetch();
    await useCapabilitiesStore.getState().fetch();
    await useCapabilitiesStore.getState().fetch();

    expect(mockedApi).toHaveBeenCalledTimes(1);
  });
});
