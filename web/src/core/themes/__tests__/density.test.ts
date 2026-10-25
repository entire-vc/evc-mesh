import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: vi.fn() };
});
import { api } from "@/lib/api";
import { useModeStore } from "../mode";
import { chooseDensity, readDensity } from "../density";

beforeEach(() => {
  localStorage.clear();
  delete document.documentElement.dataset.density;
  vi.mocked(api).mockReset();
  vi.mocked(api).mockResolvedValue({});
});

describe("interface density", () => {
  it("compact is set on <html>, kept for the next load and saved with the colour mode", async () => {
    useModeStore.setState({ chosen: "dark" });
    chooseDensity("compact");
    expect(document.documentElement.dataset.density).toBe("compact");
    expect(localStorage.getItem("density")).toBe("compact");
    expect(readDensity()).toBe("compact");
    await vi.waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/v1/me/ui-state/appearance", {
        method: "PUT",
        body: { state: { mode: "dark", density: "compact" } },
      }),
    );
  });

  it("comfortable switches it back", () => {
    chooseDensity("compact");
    chooseDensity("comfortable");
    expect(document.documentElement.dataset.density).toBe("comfortable");
    expect(readDensity()).toBe("comfortable");
  });

  it("nothing painted reads as comfortable", () => {
    expect(readDensity()).toBe("comfortable");
  });
});
