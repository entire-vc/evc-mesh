import { renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useWorkspaceTheme } from "../use-workspace-theme";
import { applyWorkspaceTheme, clearWorkspaceTheme } from "@/core/themes/runtime";
import { useWorkspaceStore } from "@/stores/workspace";

vi.mock("@/core/themes/runtime", () => ({ applyWorkspaceTheme: vi.fn(async () => {}), clearWorkspaceTheme: vi.fn() }));

beforeEach(() => {
  vi.mocked(applyWorkspaceTheme).mockClear();
  vi.mocked(clearWorkspaceTheme).mockClear();
  useWorkspaceStore.setState({ currentWorkspace: { slug: "old" } as never });
});

describe("useWorkspaceTheme", () => {
  it("follows the URL slug, not the store that is updated a commit later", () => {
    renderHook(() => useWorkspaceTheme("new"));
    expect(vi.mocked(applyWorkspaceTheme).mock.calls[0]?.[0]).toBe("new");
  });

  it("ignores the store when the URL names no workspace", () => {
    renderHook(() => useWorkspaceTheme());
    expect(applyWorkspaceTheme).not.toHaveBeenCalled();
    expect(clearWorkspaceTheme).toHaveBeenCalled();
  });

  it("does not touch the theme until the session is ready, then applies it", () => {
    const { rerender } = renderHook(({ r }) => useWorkspaceTheme("a", r), { initialProps: { r: false } });
    expect(applyWorkspaceTheme).not.toHaveBeenCalled();
    expect(clearWorkspaceTheme).not.toHaveBeenCalled();
    rerender({ r: true });
    expect(vi.mocked(applyWorkspaceTheme).mock.calls[0]?.[0]).toBe("a");
  });

  it("marks the previous request stale when the slug changes", () => {
    const { rerender } = renderHook(({ s }) => useWorkspaceTheme(s), { initialProps: { s: "a" } });
    const isStale = vi.mocked(applyWorkspaceTheme).mock.calls[0]![1]!;
    expect(isStale()).toBe(false);
    rerender({ s: "b" });
    expect(isStale()).toBe(true);
  });

  it("clears the workspace theme when the shell unmounts (sign-out to /login)", () => {
    const { unmount } = renderHook(() => useWorkspaceTheme("a"));
    expect(clearWorkspaceTheme).not.toHaveBeenCalled();
    unmount();
    expect(clearWorkspaceTheme).toHaveBeenCalled();
  });
});
