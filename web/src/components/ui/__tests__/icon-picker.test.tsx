import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, act } from "@testing-library/react";
import { useState } from "react";
import { IconPicker } from "@/components/ui/icon-picker";
import { searchEmoji } from "@/components/ui/emoji-data";
import { registerThemeIcons, clearThemeIcons } from "@/core/icons/theme-icons";
import { useThemeStore } from "@/core/themes/store";

function Harness({ onChange, initial = "" }: { onChange?: (v: string) => void; initial?: string }) {
  const [v, setV] = useState(initial);
  return (
    <IconPicker
      value={v}
      onChange={(x) => {
        setV(x);
        onChange?.(x);
      }}
    />
  );
}

const trigger = () => screen.getByRole("button", { name: /choose icon|выбрать иконку/i });
const cellByEmoji = (e: string) =>
  Array.from(document.querySelectorAll<HTMLElement>("[data-emoji]")).find((b) => b.dataset.emoji === e)!;

beforeEach(() => {
  window.localStorage.clear();
  clearThemeIcons("test-theme");
  useThemeStore.getState().set({ viewsThemeId: "mesh-default" });
});

describe("emoji search", () => {
  it("finds the rocket by English and Russian names", () => {
    expect(searchEmoji("rocket").map((e) => e.e)).toContain("🚀");
    expect(searchEmoji("ракета").map((e) => e.e)).toContain("🚀");
    expect(searchEmoji("РАКЕТ").map((e) => e.e)).toContain("🚀");
  });
  it("returns nothing for gibberish (negative control)", () => {
    expect(searchEmoji("zzzzqqq")).toHaveLength(0);
  });
});

describe("IconPicker", () => {
  it("opens, searches, selects with the keyboard only, and closes", async () => {
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);
    trigger().focus();
    fireEvent.click(trigger()); // Enter/Space on a button fires click
    const search = await screen.findByRole("searchbox");
    await waitFor(() => expect(document.activeElement).toBe(search));
    fireEvent.change(search, { target: { value: "ракета" } });
    fireEvent.keyDown(search, { key: "ArrowDown" });
    expect((document.activeElement as HTMLElement).dataset.emoji).toBe("🚀");
    fireEvent.click(document.activeElement as HTMLElement);
    expect(onChange).toHaveBeenCalledWith("🚀");
    expect(screen.queryByRole("searchbox")).toBeNull();
    expect(document.activeElement).toBe(trigger());
    expect(trigger().textContent).toBe("🚀");
  });

  it("switches tabs with the arrow keys and finds the core icon by both languages", async () => {
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);
    fireEvent.click(trigger());
    await screen.findByRole("searchbox");
    const emojiTab = screen.getByRole("tab", { name: /emoji|эмодзи/i });
    emojiTab.focus();
    fireEvent.keyDown(emojiTab, { key: "ArrowRight" });
    const iconsTab = screen.getByRole("tab", { name: /^icons$|^иконки$/i });
    expect(iconsTab.getAttribute("aria-selected")).toBe("true");
    expect(document.activeElement).toBe(iconsTab);

    const search = screen.getByRole("searchbox");
    fireEvent.change(search, { target: { value: "rocket" } });
    expect(document.querySelector('[data-icon="rocket"]')).not.toBeNull();
    fireEvent.change(search, { target: { value: "ракета" } });
    expect(document.querySelector('[data-icon="rocket"]')).not.toBeNull();
    fireEvent.change(search, { target: { value: "zzzzqqq" } });
    expect(document.querySelector("[data-icon]")).toBeNull();

    fireEvent.change(search, { target: { value: "ракета" } });
    fireEvent.keyDown(search, { key: "Enter" });
    expect(onChange).toHaveBeenCalledWith("icon:rocket");
    expect(screen.queryByRole("searchbox")).toBeNull();
  });

  it("offers the live theme's own icons first, found by the theme's keywords", async () => {
    function Own({ className }: { className?: string }) {
      return <svg data-testid="own" className={className} />;
    }
    registerThemeIcons("test-theme", { "my-glyph": Own }, { "my-glyph": { en: "glyph", ru: ["глиф"] } });
    useThemeStore.getState().set({ viewsThemeId: "test-theme" });
    render(<Harness initial="icon:my-glyph" />);
    fireEvent.click(trigger());
    await screen.findByRole("searchbox");
    // an icon value opens on the Icons tab
    expect(screen.getByRole("tab", { name: /^icons$|^иконки$/i }).getAttribute("aria-selected")).toBe("true");
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "глиф" } });
    expect(document.querySelector('[data-icon="my-glyph"]')).not.toBeNull();
  });

  it("an open catalog picks up an icon added by a replaced theme bundle", async () => {
    function Own({ className }: { className?: string }) {
      return <svg data-testid="own" className={className} />;
    }
    registerThemeIcons("test-theme", { "my-glyph": Own }, { "my-glyph": { en: "glyph" } });
    useThemeStore.getState().set({ viewsThemeId: "test-theme" });
    render(<Harness initial="icon:my-glyph" />);
    fireEvent.click(trigger());
    await screen.findByRole("searchbox");
    expect(document.querySelector('[data-icon="fresh-glyph"]')).toBeNull();
    act(() => {
      registerThemeIcons("test-theme", { "my-glyph": Own, "fresh-glyph": Own }, {});
    });
    await waitFor(() => expect(document.querySelector('[data-icon="fresh-glyph"]')).not.toBeNull());
  });

  it("Escape closes and returns focus to the trigger", async () => {
    render(<Harness />);
    fireEvent.click(trigger());
    const search = await screen.findByRole("searchbox");
    fireEvent.keyDown(search, { key: "Escape" });
    expect(screen.queryByRole("searchbox")).toBeNull();
    expect(document.activeElement).toBe(trigger());
  });

  it("clears with «no icon» and remembers recents", async () => {
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);
    fireEvent.click(trigger());
    await screen.findByRole("searchbox");
    fireEvent.click(cellByEmoji("🔥"));
    expect(JSON.parse(window.localStorage.getItem("mesh.icon-picker.recent")!)).toEqual(["🔥"]);
    fireEvent.click(trigger());
    await screen.findByRole("searchbox");
    fireEvent.click(screen.getByRole("button", { name: /no icon|без иконки/i }));
    expect(onChange).toHaveBeenLastCalledWith("");
  });

  it("survives blocked localStorage", async () => {
    const spy = vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    render(<Harness />);
    fireEvent.click(trigger());
    expect(await screen.findByRole("searchbox")).toBeTruthy();
    spy.mockRestore();
  });

  it("does not load the popover chunk code until opened (no panel in the DOM)", () => {
    render(<Harness />);
    expect(screen.queryByRole("tablist")).toBeNull();
    void act;
  });
});
