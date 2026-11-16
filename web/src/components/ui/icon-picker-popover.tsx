// Lazy chunk: popover shell + panel + catalogue. Imported on demand from
// icon-picker.tsx so only the trigger button lives in the main bundle.
import {
  type KeyboardEvent as ReactKeyboardEvent,
  type RefObject,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import IconPickerPanel from "./icon-picker-panel";

const POPOVER_WIDTH = 320;
const MARGIN = 8;

export interface IconPickerPopoverProps {
  anchorRef: RefObject<HTMLElement | null>;
  id: string;
  label: string;
  value: string;
  onSelect: (value: string) => void;
  /** Close without choosing (Escape, outside click). */
  onDismiss: (opts: { restoreFocus: boolean }) => void;
}

export default function IconPickerPopover({
  anchorRef,
  id,
  label,
  value,
  onSelect,
  onDismiss,
}: IconPickerPopoverProps) {
  const [pos, setPos] = useState({ top: 0, left: 0, width: POPOVER_WIDTH });
  const popRef = useRef<HTMLDivElement>(null);

  const place = useCallback(() => {
    const el = anchorRef.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    const width = Math.min(POPOVER_WIDTH, window.innerWidth - MARGIN * 2);
    const left = Math.max(MARGIN, Math.min(r.left, window.innerWidth - width - MARGIN));
    // Below the trigger when it fits; otherwise above; otherwise pinned to the
    // viewport edge (the panel scrolls inside, its height is capped by vh).
    const h = popRef.current?.offsetHeight ?? 0;
    let top = r.bottom + 4;
    if (top + h > window.innerHeight - MARGIN) {
      const above = r.top - 4 - h;
      top = above >= MARGIN ? above : Math.max(MARGIN, window.innerHeight - MARGIN - h);
    }
    setPos({ top, left, width });
  }, [anchorRef]);

  useLayoutEffect(() => {
    place();
    window.addEventListener("resize", place);
    window.addEventListener("scroll", place, true);
    return () => {
      window.removeEventListener("resize", place);
      window.removeEventListener("scroll", place, true);
    };
  }, [place]);

  useEffect(() => {
    const onDown = (e: MouseEvent) => {
      const n = e.target as Node;
      if (anchorRef.current?.contains(n) || popRef.current?.contains(n)) return;
      onDismiss({ restoreFocus: false });
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [anchorRef, onDismiss]);

  const onKeyDown = (e: ReactKeyboardEvent) => {
    if (e.key === "Escape") {
      // Do not let a surrounding dialog or form treat this as its own Escape.
      e.stopPropagation();
      e.preventDefault();
      onDismiss({ restoreFocus: true });
    } else if (e.key === "Tab") {
      // Keep Tab inside the popover while it is open.
      const f = Array.from(popRef.current?.querySelectorAll<HTMLElement>("input, button") ?? []);
      if (!f.length) return;
      const first = f[0]!;
      const last = f[f.length - 1]!;
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    }
  };

  return createPortal(
    <div
      ref={popRef}
      id={id}
      role="dialog"
      aria-label={label}
      onKeyDown={onKeyDown}
      style={{ top: pos.top, left: pos.left, width: pos.width }}
      className="fixed z-[9999] rounded-lg border border-border bg-popover text-popover-foreground shadow-lg"
    >
      <IconPickerPanel value={value} onSelect={onSelect} />
    </div>,
    document.body,
  );
}
