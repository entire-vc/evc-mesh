import {
  type ButtonHTMLAttributes,
  type HTMLAttributes,
  type MouseEvent as ReactMouseEvent,
  type ReactElement,
  type ReactNode,
  cloneElement,
  createContext,
  isValidElement,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { cn } from "@/lib/cn";

interface DropdownMenuContextValue {
  open: boolean;
  setOpen: (open: boolean) => void;
}

const DropdownMenuContext = createContext<DropdownMenuContextValue>({
  open: false,
  setOpen: () => {},
});

export function DropdownMenu({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    function handleClickOutside(event: MouseEvent) {
      if (ref.current && !ref.current.contains(event.target as Node)) {
        setOpen(false);
      }
    }
    document.addEventListener("mousedown", handleClickOutside);
    return () => document.removeEventListener("mousedown", handleClickOutside);
  }, []);

  return (
    <DropdownMenuContext.Provider value={{ open, setOpen }}>
      <div ref={ref} className="relative inline-block text-left">
        {children}
      </div>
    </DropdownMenuContext.Provider>
  );
}

export function DropdownMenuTrigger({
  children,
  asChild = false,
  className,
  onClick,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  asChild?: boolean;
}) {
  const { open, setOpen } = useContext(DropdownMenuContext);
  const toggle = useCallback(
    (event: ReactMouseEvent<HTMLElement>) => {
      onClick?.(event as ReactMouseEvent<HTMLButtonElement>);
      setOpen(!open);
    },
    [open, setOpen, onClick],
  );

  if (asChild) {
    if (!isValidElement(children)) {
      if (import.meta.env.DEV) {
        console.error(
          "DropdownMenuTrigger: asChild requires a single valid React element child.",
        );
      }
      return null;
    }
    // Hand our props to the child instead of wrapping it in our own <button> —
    // a wrapped Button rendered <button><button/></button>, invalid HTML.
    const child = children as ReactElement<HTMLAttributes<HTMLElement>>;
    return cloneElement(child, {
      ...props,
      className: cn(child.props.className, className),
      onClick: (event: ReactMouseEvent<HTMLElement>) => {
        child.props.onClick?.(event);
        toggle(event);
      },
    });
  }

  return (
    <button type="button" className={className} onClick={toggle} {...props}>
      {children}
    </button>
  );
}

const VIEWPORT_MARGIN = 8;

export function DropdownMenuContent({
  className,
  align = "end",
  style,
  ...props
}: HTMLAttributes<HTMLDivElement> & { align?: "start" | "end" }) {
  const { open } = useContext(DropdownMenuContext);
  const ref = useRef<HTMLDivElement>(null);
  const [shift, setShift] = useState(0);

  // Keep the panel inside the viewport: an absolutely positioned menu opened
  // near the right edge (e.g. the tab bar "⋮" at 393px) was clipped.
  useLayoutEffect(() => {
    if (!open || !ref.current) return;
    const el = ref.current;
    const rect = el.getBoundingClientRect();
    const vw = document.documentElement.clientWidth;
    let dx = 0;
    if (rect.right > vw - VIEWPORT_MARGIN) dx = vw - VIEWPORT_MARGIN - rect.right;
    if (rect.left + dx < VIEWPORT_MARGIN) dx = VIEWPORT_MARGIN - rect.left;
    setShift(dx);
    return () => setShift(0);
  }, [open]);

  if (!open) return null;

  return (
    <div
      ref={ref}
      className={cn(
        "absolute z-50 mt-2 min-w-[8rem] max-w-[calc(100vw-1rem)] overflow-hidden rounded-lg border border-border bg-popover p-1 text-popover-foreground shadow-lg",
        align === "end" ? "right-0" : "left-0",
        className,
      )}
      style={
        shift
          ? {
              ...style,
              // translate first so it is applied in viewport coordinates, not the caller's scaled space
              transform: [`translateX(${shift}px)`, style?.transform].filter(Boolean).join(" "),
            }
          : style
      }
      {...props}
    />
  );
}

export function DropdownMenuItem({
  className,
  disabled,
  ...props
}: HTMLAttributes<HTMLDivElement> & { disabled?: boolean }) {
  const { setOpen } = useContext(DropdownMenuContext);
  return (
    <div
      role="menuitem"
      {...props}
      aria-disabled={disabled ? true : props["aria-disabled"]}
      className={cn(
        "relative flex cursor-pointer select-none items-center rounded-md px-2 py-1.5 text-sm outline-none hover:bg-accent hover:text-accent-foreground",
        disabled && "pointer-events-none opacity-50",
        className,
      )}
      // Spread first: with {...props} last, an item's own onClick replaced this
      // one and the menu stayed open after every click.
      // A disabled item swallows the click: aria-disabled alone does not stop it.
      onClick={(e) => {
        if (disabled) return;
        props.onClick?.(e);
        setOpen(false);
      }}
    />
  );
}

export function DropdownMenuSeparator({
  className,
  ...props
}: HTMLAttributes<HTMLDivElement>) {
  return (
    <div className={cn("-mx-1 my-1 h-px bg-border", className)} {...props} />
  );
}

export function DropdownMenuLabel({
  className,
  ...props
}: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn("px-2 py-1.5 text-sm font-semibold", className)}
      {...props}
    />
  );
}
