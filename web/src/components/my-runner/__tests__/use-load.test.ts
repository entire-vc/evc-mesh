import { describe, expect, it } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { useLoad, type Loaded } from "@/components/my-runner/use-load";

describe("useLoad", () => {
  it("never renders data loaded for the previous key after the key changes", async () => {
    const seen: Array<[string, Loaded<string>]> = [];
    const ws1 = Promise.resolve("rows of ws-1");
    const fetchFor = (ws: string) => (ws === "ws-1" ? ws1 : new Promise<string>(() => {}));
    const { rerender } = renderHook(({ ws }: { ws: string }) => {
      const { result } = useLoad(() => fetchFor(ws), ws);
      seen.push([ws, result]);
      return result;
    }, { initialProps: { ws: "ws-1" } });
    await act(async () => { await ws1; });
    expect(seen[seen.length - 1]).toEqual(["ws-1", { state: "ready", data: "rows of ws-1" }]);

    rerender({ ws: "ws-2" });
    const underWs2 = seen.filter(([ws]) => ws === "ws-2").map(([, r]) => r);
    expect(underWs2.length).toBeGreaterThan(0);
    expect(underWs2.every((r) => r.state === "loading")).toBe(true);
  });
});
