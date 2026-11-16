// mesh-default view for dashboard.layout v2: a CSS grid placed by x, y, w, h. No grid library.
import { Component, useEffect, useState, type CSSProperties, type ReactNode } from "react";
import { Link } from "react-router";
import { useTranslation } from "react-i18next";
import { Maximize2, PackageX } from "lucide-react";
import { Widget } from "..";
import { WIDGETS } from "../registry";
import { useThemeWidgets } from "../theme-widgets";
import { Button } from "@/components/ui/button";
import { ExtensionWidgetCard } from "@/components/extensions/widgets/extension-widget";
import { isExtensionWidgetType } from "@/lib/extension-widgets";
import {
  GAP_PX,
  ROW_PX,
  coreWidgetId,
  toSdkInstance,
  colsForWidth,
  placeWidgets,
  spanPx,
  type Cols,
  type DashboardDoc,
  type Placed,
  type WidgetInstance,
} from "./model";

export function useCols(): Cols {
  const read = () => colsForWidth(window.innerWidth);
  const [cols, setCols] = useState<Cols>(read);
  useEffect(() => {
    const on = () => setCols(read());
    window.addEventListener("resize", on);
    on();
    return () => window.removeEventListener("resize", on);
  }, []);
  return cols;
}

class CellBoundary extends Component<{ fallback: ReactNode; children: ReactNode }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  componentDidCatch(e: unknown) {
    console.error("dashboard cell failed", e);
  }
  render() {
    return this.state.failed ? this.props.fallback : this.props.children;
  }
}

/** Unknown type or a switched-off extension (PRD §7.2): the title stays, the owner can remove it. */
export function WidgetStub({ type, onRemove }: { type: string; onRemove?: () => void }) {
  const { t } = useTranslation("notifications");
  return (
    <div
      role="group"
      aria-label={type}
      data-testid="widget-stub"
      className="flex h-full min-h-24 flex-col items-center justify-center gap-2 rounded-xl border border-dashed border-border bg-muted/30 p-4 text-center"
    >
      <PackageX className="h-5 w-5 text-muted-foreground" aria-hidden />
      <p className="max-w-full truncate text-sm font-medium">{type}</p>
      <p className="text-xs text-muted-foreground">{t("dashboard.page.stubHint")}</p>
      {onRemove && (
        <Button type="button" size="sm" variant="outline" onClick={onRemove}>
          {t("dashboard.page.removeWidget")}
        </Button>
      )}
    </div>
  );
}

export function WidgetCell({
  instance,
  dashboardId,
  expandTo,
  onRemove,
}: {
  instance: WidgetInstance;
  /** The dashboard the instance lives on: an extension's iframe widget names it in its token. */
  dashboardId?: string;
  expandTo?: string;
  onRemove?: () => void;
}) {
  const { t } = useTranslation("notifications");
  const id = coreWidgetId(instance.widget_type);
  const theme = useThemeWidgets();
  const known = [...WIDGETS, ...theme].some((d) => d.id === id);
  // `<ext_id>.<widget_id>` that no core or theme descriptor claims: the extension draws it, or shows its own stub.
  const extension = !known && isExtensionWidgetType(instance.widget_type);
  if (!known && !extension) return <WidgetStub type={instance.widget_type} onRemove={onRemove} />;
  return (
    <div className="group/cell relative flex min-h-0 flex-1 flex-col">
      {/* The card fills the cell and scrolls inside its own border; the expand badge stays outside the scroller, unclipped. */}
      <div className="flex min-h-0 flex-1 flex-col [&>[data-mesh-widget]>*]:min-h-0 [&>[data-mesh-widget]>*]:flex-1 [&>[data-mesh-widget]>*]:overflow-auto">
        {/* A crashing view must not take the sibling cells down; Widget has its own boundary, this one catches the rest. */}
        <CellBoundary fallback={<WidgetStub type={instance.widget_type} onRemove={onRemove} />}>
          {extension ? (
            <ExtensionWidgetCard
              type={instance.widget_type}
              settings={instance.settings ?? {}}
              size={{ w: instance.w, h: instance.h }}
              dashboardId={dashboardId}
              instanceId={instance.id}
              onRemove={onRemove}
            />
          ) : (
            <Widget instance={toSdkInstance(instance)} />
          )}
        </CellBoundary>
      </div>
      {/* On the card's corner, in the grid gap: inside the card it covered the widget's own header controls. */}
      {expandTo && (
        <Link
          to={expandTo}
          aria-label={t("dashboard.page.expand")}
          title={t("dashboard.page.expand")}
          className="absolute -right-2 -top-2 z-10 inline-flex h-7 w-7 items-center justify-center rounded-full border bg-background text-muted-foreground opacity-100 shadow-sm hover:bg-muted hover:text-foreground focus-visible:opacity-100 md:opacity-0 md:group-hover/cell:opacity-100 md:group-focus-within/cell:opacity-100 print:hidden"
        >
          <Maximize2 className="h-3.5 w-3.5" aria-hidden />
        </Link>
      )}
    </div>
  );
}

/**
 * Paper is laid out as the desktop grid whatever the window was: the 12-column cell rides along as CSS variables
 * that only the print rules in index.css read, so window.print(), Ctrl+P and a headless PDF all get it without a
 * resize or a re-render.
 */
function printVars(p: Placed): CSSProperties {
  return { "--print-col": `${p.colStart} / ${p.colEnd}`, "--print-row": `${p.rowStart} / span ${p.rowSpan}` } as CSSProperties;
}

function cellStyle(p: Placed, cols: Cols, print: Placed): CSSProperties {
  const vars = printVars(print);
  if (cols === 1) return { ...vars, minHeight: spanPx(p.rowSpan) };
  return { ...vars, gridColumn: `${p.colStart} / ${p.colEnd}`, gridRow: `${p.rowStart} / span ${p.rowSpan}` };
}

export function ViewGrid({
  doc,
  expandHref,
  onRemove,
}: {
  doc: DashboardDoc;
  expandHref: (instanceId: string) => string;
  onRemove?: (instanceId: string) => void;
}) {
  const cols = useCols();
  const placed = placeWidgets(doc.widgets, cols);
  const onPaper = new Map(placeWidgets(doc.widgets, 12).map((p) => [p.widget.id, p]));
  const style: CSSProperties =
    cols === 1
      ? { display: "flex", flexDirection: "column", gap: GAP_PX }
      : { display: "grid", gridTemplateColumns: `repeat(${cols}, minmax(0, 1fr))`, gridAutoRows: ROW_PX, gap: GAP_PX };
  return (
    <div data-testid="dashboard-grid" data-cols={cols} data-print-grid style={style}>
      {placed.map((p) => (
        <div key={p.widget.id} data-widget-instance={p.widget.id} style={cellStyle(p, cols, onPaper.get(p.widget.id) ?? p)} className="flex min-h-0 min-w-0 flex-col">
          <WidgetCell
            instance={p.widget}
            dashboardId={doc.id}
            expandTo={expandHref(p.widget.id)}
            onRemove={doc.can_edit && onRemove ? () => onRemove(p.widget.id) : undefined}
          />
        </div>
      ))}
    </div>
  );
}
