import { memo, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { AlertTriangle, Flag, Plus } from "lucide-react";
import { AgentShortTagView } from "@/components/agent-short-tag";
import { AssigneeAvatarView } from "@/components/assignee-avatar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DropZone } from "@/core/primitives";
import { apiErrorMessage } from "@/lib/api-error";
import type {
  BoardCardView,
  BoardColumnActions,
  BoardColumnData,
  BoardColumnView,
  BoardLayoutSettings,
  FieldDescriptor,
  Priority,
  QuickEditPatch,
  SlotContext,
  StatusCategory,
  TaskData,
} from "@/core/slots/types";

const PRIORITY_COLORS: Record<Priority, string> = {
  urgent: "var(--priority-fill-urgent)",
  high: "var(--priority-fill-high)",
  medium: "var(--priority-fill-medium)",
  low: "var(--priority-fill-low)",
  none: "var(--fallback-gray-400)",
};

function PriorityIcon({ priority }: { priority: Priority }) {
  const Icon = priority === "urgent" ? AlertTriangle : Flag;
  return <Icon className="h-3.5 w-3.5" style={{ color: PRIORITY_COLORS[priority] }} />;
}

/** "Showing latest N of TOTAL" + an explicit Load more (never automatic). */
function ClosedSliceFooter({
  slice,
  loadingMore,
  onLoadMore,
}: {
  slice: { loaded: number; total: number; filtersActive: boolean };
  loadingMore: boolean;
  onLoadMore: () => Promise<void>;
}) {
  const { t } = useTranslation("board");
  // null = idle, "" = loading, otherwise the last error.
  const [state, setState] = useState<string | null>(null);
  const busy = state === "" || loadingMore;
  return (
    <div className="mt-2 space-y-1 text-center text-xs text-muted-foreground" data-testid="closed-slice">
      <p>
        {t("column.closedSlice.showing", { loaded: slice.loaded, total: slice.total })}
        {slice.filtersActive ? t("column.closedSlice.filtersApply") : ""}
      </p>
      {state && (
        <p role="alert" className="text-destructive">
          {state}
        </p>
      )}
      <Button
        variant="outline"
        size="sm"
        disabled={busy}
        onClick={() => {
          setState("");
          onLoadMore().then(
            () => setState(null),
            (e: unknown) => setState(apiErrorMessage(e, t("column.closedSlice.requestFailed"))),
          );
        }}
      >
        {busy ? t("column.closedSlice.loading") : t("column.closedSlice.loadMore")}
      </Button>
    </div>
  );
}

interface ColumnCardProps {
  Card: BoardCardView;
  task: TaskData;
  columnId: string;
  columnKind: BoardColumnData["kind"];
  statusCategory?: StatusCategory;
  gateStatusName?: string;
  fields: FieldDescriptor[];
  openTask: BoardColumnActions["openTask"];
  quickEdit?: BoardColumnActions["quickEdit"];
  themeSettings: BoardLayoutSettings;
  ctx: SlotContext;
}

/** One card of the column. Memoized: a card whose own inputs did not change stays out of the commit (opening a task re-renders the board around it). */
const ColumnCard = memo(function ColumnCard({ Card, task, columnId, columnKind, statusCategory, gateStatusName, fields, openTask, quickEdit, themeSettings, ctx }: ColumnCardProps) {
  const data = useMemo(
    () => ({ task, column: { id: columnId, kind: columnKind, statusCategory }, gateStatusName, fields, selected: false, isOverlay: false }),
    [task, columnId, columnKind, statusCategory, gateStatusName, fields],
  );
  const cardActions = useMemo(
    () => ({
      openTask: (opts?: { newTab?: boolean }) => openTask(task.id, opts),
      quickEdit: quickEdit ? (patch: QuickEditPatch) => quickEdit(task.id, patch) : undefined,
    }),
    [openTask, quickEdit, task.id],
  );
  return <Card data={data} actions={cardActions} themeSettings={themeSettings} ctx={ctx} />;
});

/** `board.column` of `mesh-default`. */
export const BoardColumn: BoardColumnView = ({ data, actions, themeSettings, ctx }) => {
  const { t } = useTranslation("board");
  const { column, dnd, fields, components } = data;
  const { Card } = components;
  const taskIds = useMemo(() => column.tasks.map((task) => task.id), [column.tasks]);
  const accepting = dnd.enabled && column.kind === "status";
  const header = column.group;

  return (
    <div className="w-72 shrink-0">
      <div className="mb-3 flex items-center justify-between">
        <div className="flex items-center gap-2">
          {header?.priority ? (
            <PriorityIcon priority={header.priority} />
          ) : header?.assignee ? (
            <AssigneeAvatarView name={header.assignee.name} shortTag={header.assignee.shortTag ?? null} type={header.assignee.type} size="sm" />
          ) : (
            <div className="h-2.5 w-2.5 rounded-full" style={{ backgroundColor: column.color }} />
          )}
          <span className="text-sm font-semibold">
            {column.title}
            <AgentShortTagView tag={header?.assignee?.shortTag ?? null} type={header?.assignee?.type} />
          </span>
          <Badge variant="secondary" className="text-xs" data-testid="column-count">
            {column.countLabel}
          </Badge>
        </div>
        {actions.createTask && (
          <Button
            variant="ghost"
            size="icon"
            className="h-11 w-11 md:h-6 md:w-6"
            onClick={() => actions.createTask?.()}
            title={t("column.addTask")}
          >
            <Plus className="h-3 w-3" />
          </Button>
        )}
      </div>

      {/* Triage and group columns carry the zone too, switched off: their cards still know where they came from. */}
      <DropZone
        zone={{ kind: "status", id: column.id }}
        orientation="vertical"
        itemIds={taskIds}
        enabled={accepting}
        data-testid="board-column"
        data-column-id={column.id}
        data-status-id={column.status?.id ?? ""}
        className={({ isOver }) =>
          "min-h-[60px] space-y-2 rounded-xl bg-muted/50 p-2 transition-colors" +
          (isOver ? " ring-2 ring-primary/30 bg-muted/70" : "") +
          (!dnd.enabled ? " opacity-90" : "")
        }
      >
        <>
          {column.tasks.map((task) => (
            <ColumnCard
              key={task.id}
              Card={Card}
              task={task}
              columnId={column.id}
              columnKind={column.kind}
              statusCategory={column.status?.category}
              gateStatusName={column.gateStatusNameByTask?.[task.id]}
              fields={fields}
              openTask={actions.openTask}
              quickEdit={actions.quickEdit}
              themeSettings={themeSettings}
              ctx={ctx}
            />
          ))}
        </>

        {column.tasks.length === 0 && (
          <p className="py-4 text-center text-xs text-muted-foreground">{t("column.empty")}</p>
        )}
      </DropZone>
      {column.hasMore && column.closedSlice && (
        <ClosedSliceFooter slice={column.closedSlice} loadingMore={column.loadingMore} onLoadMore={actions.loadMore} />
      )}
    </div>
  );
};
