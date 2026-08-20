import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { ResizableDivider } from "@/components/resizable-divider";
import {
  DOC_TREE_DEFAULT_WIDTH,
  DOC_TREE_MAX_WIDTH,
  DOC_TREE_MIN_WIDTH,
  clampDocTreeWidth,
  loadDocTreeWidth,
  saveDocTreeWidth,
} from "@/lib/docs-layout-storage";

const MIN = 180;
const MAX = 520;
const DEFAULT = 280;

/**
 * The divider is controlled, so a bare render cannot show a drag doing
 * anything. This is the smallest host that behaves like the page: it owns the
 * width, and records what was committed so persistence can be asserted
 * separately from the live value.
 */
function Host({
  initial = DEFAULT,
  onCommit,
}: {
  initial?: number;
  onCommit?: (w: number) => void;
}) {
  const [width, setWidth] = useState(initial);
  return (
    <div>
      <div data-testid="pane" style={{ width: `${width}px` }} />
      <ResizableDivider
        label="Resize document tree"
        value={width}
        min={MIN}
        max={MAX}
        defaultValue={DEFAULT}
        onChange={setWidth}
        onCommit={onCommit}
      />
    </div>
  );
}

const paneWidth = () => screen.getByTestId("pane").style.width;
const handle = () => screen.getByRole("separator", { name: "Resize document tree" });

/** One complete drag gesture: press on the bar, move to `toX`, release. */
function drag(fromX: number, toX: number) {
  fireEvent.mouseDown(handle(), { clientX: fromX, button: 0 });
  fireEvent.mouseMove(window, { clientX: toX });
  fireEvent.mouseUp(window, { clientX: toX });
}

beforeEach(() => {
  localStorage.clear();
});

describe("ResizableDivider — the separator itself", () => {
  it("is a focusable vertical separator that reports its bounds", () => {
    render(<Host />);
    const el = handle();

    expect(el).toHaveAttribute("aria-orientation", "vertical");
    expect(el).toHaveAttribute("aria-valuenow", String(DEFAULT));
    expect(el).toHaveAttribute("aria-valuemin", String(MIN));
    expect(el).toHaveAttribute("aria-valuemax", String(MAX));
    // Reachable by tab, or the only way to move it is with a mouse.
    expect(el).toHaveAttribute("tabindex", "0");

    el.focus();
    expect(el).toHaveFocus();
  });
});

describe("ResizableDivider — dragging", () => {
  it("widens the pane by however far the pointer travelled", () => {
    render(<Host />);
    drag(300, 380);
    expect(paneWidth()).toBe(`${DEFAULT + 80}px`);
  });

  it("narrows it when the pointer goes the other way", () => {
    render(<Host />);
    drag(300, 240);
    expect(paneWidth()).toBe(`${DEFAULT - 60}px`);
  });

  it("keeps following a pointer that has left the 1px bar", () => {
    // The listeners live on window precisely for this: a pointer moving faster
    // than the bar is wide would otherwise strand the drag mid-gesture.
    render(<Host />);
    fireEvent.mouseDown(handle(), { clientX: 300, button: 0 });
    fireEvent.mouseMove(window, { clientX: 320 });
    fireEvent.mouseMove(window, { clientX: 355 });
    expect(paneWidth()).toBe(`${DEFAULT + 55}px`);
    fireEvent.mouseUp(window, { clientX: 355 });
  });

  it("does nothing until the bar has actually been pressed", () => {
    render(<Host />);
    fireEvent.mouseMove(window, { clientX: 900 });
    expect(paneWidth()).toBe(`${DEFAULT}px`);
  });

  it("ignores a right-click drag", () => {
    render(<Host />);
    fireEvent.mouseDown(handle(), { clientX: 300, button: 2 });
    fireEvent.mouseMove(window, { clientX: 400 });
    expect(paneWidth()).toBe(`${DEFAULT}px`);
  });

  it("stops resizing once the button is released", () => {
    render(<Host />);
    drag(300, 340);
    fireEvent.mouseMove(window, { clientX: 900 });
    expect(paneWidth()).toBe(`${DEFAULT + 40}px`);
  });

  it("releases the page's text selection and cursor when the drag ends", () => {
    render(<Host />);
    fireEvent.mouseDown(handle(), { clientX: 300, button: 0 });
    expect(document.body.style.userSelect).toBe("none");
    expect(document.body.style.cursor).toBe("col-resize");

    fireEvent.mouseUp(window, { clientX: 300 });
    expect(document.body.style.userSelect).toBe("");
    expect(document.body.style.cursor).toBe("");
  });

  it("does not leave the page unselectable when it unmounts mid-drag", () => {
    const { unmount } = render(<Host />);
    fireEvent.mouseDown(handle(), { clientX: 300, button: 0 });
    unmount();
    expect(document.body.style.userSelect).toBe("");
    expect(document.body.style.cursor).toBe("");
  });
});

describe("ResizableDivider — bounds", () => {
  it("cannot be dragged down to nothing", () => {
    render(<Host />);
    drag(300, -5000);
    expect(paneWidth()).toBe(`${MIN}px`);
  });

  it("cannot be dragged out over the whole page", () => {
    render(<Host />);
    drag(300, 5000);
    expect(paneWidth()).toBe(`${MAX}px`);
  });

  it("commits the clamped width, not the raw pointer distance", () => {
    const onCommit = vi.fn();
    render(<Host onCommit={onCommit} />);
    drag(300, 5000);
    expect(onCommit).toHaveBeenCalledWith(MAX);
  });
});

describe("ResizableDivider — keyboard", () => {
  it("moves on the arrow keys", () => {
    render(<Host />);
    fireEvent.keyDown(handle(), { key: "ArrowRight" });
    expect(paneWidth()).toBe(`${DEFAULT + 16}px`);
    fireEvent.keyDown(handle(), { key: "ArrowLeft" });
    expect(paneWidth()).toBe(`${DEFAULT}px`);
  });

  it("takes bigger strides with shift held", () => {
    render(<Host />);
    fireEvent.keyDown(handle(), { key: "ArrowRight", shiftKey: true });
    expect(paneWidth()).toBe(`${DEFAULT + 64}px`);
  });

  it("jumps to the bounds on Home and End, and stays inside them", () => {
    render(<Host />);
    fireEvent.keyDown(handle(), { key: "Home" });
    expect(paneWidth()).toBe(`${MIN}px`);
    fireEvent.keyDown(handle(), { key: "ArrowLeft" });
    expect(paneWidth()).toBe(`${MIN}px`);

    fireEvent.keyDown(handle(), { key: "End" });
    expect(paneWidth()).toBe(`${MAX}px`);
    fireEvent.keyDown(handle(), { key: "ArrowRight" });
    expect(paneWidth()).toBe(`${MAX}px`);
  });

  it("commits every key press, so a keyboard resize is remembered too", () => {
    const onCommit = vi.fn();
    render(<Host onCommit={onCommit} />);
    fireEvent.keyDown(handle(), { key: "ArrowRight" });
    expect(onCommit).toHaveBeenCalledWith(DEFAULT + 16);
  });

  it("leaves other keys to the page", () => {
    render(<Host />);
    fireEvent.keyDown(handle(), { key: "a" });
    fireEvent.keyDown(handle(), { key: "Tab" });
    expect(paneWidth()).toBe(`${DEFAULT}px`);
  });
});

describe("ResizableDivider — reset", () => {
  it("returns to the default width on double-click", () => {
    render(<Host initial={MAX} />);
    expect(paneWidth()).toBe(`${MAX}px`);

    fireEvent.doubleClick(handle());
    expect(paneWidth()).toBe(`${DEFAULT}px`);
  });

  it("commits the reset, so the default outlives the reload too", () => {
    const onCommit = vi.fn();
    render(<Host initial={MIN} onCommit={onCommit} />);
    fireEvent.doubleClick(handle());
    expect(onCommit).toHaveBeenLastCalledWith(DEFAULT);
  });

  it("also resets on Enter, for anyone who cannot double-click", () => {
    render(<Host initial={MAX} />);
    fireEvent.keyDown(handle(), { key: "Enter" });
    expect(paneWidth()).toBe(`${DEFAULT}px`);
  });
});

describe("doc tree width persistence", () => {
  it("has nothing to restore before anything is stored", () => {
    expect(loadDocTreeWidth()).toBeNull();
  });

  it("round-trips the chosen width", () => {
    saveDocTreeWidth(340);
    expect(loadDocTreeWidth()).toBe(340);
  });

  it("survives a drag: what onCommit wrote is what the next mount reads", () => {
    render(<Host onCommit={saveDocTreeWidth} />);
    drag(300, 400);
    expect(loadDocTreeWidth()).toBe(DEFAULT + 100);
  });

  it("clamps a width written by a build with different bounds", () => {
    saveDocTreeWidth(9000);
    expect(loadDocTreeWidth()).toBe(DOC_TREE_MAX_WIDTH);
    saveDocTreeWidth(1);
    expect(loadDocTreeWidth()).toBe(DOC_TREE_MIN_WIDTH);
  });

  it("treats junk in storage as nothing stored rather than throwing", () => {
    localStorage.setItem("mesh_docs_tree_width", "not a number");
    expect(loadDocTreeWidth()).toBeNull();
  });

  it("falls back to the default for a width that is not a number at all", () => {
    expect(clampDocTreeWidth(Number.NaN)).toBe(DOC_TREE_DEFAULT_WIDTH);
  });

  it("does not take the page down when localStorage refuses to write", () => {
    const setItem = vi
      .spyOn(Storage.prototype, "setItem")
      .mockImplementation(() => {
        throw new Error("QuotaExceededError");
      });
    expect(() => saveDocTreeWidth(300)).not.toThrow();
    setItem.mockRestore();
  });
});
