import { memo, useMemo, useState } from "react";
import { DropZone } from "@entire-vc/mesh-theme-sdk";
import type { BoardCardView, BoardColumnActions, BoardColumnData, BoardColumnView, BoardLayoutSettings, FieldDescriptor, QuickEditPatch, SlotContext, TaskData } from "@entire-vc/mesh-theme-sdk";
import { say, wip } from "./logic";

interface BandCardProps {
  Card: BoardCardView;
  task: TaskData;
  column: BoardColumnData;
  fields: FieldDescriptor[];
  openTask: BoardColumnActions["openTask"];
  quickEdit?: BoardColumnActions["quickEdit"];
  themeSettings: BoardLayoutSettings;
  ctx: SlotContext;
}

/** One card of the band; a card whose own inputs did not change stays out of the commit. */
const BandCard = memo(function BandCard({ Card, task, column, fields, openTask, quickEdit, themeSettings, ctx }: BandCardProps) {
  const data = useMemo(
    () => ({ task, column: { id: column.id, kind: column.kind, statusCategory: column.status?.category }, gateStatusName: column.gateStatusNameByTask?.[task.id], fields, selected: false, isOverlay: false }),
    [task, column.id, column.kind, column.status?.category, column.gateStatusNameByTask, fields],
  );
  const actions = useMemo(
    () => ({
      openTask: (opts?: { newTab?: boolean }) => openTask(task.id, opts),
      quickEdit: quickEdit ? (patch: QuickEditPatch) => quickEdit(task.id, patch) : undefined,
    }),
    [openTask, quickEdit, task.id],
  );
  return <Card data={data} actions={actions} themeSettings={themeSettings} ctx={ctx} />;
});

/** `board.column` of Board Studio: a status as a horizontal band, its cards flowing in a grid. */
export const Column: BoardColumnView = ({ data, actions, themeSettings, ctx }) => {
  const { column, dnd, fields, components } = data;
  const locale = ctx.locale;
  const ids = useMemo(() => column.tasks.map((t) => t.id), [column.tasks]);
  const [busy, setBusy] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const state = themeSettings.show_wip === false ? "none" : wip(column.totalCount, column.wipLimit);
  return (
    <section className="studio-band" data-column={column.id} data-wip={state} aria-label={column.title}>
      <header className="studio-rail">
        <span className="studio-dot" style={column.color ? { backgroundColor: column.color } : undefined} aria-hidden="true" />
        <h2 className="studio-status">{column.title}</h2>
        <span className="studio-total" data-testid="column-count">{column.countLabel}</span>
        {state !== "none" && column.wipLimit != null && (
          <span className="studio-wip" title={state === "over" ? say(locale, "overLimit", { limit: column.wipLimit }) : undefined}>
            {column.totalCount}/{column.wipLimit}
          </span>
        )}
        {actions.createTask && (
          <button type="button" className="studio-add" onClick={() => actions.createTask?.()} aria-label={`${say(locale, "add")}: ${column.title}`}>+</button>
        )}
      </header>
      <DropZone
        zone={{ kind: "status", id: column.id }}
        orientation="horizontal"
        itemIds={ids}
        enabled={dnd.enabled && column.kind === "status"}
        data-testid="board-column"
        data-column-id={column.id}
        data-status-id={column.status?.id ?? ""}
        className={({ isOver }) => `studio-drop${isOver ? " is-over" : ""}`}
      >
        <>
          {column.tasks.map((task) => (
            <BandCard key={task.id} Card={components.Card} task={task} column={column} fields={fields} openTask={actions.openTask} quickEdit={actions.quickEdit} themeSettings={themeSettings} ctx={ctx} />
          ))}
          {column.tasks.length === 0 && <p className="studio-empty">{say(locale, "emptyBand")}</p>}
        </>
      </DropZone>
      {column.hasMore && column.closedSlice && (
        <div className="studio-more" data-testid="closed-slice">
          <span>{say(locale, "closedPartial", { loaded: column.closedSlice.loaded, total: column.closedSlice.total })}</span>
          {loadError && <span role="alert" className="studio-error">{loadError}</span>}
          <button
            type="button"
            className="studio-act"
            disabled={busy || column.loadingMore}
            onClick={() => {
              setBusy(true);
              setLoadError(null);
              actions.loadMore().then(() => setBusy(false), (e: unknown) => { setBusy(false); setLoadError(e instanceof Error ? e.message : String(e)); });
            }}
          >
            {busy || column.loadingMore ? say(locale, "loading") : say(locale, "loadMore")}
          </button>
        </div>
      )}
    </section>
  );
};
