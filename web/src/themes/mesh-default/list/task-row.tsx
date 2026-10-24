import { useEffect, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { AlignLeft, ChevronDown, ChevronRight, CircleCheck, GitBranch, Loader2, Lock, Paperclip, Pencil, Snowflake } from "lucide-react";
import { AssigneeAvatar } from "@/components/assignee-avatar";
import { CustomFieldRenderer } from "@/components/custom-field-renderer";
import { PriorityFlag } from "@/components/priority-flag";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { TaskLink } from "@entire-vc/mesh-theme-sdk";
import type { TaskRowActions, TaskRowColumn, TaskRowData, TaskRowView } from "@/core/slots/types";
import { cn } from "@/lib/cn";
import { formatDate } from "@/lib/utils";
import type { Priority } from "@/types";

const PRIORITY_OPTIONS: Priority[] = ["urgent", "high", "medium", "low", "none"];

export function getDateClass(dueDate: string | null | undefined, statusCategory: string | undefined): string {
  if (!dueDate) return "text-muted-foreground";
  const isTerminal = statusCategory === "done" || statusCategory === "cancelled";
  if (!isTerminal && new Date(dueDate) < new Date()) {
    return "text-tone-danger-500 font-medium";
  }
  return "text-muted-foreground";
}

// What a cell gets: the row's data, the actions the viewer may use, and which cell (if any) is being edited.
interface CellProps {
  data: TaskRowData;
  actions: TaskRowActions;
  isEditing: boolean;
  startEdit(): void;
  stopEdit(): void;
}

// A cell that opens an editor on click; keyboard users reach it with Tab and open it with Enter.
function EditableTd({ editable, onOpen, children }: { editable: boolean; onOpen(): void; children: ReactNode }) {
  if (!editable) return <td className="px-3 py-2">{children}</td>;
  const onKeyDown = (e: KeyboardEvent) => {
    if (e.target === e.currentTarget && e.key === "Enter") {
      e.preventDefault();
      onOpen();
    }
  };
  return (
    <td
      className="cursor-pointer px-3 py-2"
      tabIndex={0}
      onKeyDown={onKeyDown}
      onClick={(e) => {
        e.stopPropagation();
        onOpen();
      }}
    >
      {children}
    </td>
  );
}

function TitleCell({ data, actions, isEditing, startEdit, stopEdit }: CellProps) {
  const { t } = useTranslation("list");
  const { task, status } = data;
  const [draft, setDraft] = useState(task.title);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (isEditing) {
      setDraft(task.title);
      setTimeout(() => inputRef.current?.focus(), 0);
    }
  }, [isEditing, task.title]);

  const save = (title: string) => {
    stopEdit();
    void actions.quickEdit?.({ field: "title", value: title });
  };

  if (isEditing) {
    return (
      <td className="px-3 py-2" onClick={(e) => e.stopPropagation()}>
        <Input
          ref={inputRef}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          className="h-7 text-xs font-medium"
          aria-label={data.columns[0]?.label}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              if (draft.trim()) save(draft.trim());
              else stopEdit();
            }
            if (e.key === "Escape") {
              stopEdit();
            }
          }}
          onBlur={() => {
            if (draft.trim() && draft.trim() !== task.title) {
              save(draft.trim());
            } else {
              stopEdit();
            }
          }}
        />
      </td>
    );
  }

  const subtaskCount = task.counts.subtasks;
  const artifactCount = task.counts.artifacts;

  return (
    <td
      className="group cursor-pointer px-3 py-2"
      onDoubleClick={(e) => {
        e.stopPropagation();
        if (actions.quickEdit) startEdit();
      }}
      onClick={() => actions.openTask()}
    >
      <div className="flex items-center gap-1.5">
        {/* Subtask expand toggle */}
        {data.expandable ? (
          <button
            type="button"
            className="flex-shrink-0 p-0.5 rounded hover:bg-muted transition-colors"
            onClick={(e) => {
              e.stopPropagation();
              actions.toggleExpanded?.();
            }}
            aria-expanded={data.expanded}
            aria-label={data.expanded ? t("subtasks.collapse") : t("subtasks.expand")}
          >
            {data.expanding ? (
              <Loader2 className="h-3 w-3 animate-spin text-muted-foreground" />
            ) : data.expanded ? (
              <ChevronDown className="h-3 w-3 text-muted-foreground" />
            ) : (
              <ChevronRight className="h-3 w-3 text-muted-foreground" />
            )}
          </button>
        ) : (
          <span className="w-4 flex-shrink-0" />
        )}

        {/* Status color dot */}
        {status.color && (
          <span className="inline-block h-2 w-2 rounded-full flex-shrink-0" style={{ backgroundColor: status.color }} />
        )}

        {/* Title */}
        <TaskLink taskId={task.id} className="font-medium hover:text-primary truncate">
          {task.title}
        </TaskLink>

        {/* Visual indicators */}
        <span className="flex items-center gap-1 ml-1 flex-shrink-0">
          <data.components.Badges />
          {task.gate && (
            <span
              className="flex items-center text-tone-warning-600 dark:text-tone-warning-400"
              title={task.gate.class === "soft" ? t("row.humanGateSoft") : t("row.humanGate")}
            >
              <Lock className="h-3 w-3" aria-label={t("row.humanGate")} />
            </span>
          )}
          {task.hasDescription && <AlignLeft className="h-3 w-3 text-muted-foreground/60" aria-label={t("row.hasDescription")} />}
          {artifactCount > 0 && (
            <span className="flex items-center gap-0.5 text-muted-foreground/60">
              <Paperclip className="h-3 w-3" aria-hidden="true" />
              <span className="text-[10px]">{artifactCount}</span>
            </span>
          )}
          {subtaskCount > 0 && (
            <span className="flex items-center gap-0.5 text-muted-foreground/60">
              <GitBranch className="h-3 w-3" aria-hidden="true" />
              <span className="text-[10px]">{subtaskCount}</span>
            </span>
          )}
          {task.falseOpen?.allChildrenClosed && (
            <span
              className="flex items-center text-tone-positive-600 dark:text-tone-positive-400"
              title={t("board:falseOpen.allChildrenClosed", { days: task.falseOpen.staleDays })}
            >
              <CircleCheck className="h-3 w-3" aria-label={t("row.falseOpenAllClosed")} />
            </span>
          )}
          {task.falseOpen?.onlyParkedChildrenLeft && (
            <span
              className="flex items-center text-tone-sky-600 dark:text-tone-sky-400"
              title={t("board:falseOpen.onlyParkedChildrenLeft", { count: task.falseOpen.openChildrenCount, days: task.falseOpen.staleDays })}
            >
              <Snowflake className="h-3 w-3" aria-label={t("row.falseOpenParked")} />
            </span>
          )}
        </span>

        {/* Edit icon — visible on row hover */}
        <button
          type="button"
          className="ml-1 flex h-5 w-5 flex-shrink-0 items-center justify-center rounded text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100 hover:bg-muted hover:text-foreground focus-visible:opacity-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"
          onClick={(e) => {
            e.stopPropagation();
            actions.openTask();
          }}
          aria-label={t("row.editTask")}
          title={t("row.editTask")}
        >
          <Pencil className="h-3 w-3" />
        </button>
      </div>
    </td>
  );
}

function StatusCell({ data, actions, isEditing, startEdit, stopEdit }: CellProps) {
  const { task, status, statuses } = data;
  if (isEditing && actions.changeStatus) {
    const change = (id: string) => {
      stopEdit();
      void actions.changeStatus?.(id);
    };
    return (
      <td className="px-3 py-2" onClick={(e) => e.stopPropagation()}>
        <Select
          className="h-7 text-xs"
          value={task.statusId}
          aria-label={data.columns.find((c) => c.key === "status")?.label}
          onChange={(e) => {
            if (e.target.value) change(e.target.value);
            else stopEdit();
          }}
          autoFocus
          onBlur={stopEdit}
          onKeyDown={(e) => {
            if (e.key === "Escape") stopEdit();
            if (e.key === "Enter") change((e.target as HTMLSelectElement).value);
          }}
        >
          {statuses.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
            </option>
          ))}
        </Select>
      </td>
    );
  }

  return (
    <EditableTd editable={Boolean(actions.changeStatus)} onOpen={startEdit}>
      <div className="flex items-center gap-1.5">
        <span className="inline-block h-2 w-2 rounded-full" style={{ backgroundColor: status.color }} />
        <span className="text-xs">{status.name}</span>
      </div>
    </EditableTd>
  );
}

function PriorityCell({ data, actions, isEditing, startEdit, stopEdit }: CellProps) {
  const { t } = useTranslation("list");
  const { task } = data;
  if (isEditing && actions.quickEdit) {
    const save = (priority: string) => {
      stopEdit();
      void actions.quickEdit?.({ field: "priority", value: priority as Priority });
    };
    return (
      <td className="px-3 py-2" onClick={(e) => e.stopPropagation()}>
        <Select
          className="h-7 text-xs"
          value={task.priority}
          aria-label={data.columns.find((c) => c.key === "priority")?.label}
          onChange={(e) => {
            if (e.target.value) save(e.target.value);
            else stopEdit();
          }}
          autoFocus
          onBlur={stopEdit}
          onKeyDown={(e) => {
            if (e.key === "Escape") stopEdit();
            if (e.key === "Enter") save((e.target as HTMLSelectElement).value);
          }}
        >
          {PRIORITY_OPTIONS.map((p) => (
            <option key={p} value={p}>
              {t(`priority.${p}`)}
            </option>
          ))}
        </Select>
      </td>
    );
  }

  return (
    <EditableTd editable={Boolean(actions.quickEdit)} onOpen={startEdit}>
      <PriorityFlag priority={task.priority} size="sm" />
    </EditableTd>
  );
}

function DueDateCell({ data, actions, isEditing, startEdit, stopEdit }: CellProps) {
  const { task } = data;
  const inputRef = useRef<HTMLInputElement>(null);
  const dateValue = task.dueDate ? task.dueDate.slice(0, 10) : "";

  useEffect(() => {
    if (isEditing) {
      setTimeout(() => inputRef.current?.focus(), 0);
    }
  }, [isEditing]);

  const save = (value: string | null) => {
    stopEdit();
    void actions.quickEdit?.({ field: "due_date", value });
  };

  if (isEditing && actions.quickEdit) {
    return (
      <td className="px-3 py-2" onClick={(e) => e.stopPropagation()}>
        <Input
          ref={inputRef}
          type="date"
          className="h-7 text-xs"
          defaultValue={dateValue}
          aria-label={data.columns.find((c) => c.key === "due_date")?.label}
          onKeyDown={(e) => {
            if (e.key === "Escape") stopEdit();
            if (e.key === "Enter") {
              const v = (e.target as HTMLInputElement).value;
              save(v || null);
            }
          }}
          onBlur={(e) => {
            const v = e.target.value;
            save(v || null);
          }}
        />
      </td>
    );
  }

  return (
    <EditableTd editable={Boolean(actions.quickEdit)} onOpen={startEdit}>
      <span className={cn("text-xs", getDateClass(task.dueDate, task.statusCategory))}>
        {task.dueDate ? formatDate(task.dueDate) : "--"}
      </span>
    </EditableTd>
  );
}

function CustomFieldCell({ data, column, actions, isEditing, startEdit, stopEdit }: CellProps & { column: TaskRowColumn }) {
  const field = column.customField;
  const slug = column.key.slice(3);
  const currentValue = data.task.customFields[slug];
  const [draft, setDraft] = useState<unknown>(currentValue);

  useEffect(() => {
    if (isEditing) {
      setDraft(currentValue);
    }
  }, [isEditing, currentValue]);

  if (!field) return <td className="px-3 py-2" />;

  const save = (value: unknown) => {
    stopEdit();
    void actions.quickEdit?.({ field: `cf:${slug}`, value });
  };

  if (isEditing && actions.quickEdit) {
    return (
      <td
        className="px-3 py-2"
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => {
          if (e.key === "Escape") stopEdit();
          if (e.key === "Enter" && field.field_type !== "json") {
            e.preventDefault();
            save(draft);
          }
        }}
      >
        <CustomFieldRenderer
          field={field}
          value={draft}
          onChange={(v) => {
            setDraft(v);
            // For checkbox and select types, save immediately on change
            if (field.field_type === "checkbox" || field.field_type === "select") {
              save(v);
            }
          }}
          compact
        />
      </td>
    );
  }

  return (
    <EditableTd editable={Boolean(actions.quickEdit)} onOpen={startEdit}>
      <CustomFieldRenderer field={field} value={currentValue} onChange={() => {}} readOnly compact />
    </EditableTd>
  );
}

function SubtaskRow({ data, actions }: { data: TaskRowData; actions: TaskRowActions }) {
  const { t } = useTranslation("list");
  const { task, status } = data;
  // Same order as the parent row: `data.columns` decides it (`row_fields` can reorder).
  const cell = (column: TaskRowColumn) => {
    switch (column.key) {
      case "status":
        return (
          <td key={column.key} className="px-3 py-1.5">
            <div className="flex items-center gap-1.5">
              <span className="inline-block h-2 w-2 rounded-full" style={{ backgroundColor: status.color }} />
              <span className="text-xs text-muted-foreground">{status.name}</span>
            </div>
          </td>
        );
      case "priority":
        return (
          <td key={column.key} className="px-3 py-1.5">
            <PriorityFlag priority={task.priority} size="sm" />
          </td>
        );
      case "assignee":
        return (
          <td key={column.key} className="px-3 py-1.5">
            <AssigneeAvatar
              id={task.assignee?.id ?? null}
              name={task.assignee?.name || undefined}
              type={task.assignee?.type ?? "unassigned"}
              size="sm"
            />
          </td>
        );
      case "due_date":
        return (
          <td key={column.key} className="px-3 py-1.5">
            <span className={cn("text-xs", getDateClass(task.dueDate, task.statusCategory))}>
              {task.dueDate ? formatDate(task.dueDate) : "--"}
            </span>
          </td>
        );
      case "created_at":
        return (
          <td key={column.key} className="px-3 py-1.5">
            <span className="text-xs text-muted-foreground/50">{formatDate(data.createdAt)}</span>
          </td>
        );
      default:
        // labels, estimated_hours and custom fields have no subtask value.
        return (
          <td key={column.key} className="px-3 py-1.5">
            <span className="text-xs text-muted-foreground/50">--</span>
          </td>
        );
    }
  };
  return (
    <tr className="border-b border-border transition-colors hover:bg-muted/20 opacity-90" data-task-id={task.id}>
      {/* No checkbox for subtasks */}
      <td className="w-10 px-3 py-1.5" onClick={(e) => e.stopPropagation()} />

      <td className="cursor-pointer px-3 py-1.5 pl-10" onClick={() => actions.openTask()}>
        <div className="flex items-center gap-1.5">
          <span className="text-muted-foreground/50 text-xs mr-0.5">└</span>
          <span className="inline-block h-1.5 w-1.5 rounded-full flex-shrink-0" style={{ backgroundColor: status.color }} />
          <TaskLink taskId={task.id} className="text-xs text-muted-foreground hover:text-foreground transition-colors">
            {task.title}
          </TaskLink>
          {task.gate && (
            <span className="flex-shrink-0" title={t("row.humanGate")}>
              <Lock className="h-3 w-3 text-tone-warning-600 dark:text-tone-warning-400" aria-label={t("row.humanGate")} />
            </span>
          )}
        </div>
      </td>

      {data.columns.filter((c) => c.key !== "title").map(cell)}
    </tr>
  );
}

/** `task.row` of `mesh-default`: one table row, cells in the order of `data.columns`. */
export const TaskRow: TaskRowView = ({ data, actions }) => {
  const { t } = useTranslation("list");
  // Which cell is open for editing is the view's own state; what it saves goes through `actions`.
  const [editing, setEditing] = useState<string | null>(null);
  if (data.depth > 0) return <SubtaskRow data={data} actions={actions} />;

  const { task } = data;
  const cell = (column: TaskRowColumn): ReactNode => {
    const props: CellProps = {
      data,
      actions,
      isEditing: editing === column.key,
      startEdit: () => setEditing(column.key),
      stopEdit: () => setEditing(null),
    };
    switch (column.key) {
      case "title":
        return <TitleCell key={column.key} {...props} />;
      case "status":
        return <StatusCell key={column.key} {...props} />;
      case "priority":
        return <PriorityCell key={column.key} {...props} />;
      case "assignee":
        return (
          <td key={column.key} className="px-3 py-2">
            <AssigneeAvatar
              id={task.assignee?.id ?? null}
              name={task.assignee?.name || undefined}
              type={task.assignee?.type ?? "unassigned"}
              size="sm"
            />
          </td>
        );
      case "due_date":
        return <DueDateCell key={column.key} {...props} />;
      case "labels":
        return (
          <td key={column.key} className="px-3 py-2">
            <div className="flex flex-wrap gap-1">
              {task.labels.slice(0, 3).map((label) => (
                <span key={label} className="rounded-full bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
                  {label}
                </span>
              ))}
              {task.labels.length > 3 && <span className="text-[10px] text-muted-foreground">+{task.labels.length - 3}</span>}
            </div>
          </td>
        );
      case "estimated_hours":
        return (
          <td key={column.key} className="px-3 py-2 text-xs text-muted-foreground">
            {task.estimatedHours != null ? t("table.estimateHours", { hours: task.estimatedHours }) : "--"}
          </td>
        );
      case "created_at":
        return (
          <td key={column.key} className="px-3 py-2 text-xs text-muted-foreground">
            {formatDate(data.createdAt)}
          </td>
        );
      default:
        return column.key.startsWith("cf:") ? <CustomFieldCell key={column.key} column={column} {...props} /> : null;
    }
  };

  return (
    <tr
      data-task-id={task.id}
      className={cn("border-b border-border transition-colors last:border-b-0", data.selected ? "bg-primary/5" : "hover:bg-muted/30")}
    >
      <td className="w-10 px-3 py-2" onClick={(e) => e.stopPropagation()}>
        <input
          type="checkbox"
          checked={data.selected}
          aria-checked={data.selected}
          onChange={() => actions.toggleSelected()}
          className="h-4 w-4 rounded border-input cursor-pointer"
          aria-label={t("table.selectTask", { title: task.title })}
        />
      </td>
      {data.columns.map(cell)}
    </tr>
  );
};
