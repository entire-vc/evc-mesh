import type { KeyboardEvent, ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ArrowDown, ArrowUp, GripVertical, Settings, X } from "lucide-react";
import { cn } from "@/lib/cn";

export interface FrameProps {
  title: string;
  children: ReactNode;
  onSettings: () => void;
  onRemove: () => void;
  /** Desktop: keyboard moves (arrows) and resizes (Shift+arrows) of the focused handle. */
  onKeyMove?: (dx: number, dy: number, resize: boolean) => void;
  /** Phone: reorder buttons instead of a drag handle. */
  onUp?: () => void;
  onDown?: () => void;
  mobile?: boolean;
}

const iconBtn =
  "dash-nodrag inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-40 disabled:pointer-events-none";

/** One widget in edit mode: title bar is the drag handle; ⚙ and × sit in its corner. */
export function WidgetFrame({ title, children, onSettings, onRemove, onKeyMove, onUp, onDown, mobile }: FrameProps) {
  const { t } = useTranslation("dashboardEdit");
  const onKey = (e: KeyboardEvent<HTMLDivElement>) => {
    if (!onKeyMove) return;
    const d: Record<string, [number, number]> = { ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1] };
    const v = d[e.key];
    if (!v) return;
    e.preventDefault();
    onKeyMove(v[0], v[1], e.shiftKey);
  };
  return (
    <div className="@container flex h-full flex-col overflow-hidden rounded-lg border border-border bg-card text-card-foreground shadow-sm ring-1 ring-primary/20">
      <div className="flex items-center gap-1 border-b border-border bg-muted/40 pl-1 pr-1">
        {mobile ? (
          <>
            <span className="min-w-0 flex-1 truncate px-2 text-sm font-medium">{title}</span>
            <button type="button" className={iconBtn} onClick={onUp} disabled={!onUp} aria-label={t("moveUp", { defaultValue: "Move up: {{title}}", title })}>
              <ArrowUp className="h-4 w-4" aria-hidden />
            </button>
            <button type="button" className={iconBtn} onClick={onDown} disabled={!onDown} aria-label={t("moveDown", { defaultValue: "Move down: {{title}}", title })}>
              <ArrowDown className="h-4 w-4" aria-hidden />
            </button>
          </>
        ) : (
          <div
            role="button"
            tabIndex={0}
            onKeyDown={onKey}
            aria-label={t("dragHandle", { defaultValue: "Move {{title}}: arrow keys move, Shift plus arrow keys resize", title })}
            data-testid="dash-drag-handle"
            className={cn("dash-drag flex min-w-0 flex-1 cursor-grab items-center gap-1 rounded-md px-1 py-1.5 active:cursor-grabbing", "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring")}
          >
            <GripVertical className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
            <span className="truncate text-sm font-medium">{title}</span>
          </div>
        )}
        <button type="button" className={iconBtn} onClick={onSettings} aria-label={t("settings", { defaultValue: "Settings: {{title}}", title })}>
          <Settings className="h-4 w-4" aria-hidden />
        </button>
        <button type="button" className={iconBtn} onClick={onRemove} aria-label={t("remove", { defaultValue: "Remove: {{title}}", title })}>
          <X className="h-4 w-4" aria-hidden />
        </button>
      </div>
      <div className="dash-nodrag pointer-events-none min-h-0 flex-1 overflow-hidden p-2" aria-hidden={false}>
        {children}
      </div>
    </div>
  );
}
