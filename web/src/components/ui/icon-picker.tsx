import { type ComponentType, Suspense, useCallback, useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Smile } from "lucide-react";
import { cn } from "@/lib/cn";
import { EntityIcon } from "./entity-icon";
import type { IconPickerPopoverProps } from "./icon-picker-popover";

export interface IconPickerProps {
  /** Current value: an emoji, `icon:<name>`, a legacy text icon, or "" for none. */
  value: string;
  /** Called with the chosen emoji or `icon:<name>`, or "" for «no icon». */
  onChange: (value: string) => void;
  id?: string;
  disabled?: boolean;
  className?: string;
  /** Accessible name; defaults to the localized «Choose icon». */
  "aria-label"?: string;
}

type PopoverComponent = ComponentType<IconPickerPopoverProps>;
let popoverPromise: Promise<PopoverComponent> | null = null;

// Only this trigger ships in the main bundle; the popover, the panel and the
// catalogue load on first open (or on hover/focus of the trigger).
function loadPopover(): Promise<PopoverComponent> {
  popoverPromise ??= import("./icon-picker-popover").then(
    (m) => m.default,
    (err: unknown) => {
      popoverPromise = null; // a failed load can be retried by the next open
      throw err;
    },
  );
  return popoverPromise;
}

export function IconPicker({
  value,
  onChange,
  id,
  disabled,
  className,
  "aria-label": ariaLabel,
}: IconPickerProps) {
  const { t } = useTranslation("common");
  const [open, setOpen] = useState(false);
  const [Popover, setPopover] = useState<PopoverComponent | null>(null);
  const [failed, setFailed] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const popId = useId();

  const toggle = () => {
    if (open) {
      setOpen(false);
      return;
    }
    setFailed(false);
    setOpen(true);
    loadPopover().then(
      (C) => setPopover(() => C),
      () => {
        setFailed(true);
        setOpen(false);
      },
    );
  };

  const dismiss = useCallback(({ restoreFocus }: { restoreFocus: boolean }) => {
    setOpen(false);
    if (restoreFocus) triggerRef.current?.focus();
  }, []);

  const label = ariaLabel ?? t("iconPicker.trigger");

  return (
    <>
      <button
        ref={triggerRef}
        id={id}
        type="button"
        disabled={disabled}
        aria-label={label}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls={open ? popId : undefined}
        aria-busy={open && !Popover ? true : undefined}
        title={failed ? t("iconPicker.loadFailed") : undefined}
        onMouseEnter={() => void loadPopover().catch(() => {})}
        onFocus={() => void loadPopover().catch(() => {})}
        onClick={toggle}
        className={cn(
          "inline-flex h-10 min-w-10 items-center justify-center gap-1 rounded-md border border-input bg-background px-2 text-xl leading-none",
          "hover:bg-accent focus:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50",
          failed && "border-destructive",
          className,
        )}
      >
        <EntityIcon value={value} fallback={Smile} className="h-5 w-5" />
      </button>
      {open && Popover && (
        <Suspense fallback={null}>
          <Popover
            anchorRef={triggerRef}
            id={popId}
            label={label}
            value={value}
            onSelect={(v) => {
              onChange(v);
              dismiss({ restoreFocus: true });
            }}
            onDismiss={dismiss}
          />
        </Suspense>
      )}
    </>
  );
}
