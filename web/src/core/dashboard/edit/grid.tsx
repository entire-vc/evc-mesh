// The only file that imports react-grid-layout. It lives in the edit chunk: view mode never loads it.
import { useMemo } from "react";
import ReactGridLayout, { useContainerWidth, verticalCompactor, type LayoutItem as RglItem } from "react-grid-layout";
import "react-grid-layout/css/styles.css";
import { applyGeometry } from "./model";
import type { CatalogType, LayoutItem } from "./types";

interface GridProps {
  items: LayoutItem[];
  types: Map<string, CatalogType>;
  cols: number;
  onLayout: (next: LayoutItem[]) => void;
  renderItem: (item: LayoutItem) => React.ReactNode;
}

// jsdom has no layout: a measured width of 0 would collapse every cell, so tests get a nominal one.
const FALLBACK_WIDTH = 1200;

export function EditGrid({ items, types, cols, onLayout, renderItem }: GridProps) {
  const { width, containerRef } = useContainerWidth();
  const layout = useMemo<RglItem[]>(
    () =>
      items.map((it) => {
        const t = types.get(it.type);
        return {
          i: it.id, x: it.x, y: it.y, w: it.w, h: it.h,
          minW: Math.min(t?.size.min.w ?? 1, cols), minH: t?.size.min.h ?? 1,
          maxW: Math.min(t?.size.max.w ?? cols, cols), maxH: t?.size.max.h,
        };
      }),
    [items, types, cols],
  );
  const commit = (l: readonly RglItem[]) => onLayout(applyGeometry(items, l, types, cols));
  return (
    <div ref={containerRef} data-testid="dash-edit-grid">
      {(
        <ReactGridLayout
          width={width || FALLBACK_WIDTH}
          layout={layout}
          gridConfig={{ cols, rowHeight: 72, margin: [16, 16], containerPadding: [0, 0], maxRows: Infinity }}
          dragConfig={{ enabled: true, bounded: false, handle: ".dash-drag", cancel: ".dash-nodrag", threshold: 3 }}
          resizeConfig={{ enabled: true, handles: ["se"] }}
          compactor={verticalCompactor}
          onDragStop={(l) => commit(l)}
          onResizeStop={(l) => commit(l)}
        >
          {items.map((it) => (
            <div key={it.id} data-testid={`dash-item-${it.id}`}>
              {renderItem(it)}
            </div>
          ))}
        </ReactGridLayout>
      )}
    </div>
  );
}
