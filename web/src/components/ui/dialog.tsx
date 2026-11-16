import {
  type HTMLAttributes,
  type KeyboardEvent,
  createContext,
  forwardRef,
  useContext,
  useEffect,
  useId,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import { cn } from "@/lib/cn";
import { X } from "lucide-react";

interface DialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  children: React.ReactNode;
  /**
   * Width override for the centring wrapper. The wrapper, not the content, is
   * what caps a dialog's width — a `max-w-*` on DialogContent alone is silently
   * clamped by this element and has no effect. Defaults to the previous fixed
   * `max-w-lg`, so every existing dialog is unchanged.
   */
  className?: string;
  /**
   * Close on a click on the dimmed backdrop. Off by default: the wrapper's own
   * click handler never fires (the backdrop covers it), so existing dialogs
   * have never closed this way and keep not doing so.
   */
  closeOnBackdrop?: boolean;
  /**
   * Close on Escape. On by default; turn it off only while closing would lose
   * work in flight (a confirm that is already processing).
   */
  closeOnEscape?: boolean;
}

/**
 * Ids that tie DialogContent to the DialogTitle / DialogDescription inside it,
 * so the dialog is announced by its title without every caller wiring ids. An
 * id with no element behind it (a dialog without a description) is ignored by
 * accessible-name computation.
 */
const DialogContext = createContext<{ title: string; description: string } | null>(null);

/**
 * Elements Tab can reach inside `root`, in DOM order. The browser's own
 * tabIndex already says what is focusable; what CSS hides (`hidden sm:flex`)
 * is dropped too — jsdom has no layout, so there everything counts as visible.
 */
function tabbables(root: Element): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>("*")].filter(
    (el) =>
      el.tabIndex >= 0 &&
      !el.matches(":disabled,a:not([href]),[hidden],[hidden] *,[inert] *") &&
      (!el.checkVisibility || el.checkVisibility()),
  );
}

/**
 * Key presses a dialog has already dealt with. A nested dialog is portaled
 * outside its parent's box, but its events still bubble to the parent through
 * the React tree; the innermost dialog records them here so the outer one
 * leaves them alone, without stopping propagation for anyone else.
 */
const handledKeys = new WeakSet<Event>();

export function Dialog({ open, ...rest }: DialogProps) {
  return open ? <OpenDialog {...rest} /> : null;
}

function OpenDialog({
  onOpenChange,
  children,
  className,
  closeOnBackdrop = false,
  closeOnEscape = true,
}: Omit<DialogProps, "open">) {
  const boxRef = useRef<HTMLDivElement>(null);
  // Read during the first render, before anything inside can take focus:
  // this is the element (usually the opener) focus goes back to on close.
  const [returnFocusTo] = useState(() => document.activeElement as HTMLElement | null);
  const id = useId();

  useEffect(() => {
    const box = boxRef.current!;
    const body = document.body;
    body.style.overflow = "hidden";
    // A child with autoFocus, or a nested dialog opened together with this
    // one (its effect runs first), has already taken focus; keep it there.
    // (A modal holding our own opener is the parent we were opened from.)
    let active = document.activeElement;
    const otherModal = active?.closest('[aria-modal="true"]');
    if (!box.contains(active) && !(otherModal && !otherModal.contains(returnFocusTo))) {
      (tabbables(box)[0] ?? box.querySelector<HTMLElement>("[role=dialog]") ?? box).focus();
    }
    return () => {
      body.style.overflow = "";
      // Hand focus back only if it was left in the dialog (or dropped to body
      // when the dialog left the DOM) — never steal it from where the caller
      // deliberately moved it while closing.
      // (Focusing an opener that has left the DOM does nothing.)
      active = document.activeElement;
      if (active === body || box.contains(active)) returnFocusTo?.focus();
    };
  }, [returnFocusTo]);

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const box = boxRef.current!;
    // Already dealt with by a dialog nested inside this one.
    if (handledKeys.has(e.nativeEvent)) return;
    handledKeys.add(e.nativeEvent);

    if (e.key === "Escape") {
      // An inner control (inline edit, popover, suggestion list) that already
      // handled Escape marks it so; the dialog stays open.
      if (e.defaultPrevented || !closeOnEscape) return;
      e.preventDefault();
      // Keep page-level Escape listeners (slide-overs, menus) behind the
      // modal from acting on the same key press.
      e.stopPropagation();
      onOpenChange(false);
    } else if (e.key === "Tab") {
      const items = tabbables(box);
      const target = e.target as Node;
      // Focus may be in something a child portaled out of the box (a date
      // popover): Tab moves freely inside that portal, and leaving its edge
      // comes back into the dialog instead of to the page behind.
      let scope = items;
      if (!box.contains(target)) {
        let root: Node | null = target;
        while (root && root.parentNode !== document.body) root = root.parentNode;
        scope = root ? tabbables(root as Element) : [];
      }
      const edge = e.shiftKey ? scope[0] : scope[scope.length - 1];
      if (target === edge || !scope.includes(target as HTMLElement)) {
        e.preventDefault();
        (e.shiftKey ? items[items.length - 1] : items[0])?.focus();
      }
    }
  };

  return createPortal(
    <DialogContext.Provider value={{ title: id + "t", description: id + "d" }}>
      <div
        className="fixed inset-0 z-50 flex items-center justify-center"
        onClick={(e) => e.target === e.currentTarget && onOpenChange(false)}
      >
        <div
          className="fixed inset-0 bg-black/50"
          onClick={closeOnBackdrop ? () => onOpenChange(false) : undefined}
          // A press on the backdrop must not drop focus to <body>: the
          // keyboard (Escape, Tab) would then no longer reach the dialog.
          onMouseDown={(e) => e.preventDefault()}
        />
        <div
          ref={boxRef}
          className={cn("relative z-50 w-full max-w-lg", className)}
          onKeyDown={onKeyDown}
        >
          {children}
        </div>
      </div>
    </DialogContext.Provider>,
    document.body,
  );
}

export const DialogContent = forwardRef<
  HTMLDivElement,
  HTMLAttributes<HTMLDivElement> & { onClose?: () => void; /** Accessible name of the close button. */ closeLabel?: string }
>(({ className, children, onClose, closeLabel, ...props }, ref) => {
  const ctx = useContext(DialogContext);
  // Inside a Dialog the content is the modal dialog itself. A caller's own
  // role / aria-* props still win (they are spread after these), and an
  // explicit aria-label is not overridden by the title reference.
  const dialogProps = ctx
    ? {
        role: "dialog",
        "aria-modal": true,
        "aria-labelledby": props["aria-label"] ? undefined : ctx.title,
        "aria-describedby": ctx.description,
        tabIndex: -1,
      }
    : {};
  return (
    <div
      ref={ref}
      className={cn(
        "relative mx-4 rounded-xl border border-border bg-card p-6 shadow-lg",
        ctx && "outline-none",
        className,
      )}
      {...dialogProps}
      {...props}
    >
      {children}
      {onClose && (
        <button
          type="button"
          onClick={onClose}
          aria-label={closeLabel}
          className="absolute right-4 top-4 rounded-sm opacity-70 hover:opacity-100"
        >
          <X className="h-4 w-4" />
        </button>
      )}
    </div>
  );
});
DialogContent.displayName = "DialogContent";

export function DialogHeader({
  className,
  ...props
}: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn("flex flex-col space-y-1.5 text-center sm:text-left", className)}
      {...props}
    />
  );
}

export function DialogTitle({
  className,
  id,
  ...props
}: HTMLAttributes<HTMLHeadingElement>) {
  const ctx = useContext(DialogContext);
  return (
    <h2
      // A caller's own id wins; it then points aria-labelledby at it itself.
      id={id ?? ctx?.title}
      className={cn("text-lg font-semibold leading-none tracking-tight", className)}
      {...props}
    />
  );
}

export function DialogDescription({
  className,
  id,
  ...props
}: HTMLAttributes<HTMLParagraphElement>) {
  const ctx = useContext(DialogContext);
  return (
    <p
      id={id ?? ctx?.description}
      className={cn("text-sm text-muted-foreground", className)}
      {...props}
    />
  );
}

export function DialogFooter({
  className,
  ...props
}: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn("flex flex-col-reverse sm:flex-row sm:justify-end sm:space-x-2 mt-4", className)}
      {...props}
    />
  );
}
