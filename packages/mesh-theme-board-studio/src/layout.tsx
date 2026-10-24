import { useMemo, useState } from "react";
import type { BoardLayoutView, FieldDescriptor } from "@entire-vc/mesh-theme-sdk";
import { lanes, say } from "./logic";

const byKeys = (all: FieldDescriptor[], keys: string[]): FieldDescriptor[] => {
  const map = new Map(all.map((f) => [f.key, f]));
  return keys.flatMap((k) => (map.has(k) ? [map.get(k)!] : []));
};

/** `board.layout` of Board Studio: the project's work as status bands, with a strip that puts one person's cards in the light. */
export const Layout: BoardLayoutView = ({ data, actions, themeSettings, ctx }) => {
  const { columns, components, dnd, emptyReason, closedPartial } = data;
  const locale = ctx.locale;
  const [focus, setFocus] = useState<string | null>(null);
  const fields = useMemo(() => byKeys(data.availableFields, data.cardFields), [data.availableFields, data.cardFields]);
  const list = columns.state === "loading" ? null : (columns.value ?? null);
  const people = useMemo(() => lanes((list ?? []).flatMap((c) => c.tasks)), [list]);
  // New identity only when the choice changes: the cards compare their settings by reference.
  const settings = useMemo(() => ({ ...themeSettings, studio_focus: focus }), [themeSettings, focus]);

  if (columns.state === "loading") {
    return (
      <div className="studio-board" data-density={themeSettings.density} aria-busy="true" role="status" aria-label={say(locale, "loading")}>
        {[0, 1, 2].map((i) => (
          <div key={i} className="studio-band studio-skeleton"><div className="studio-rail" /><div className="studio-drop"><div className="studio-ghost" /><div className="studio-ghost" /></div></div>
        ))}
      </div>
    );
  }

  return (
    <div className="studio-board" data-density={themeSettings.density}>
      {columns.state === "error" && (
        <div role="alert" className="studio-alert">
          <span>{columns.error.message}</span>
          <button type="button" className="studio-act" onClick={() => columns.retry()}>{say(locale, "retry")}</button>
        </div>
      )}
      {columns.state === "refreshing" && <p role="status" className="studio-note">{say(locale, "refreshing")}</p>}
      {closedPartial && list && (
        <p role="status" className="studio-note" data-testid="closed-partial-note">{say(locale, "closedPartial", { loaded: closedPartial.loaded, total: closedPartial.total })}</p>
      )}
      {people.length > 1 && (
        <div className="studio-people" role="group" aria-label={say(locale, "focusOn")}>
          <button type="button" className="studio-chip" aria-pressed={focus === null} onClick={() => setFocus(null)}>{say(locale, "everyone")}</button>
          {people.map((p) => (
            <button key={p.key} type="button" className="studio-chip" aria-pressed={focus === p.key} onClick={() => setFocus(focus === p.key ? null : p.key)}>
              {p.name || say(locale, "unassigned")} <b>{p.count}</b>
            </button>
          ))}
        </div>
      )}
      {list?.map((column, index) => (
        <components.Column
          key={column.id}
          data={{ column, index, total: list.length, dnd, fields, components: { Card: components.Card } }}
          actions={{
            createTask: column.status && actions.createTask ? () => actions.createTask?.(column.id) : undefined,
            loadMore: () => actions.loadMore(column.id),
            openTask: actions.openTask,
            quickEdit: actions.quickEdit,
            moveTask: (taskId, position) => actions.moveTask(taskId, column.id, position),
          }}
          themeSettings={settings}
          ctx={ctx}
        />
      ))}
      {list && list.length === 0 && (
        <p className="studio-empty-board">{say(locale, emptyReason === "no_statuses" ? "noStatuses" : "noMatches")}</p>
      )}
    </div>
  );
};
