import { useMemo } from "react";
import { Columns3 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { resolveCardFields } from "@/core/slots/board-data";
import type { BoardLayoutView } from "@/core/slots/types";

/** `board.layout` of `mesh-default`: horizontal columns, left to right. */
export const BoardLayout: BoardLayoutView = ({ data, actions, themeSettings, ctx }) => {
  const { t } = useTranslation("board");
  const { columns, components, dnd, emptyReason, closedPartial } = data;
  const fields = useMemo(() => resolveCardFields(data.availableFields, data.cardFields), [data.availableFields, data.cardFields]);
  const list = columns.state === "loading" ? null : (columns.value ?? null);

  if (columns.state === "loading") {
    return (
      <div className="flex gap-4 overflow-x-auto pb-4">
        {Array.from({ length: 4 }).map((_, i) => (
          <div key={i} className="w-72 shrink-0 space-y-3">
            <Skeleton className="h-8 w-full rounded-lg" />
            <Skeleton className="h-24 w-full rounded-lg" />
            <Skeleton className="h-24 w-full rounded-lg" />
          </div>
        ))}
      </div>
    );
  }

  return (
    <>
      {columns.state === "error" && (
        <div role="alert" className="flex flex-wrap items-center gap-3 rounded-lg border border-destructive/40 p-3 text-sm">
          <span>{columns.error.message}</span>
          <Button variant="outline" size="sm" onClick={() => columns.retry()}>
            {t("refresh.retry")}
          </Button>
        </div>
      )}
      {columns.state === "refreshing" && (
        <p role="status" className="text-xs text-muted-foreground">
          {t("refresh.inProgress")}
        </p>
      )}
      {closedPartial && list && (
        <p role="status" className="text-xs text-muted-foreground" data-testid="closed-partial-note">
          {t("closedPartial", { loaded: closedPartial.loaded, total: closedPartial.total })}
        </p>
      )}
      {list && (
        <div className="flex gap-4 overflow-x-auto pb-4">
          {list.map((column, index) => (
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
              themeSettings={themeSettings}
              ctx={ctx}
            />
          ))}

          {list.length === 0 && (
            <div className="flex w-full items-center justify-center py-12">
              <div className="text-center">
                <Columns3 className="mx-auto mb-4 h-12 w-12 text-muted-foreground" />
                <h3 className="mb-2 text-lg font-semibold">
                  {emptyReason === "no_statuses" ? t("empty.noStatuses.title") : t("empty.noMatches.title")}
                </h3>
                <p className="text-sm text-muted-foreground">
                  {emptyReason === "no_statuses" ? t("empty.noStatuses.hint") : t("empty.noMatches.hint")}
                </p>
              </div>
            </div>
          )}
        </div>
      )}
    </>
  );
};
