import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";

vi.mock("@/lib/extension-slots", async (orig) => ({
  ...(await orig<typeof import("@/lib/extension-slots")>()),
  useSidebarItems: () => [{ extensionId: "acme", title: "Acme board", projectLevel: true }],
}));

// Each read hands back a promise the test settles by hand, in any order.
const pending = vi.hoisted(() => ({ settle: [] as Array<(ids: string[] | Error) => void> }));
vi.mock("@/lib/api", () => ({
  api: vi.fn(
    () =>
      new Promise((res, rej) => {
        pending.settle.push((ids) =>
          ids instanceof Error
            ? rej(ids)
            : res({ extensions: ids.map((id) => ({ extension_id: id, enabled: true, settings: {} })) }),
        );
      }),
  ),
}));

import { readProjectBindings, resetProjectExtensionTabsForTests, useProjectExtensionTabs } from "@/lib/project-extension-tabs";

const ids = (tabs: Array<{ extensionId: string }>) => tabs.map((t) => t.extensionId);

describe("useProjectExtensionTabs ordering", () => {
  beforeEach(() => {
    pending.settle = [];
    resetProjectExtensionTabsForTests();
  });

  async function headerReadInFlight() {
    const hook = renderHook(() => useProjectExtensionTabs("p1"));
    await waitFor(() => expect(pending.settle).toHaveLength(1));
    return hook;
  }

  it("a header read started before an unbind does not bring the tab back", async () => {
    const { result } = await headerReadInFlight();
    // Settings unbinds acme and re-reads; that newer read answers first...
    const settings = readProjectBindings("p1");
    await act(async () => { pending.settle[1]?.([]); await settings; });
    // ...then the older header read answers with acme still bound.
    await act(async () => pending.settle[0]?.(["acme"]));
    expect(ids(result.current)).toEqual([]);
  });

  it("a header read started before a bind does not hide the tab", async () => {
    const { result } = await headerReadInFlight();
    const settings = readProjectBindings("p1");
    await act(async () => { pending.settle[1]?.(["acme"]); await settings; });
    await act(async () => pending.settle[0]?.([]));
    expect(ids(result.current)).toEqual(["acme"]);
  });

  it("an older settings read (from a view since closed) does not overwrite a newer one", async () => {
    const { result } = await headerReadInFlight();
    await act(async () => pending.settle[0]?.([]));
    const closedView = readProjectBindings("p1");
    const newView = readProjectBindings("p1");
    await act(async () => { pending.settle[2]?.(["acme"]); await newView; });
    await act(async () => { pending.settle[1]?.([]); await closedView; });
    expect(ids(result.current)).toEqual(["acme"]);
  });

  it("an older failed read does not wipe a newer answer, a newer failed read does", async () => {
    const { result } = await headerReadInFlight();
    const newer = readProjectBindings("p1");
    await act(async () => { pending.settle[1]?.(["acme"]); await newer; });
    await act(async () => { pending.settle[0]?.(new Error("boom")); });
    expect(ids(result.current)).toEqual(["acme"]);
    const failing = readProjectBindings("p1");
    await act(async () => { pending.settle[2]?.(new Error("boom")); await failing.catch(() => {}); });
    expect(ids(result.current)).toEqual([]);
  });
});
