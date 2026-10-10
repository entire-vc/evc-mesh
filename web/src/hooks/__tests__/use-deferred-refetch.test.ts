import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { useDeferredRefetch } from "@/hooks/use-deferred-refetch";

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

const setup = () => {
  const run = vi.fn();
  const { result, unmount } = renderHook(() => useDeferredRefetch(run, 500));
  return { run, h: result, unmount };
};

describe("useDeferredRefetch", () => {
  it("without a drag, a burst of events refreshes once after the debounce", () => {
    const { run, h } = setup();
    act(() => { h.current.schedule(); h.current.schedule(); });
    act(() => { vi.advanceTimersByTime(500); });
    expect(run).toHaveBeenCalledTimes(1);
  });

  it("an event during a drag does not refresh until drop, then exactly once", () => {
    const { run, h } = setup();
    act(() => { h.current.startDrag(); h.current.schedule(); });
    act(() => { vi.advanceTimersByTime(2000); });
    expect(run).not.toHaveBeenCalled();
    act(() => { h.current.flush(); });
    expect(run).toHaveBeenCalledTimes(1);
    act(() => { vi.advanceTimersByTime(2000); });
    expect(run).toHaveBeenCalledTimes(1);
  });

  it("two events during a drag give one refresh", () => {
    const { run, h } = setup();
    act(() => { h.current.startDrag(); h.current.schedule(); });
    act(() => { vi.advanceTimersByTime(600); h.current.schedule(); });
    act(() => { vi.advanceTimersByTime(600); });
    act(() => { h.current.flush(); });
    act(() => { vi.advanceTimersByTime(2000); });
    expect(run).toHaveBeenCalledTimes(1);
  });

  it("drop while the debounce timer is still pending: one refresh, no second from the timer", () => {
    const { run, h } = setup();
    act(() => { h.current.startDrag(); h.current.schedule(); });
    act(() => { vi.advanceTimersByTime(100); h.current.flush(); });
    expect(run).toHaveBeenCalledTimes(1);
    act(() => { vi.advanceTimersByTime(2000); });
    expect(run).toHaveBeenCalledTimes(1);
  });

  it("a drag with no events refreshes nothing on drop/cancel", () => {
    const { run, h } = setup();
    act(() => { h.current.startDrag(); h.current.flush(); });
    expect(run).not.toHaveBeenCalled();
  });

  it("unmount cancels a pending refresh", () => {
    const { run, h, unmount } = setup();
    act(() => { h.current.schedule(); });
    unmount();
    act(() => { vi.advanceTimersByTime(2000); });
    expect(run).not.toHaveBeenCalled();
  });
});
