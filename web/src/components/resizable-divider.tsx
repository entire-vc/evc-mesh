import { type KeyboardEvent, type MouseEvent, useCallback, useEffect, useRef } from "react";
import { cn } from "@/lib/cn";

export interface ResizableDividerProps {
  /** Current width, in px, of the pane on the divider's left. */
  value: number;
  min: number;
  max: number;
  /** The width a double-click (or Enter) restores. */
  defaultValue: number;
  /** Fired continuously during a drag and once per key press. */
  onChange: (width: number) => void;
  /**
   * Fired when the gesture ends — the moment worth persisting. Separate from
   * onChange so a drag writes to storage once instead of once per mousemove.
   */
  onCommit?: (width: number) => void;
  /** Px moved per arrow key. Shift multiplies it. */
  step?: number;
  /** Accessible name, e.g. "Resize document tree". */
  label: string;
  className?: string;
}

const SHIFT_MULTIPLIER = 4;

/**
 * A one-pixel rule that is also the control for the width of the pane beside
 * it — the line between the tree and the page, not a scrollbar-like widget
 * bolted next to one.
 *
 * It is a focusable `separator`, i.e. the ARIA window-splitter pattern, so it
 * carries aria-valuenow/min/max and answers the arrow keys. A divider that can
 * only be dragged is a divider that a keyboard user cannot move at all, and the
 * whole point of this control is that the reader with the long document titles
 * gets to decide how much room they take.
 */
export function ResizableDivider({
  value,
  min,
  max,
  defaultValue,
  onChange,
  onCommit,
  step = 16,
  label,
  className,
}: ResizableDividerProps) {
  const clamp = useCallback(
    (n: number) => Math.min(max, Math.max(min, Math.round(n))),
    [min, max],
  );

  // The window listeners below are installed once, so they must not close over
  // the props of the render that installed them.
  const latest = useRef({ onChange, onCommit, clamp });
  latest.current = { onChange, onCommit, clamp };

  const drag = useRef<{ startX: number; startValue: number; width: number } | null>(
    null,
  );

  useEffect(() => {
    // On window rather than on the element: a pointer routinely outruns a 1px
    // bar, and listeners on the bar stop firing the instant it does — which
    // reads as the drag "sticking" halfway.
    function handleMove(event: globalThis.MouseEvent) {
      const current = drag.current;
      if (!current) return;
      event.preventDefault();
      const next = latest.current.clamp(
        current.startValue + (event.clientX - current.startX),
      );
      current.width = next;
      latest.current.onChange(next);
    }

    function handleUp() {
      const current = drag.current;
      if (!current) return;
      drag.current = null;
      document.body.style.userSelect = "";
      document.body.style.cursor = "";
      latest.current.onCommit?.(current.width);
    }

    window.addEventListener("mousemove", handleMove);
    window.addEventListener("mouseup", handleUp);
    return () => {
      window.removeEventListener("mousemove", handleMove);
      window.removeEventListener("mouseup", handleUp);
      // A drag interrupted by an unmount must not leave the whole page
      // unselectable and stuck on a resize cursor.
      if (drag.current) {
        drag.current = null;
        document.body.style.userSelect = "";
        document.body.style.cursor = "";
      }
    };
  }, []);

  const handleMouseDown = (event: MouseEvent<HTMLDivElement>) => {
    if (event.button !== 0) return;
    event.preventDefault();
    drag.current = { startX: event.clientX, startValue: value, width: value };
    // Without these the drag selects the document text it passes over, and the
    // cursor flickers back to a caret every time it leaves the 1px bar.
    document.body.style.userSelect = "none";
    document.body.style.cursor = "col-resize";
  };

  const reset = () => {
    const next = clamp(defaultValue);
    onChange(next);
    onCommit?.(next);
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const delta = event.shiftKey ? step * SHIFT_MULTIPLIER : step;
    let next: number;
    switch (event.key) {
      case "ArrowLeft":
        next = value - delta;
        break;
      case "ArrowRight":
        next = value + delta;
        break;
      case "Home":
        next = min;
        break;
      case "End":
        next = max;
        break;
      case "Enter":
        next = defaultValue;
        break;
      default:
        return;
    }
    event.preventDefault();
    const clamped = clamp(next);
    onChange(clamped);
    onCommit?.(clamped);
  };

  return (
    <div
      role="separator"
      aria-orientation="vertical"
      aria-label={label}
      aria-valuenow={value}
      aria-valuemin={min}
      aria-valuemax={max}
      tabIndex={0}
      onMouseDown={handleMouseDown}
      onDoubleClick={reset}
      onKeyDown={handleKeyDown}
      className={cn(
        "group relative w-px shrink-0 cursor-col-resize bg-border outline-none transition-colors",
        "hover:bg-primary/50 focus-visible:bg-primary",
        className,
      )}
    >
      {/* The line is 1px because that is the design; the grab area is 9px
          because 1px is not a target anybody can hit. */}
      <span aria-hidden="true" className="absolute inset-y-0 -left-1 -right-1" />
    </div>
  );
}
