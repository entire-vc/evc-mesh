import { describe, it, expect, beforeEach } from "vitest";
import { render, screen, waitFor, act } from "@testing-library/react";
import { EntityIcon } from "@/components/ui/entity-icon";
import { registerThemeIcons, clearThemeIcons } from "@/core/icons/theme-icons";
import { parseIconValue } from "@/core/icons/value";
import { useThemeStore } from "@/core/themes/store";

function Default({ className }: { className?: string }) {
  return <svg data-testid="default" className={className} />;
}
function ThemeRocket({ className }: { className?: string }) {
  return <svg data-testid="theme-rocket" className={className} />;
}

beforeEach(() => {
  clearThemeIcons("graphite-test");
  useThemeStore.getState().set({ viewsThemeId: "mesh-default" });
});

describe("parseIconValue", () => {
  it("classifies values", () => {
    expect(parseIconValue("")).toEqual({ kind: "none" });
    expect(parseIconValue("🚀")).toEqual({ kind: "emoji", emoji: "🚀" });
    expect(parseIconValue("icon:rocket")).toEqual({ kind: "icon", name: "rocket" });
    expect(parseIconValue("icon:Rocket!")).toEqual({ kind: "text", text: "icon:Rocket!" });
    expect(parseIconValue("rocket")).toEqual({ kind: "text", text: "rocket" });
  });
});

describe("EntityIcon", () => {
  it("draws the live theme's icon when it has one", () => {
    registerThemeIcons("graphite-test", { rocket: ThemeRocket }, {});
    useThemeStore.getState().set({ viewsThemeId: "graphite-test" });
    render(<EntityIcon value="icon:rocket" fallback={Default} />);
    expect(screen.getByTestId("theme-rocket")).toBeTruthy();
    expect(screen.queryByTestId("default")).toBeNull();
  });

  it("without a theme icon draws the core icon of that name", async () => {
    const { container } = render(<EntityIcon value="icon:rocket" fallback={Default} className="h-4 w-4" />);
    await waitFor(() => expect(container.querySelector("svg.lucide-rocket")).not.toBeNull());
    expect(screen.queryByTestId("theme-rocket")).toBeNull();
    expect(screen.queryByTestId("default")).toBeNull();
  });

  it("an unknown name draws the entity's default", async () => {
    render(<EntityIcon value="icon:no-such-icon-xyz" fallback={Default} />);
    // the core set loads, the name is not in it: the default stays
    await act(async () => {
      await import("@/core/icons/core-loader").then((m) => m.loadCoreIcons());
    });
    expect(screen.getByTestId("default")).toBeTruthy();
  });

  it("empty value draws the default; legacy text and emoji are shown as they are", () => {
    const { rerender } = render(<EntityIcon value="" fallback={Default} />);
    expect(screen.getByTestId("default")).toBeTruthy();
    rerender(<EntityIcon value="rocket" fallback={Default} />);
    expect(screen.getByText("rocket")).toBeTruthy();
    rerender(<EntityIcon value="🚀" fallback={Default} />);
    expect(screen.getByRole("img", { name: "🚀" })).toBeTruthy();
  });

  it("redraws at once when the workspace theme changes", () => {
    registerThemeIcons("graphite-test", { rocket: ThemeRocket }, {});
    render(<EntityIcon value="icon:rocket" fallback={Default} />);
    expect(screen.queryByTestId("theme-rocket")).toBeNull(); // default theme: core icon, not the theme's
    act(() => useThemeStore.getState().set({ viewsThemeId: "graphite-test" }));
    expect(screen.getByTestId("theme-rocket")).toBeTruthy();
    act(() => useThemeStore.getState().set({ viewsThemeId: "mesh-default" }));
    expect(screen.queryByTestId("theme-rocket")).toBeNull();
  });

  it("a theme icon that throws falls back instead of taking the page down", async () => {
    function Boom(): never {
      throw new Error("boom");
    }
    registerThemeIcons("graphite-test", { rocket: Boom as never }, {});
    useThemeStore.getState().set({ viewsThemeId: "graphite-test" });
    const err = console.error;
    console.error = () => {};
    try {
      render(<EntityIcon value="icon:rocket" fallback={Default} />);
      await waitFor(() => expect(screen.queryByTestId("theme-rocket")).toBeNull());
    } finally {
      console.error = err;
    }
  });
  it("recovers when a broken theme icon is replaced by a working one", async () => {
    function Boom(): never {
      throw new Error("broken theme icon");
    }
    registerThemeIcons("graphite-test", { rocket: Boom as never }, {});
    useThemeStore.getState().set({ viewsThemeId: "graphite-test" });
    const err = console.error;
    console.error = () => {};
    try {
      render(<EntityIcon value="icon:rocket" fallback={Default} />);
      expect(screen.queryByTestId("theme-rocket")).toBeNull();
      act(() => {
        registerThemeIcons("graphite-test", { rocket: ThemeRocket }, {});
      });
      await waitFor(() => expect(screen.getByTestId("theme-rocket")).toBeTruthy());
    } finally {
      console.error = err;
    }
  });

  it("keeps surrounding spaces of a legacy text icon", () => {
    expect(parseIconValue(" old ")).toEqual({ kind: "text", text: " old " });
    expect(parseIconValue("   ")).toEqual({ kind: "none" });
  });
});
