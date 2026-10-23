import { describe, expect, it } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { registerDefaultSlotView, registerSlotView, useSlotRegistryVersion } from "../registry";

// Page chunks register the built-in theme at module scope, so a lazy chunk that loads while a page is
// mounted registers the same views again. That must not look like a theme change (task.open commit budget).
describe("slot registry registration", () => {
  const View = () => null;

  it("does not announce a registration of the view that is already there", () => {
    registerDefaultSlotView("idem.slot", View);
    const { result } = renderHook(() => useSlotRegistryVersion());
    const before = result.current;
    act(() => registerDefaultSlotView("idem.slot", View));
    expect(result.current).toBe(before);
  });

  it("still announces a different view for the same slot", () => {
    registerDefaultSlotView("idem.slot2", View);
    const { result } = renderHook(() => useSlotRegistryVersion());
    const before = result.current;
    act(() => registerSlotView("idem.slot2", "mesh-default", () => null));
    expect(result.current).toBe(before + 1);
  });
});
