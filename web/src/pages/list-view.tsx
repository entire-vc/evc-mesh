import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router";
import {
  ArrowDown,
  ArrowUp,
  ArrowUpDown,
  Bot,
  ChevronLeft,
  ChevronRight,
  Columns3,
  GitBranch,
  List,
  User,
} from "lucide-react";
import { useProjectStore } from "@/stores/project";
import { useTaskStore } from "@/stores/task";
import { useCustomFieldStore } from "@/stores/custom-field";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { CustomFieldRenderer } from "@/components/custom-field-renderer";
import { cn } from "@/lib/cn";
import { formatDate, priorityConfig } from "@/lib/utils";
import type { Task, Priority } from "@/types";

// ---------------------------------------------------------------------------
// Sort configuration
// ---------------------------------------------------------------------------

type SortField = "title" | "status" | "priority" | "assignee" | "due_date" | string;
type SortDir = "asc" | "desc";

const PRIORITY_ORDER: Record<Priority, number> = {
  urgent: 0,
  high: 1,
  medium: 2,
  low: 3,
  none: 4,
};

const DEFAULT_PER_PAGE = 50;

// ---------------------------------------------------------------------------
// List View page
// ---------------------------------------------------------------------------

export function ListViewPage() {
  const { wsSlug, projectSlug } = useParams();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const { currentProject, statuses, fetchStatuses } = useProjectStore();
  const { tasks, isLoading, total, hasMore, fetchTasks } = useTaskStore();
  const { fields: customFieldDefs, fetchFields: fetchCustomFields } =
    useCustomFieldStore();

  const [sortField, setSortField] = useState<SortField>("title");
  const [sortDir, setSortDir] = useState<SortDir>("asc");

  // Derive pagination from URL search params
  const page = Math.max(1, Number(searchParams.get("page")) || 1);
  const perPage = Math.max(1, Number(searchParams.get("per_page")) || DEFAULT_PER_PAGE);

  useEffect(() => {
    if (currentProject) {
      fetchStatuses(currentProject.id);
      fetchTasks(currentProject.id, { page, page_size: perPage });
      fetchCustomFields(currentProject.id).catch(() => {
        // Custom fields API may not be available yet
      });
    }
  }, [currentProject, fetchStatuses, fetchTasks, fetchCustomFields, page, perPage]);

  // Status lookup
  const statusMap = useMemo(() => {
    const map = new Map<string, { name: string; color: string; position: number }>();
    for (const s of statuses) {
      map.set(s.id, { name: s.name, color: s.color, position: s.position });
    }
    return map;
  }, [statuses]);

  // Sorted custom field defs
  const sortedFieldDefs = useMemo(
    () => [...customFieldDefs].sort((a, b) => a.position - b.position),
    [customFieldDefs],
  );

  // Toggle sort
  const handleSort = useCallback(
    (field: SortField) => {
      if (sortField === field) {
        setSortDir((d) => (d === "asc" ? "desc" : "asc"));
      } else {
        setSortField(field);
        setSortDir("asc");
      }
    },
    [sortField],
  );

  // Sorted tasks
  const sortedTasks = useMemo(() => {
    const arr = [...tasks];
    arr.sort((a, b) => {
      let cmp = 0;

      switch (sortField) {
        case "title":
          cmp = a.title.localeCompare(b.title);
          break;
        case "status": {
          const sa = statusMap.get(a.status_id)?.position ?? 0;
          const sb = statusMap.get(b.status_id)?.position ?? 0;
          cmp = sa - sb;
          break;
        }
        case "priority":
          cmp = PRIORITY_ORDER[a.priority] - PRIORITY_ORDER[b.priority];
          break;
        case "assignee":
          cmp = (a.assignee_type ?? "").localeCompare(b.assignee_type ?? "");
          break;
        case "due_date": {
          const da = a.due_date ? new Date(a.due_date).getTime() : Infinity;
          const db = b.due_date ? new Date(b.due_date).getTime() : Infinity;
          cmp = da - db;
          break;
        }
        default: {
          // Custom field sort -- type-aware
          const fieldDef = sortedFieldDefs.find((f) => f.slug === sortField);
          const va = a.custom_fields?.[sortField];
          const vb = b.custom_fields?.[sortField];

          if (fieldDef?.field_type === "number") {
            const na = va != null ? Number(va) : -Infinity;
            const nb = vb != null ? Number(vb) : -Infinity;
            cmp = na - nb;
          } else if (
            fieldDef?.field_type === "date" ||
            fieldDef?.field_type === "datetime"
          ) {
            const da = va ? new Date(String(va)).getTime() : 0;
            const db = vb ? new Date(String(vb)).getTime() : 0;
            cmp = da - db;
          } else if (fieldDef?.field_type === "checkbox") {
            cmp = (va ? 1 : 0) - (vb ? 1 : 0);
          } else {
            const strA = va != null ? String(va) : "";
            const strB = vb != null ? String(vb) : "";
            cmp = strA.localeCompare(strB);
          }
          break;
        }
      }

      return sortDir === "asc" ? cmp : -cmp;
    });
    return arr;
  }, [tasks, sortField, sortDir, statusMap, sortedFieldDefs]);

  const handleTaskClick = useCallback(
    (task: Task) => {
      navigate(`/w/${wsSlug}/p/${projectSlug}/t/${task.id}`);
    },
    [navigate, wsSlug, projectSlug],
  );

  // Pagination helpers
  const totalPages = Math.max(1, Math.ceil(total / perPage));
  const rangeStart = total > 0 ? (page - 1) * perPage + 1 : 0;
  const rangeEnd = Math.min(page * perPage, total);

  const goToPage = useCallback(
    (newPage: number) => {
      const params = new URLSearchParams(searchParams);
      params.set("page", String(newPage));
      if (perPage !== DEFAULT_PER_PAGE) {
        params.set("per_page", String(perPage));
      }
      setSearchParams(params);
    },
    [searchParams, setSearchParams, perPage],
  );

  if (!currentProject) {
    return (
      <div className="flex items-center justify-center py-12">
        <p className="text-muted-foreground">
          Project &quot;{projectSlug}&quot; not found
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <List className="h-5 w-5 text-muted-foreground" />
          <h1 className="text-2xl font-bold tracking-tight">
            {currentProject.name}
          </h1>
        </div>

        {/* View toggle */}
        <div className="flex items-center gap-1 rounded-lg border border-border bg-muted/50 p-1">
          <Button
            variant="ghost"
            size="sm"
            className="h-7 gap-1.5 px-3 text-xs"
            onClick={() => navigate(`/w/${wsSlug}/p/${projectSlug}`)}
          >
            <Columns3 className="h-3.5 w-3.5" />
            Board
          </Button>
          <Button
            variant="secondary"
            size="sm"
            className="h-7 gap-1.5 px-3 text-xs"
          >
            <List className="h-3.5 w-3.5" />
            List
          </Button>
          <Button
            variant="ghost"
            size="sm"
            className="h-7 gap-1.5 px-3 text-xs"
            onClick={() =>
              navigate(`/w/${wsSlug}/p/${projectSlug}/timeline`)
            }
          >
            <GitBranch className="h-3.5 w-3.5" />
            Timeline
          </Button>
        </div>
      </div>

      {/* Table */}
      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 8 }).map((_, i) => (
            <Skeleton key={i} className="h-10 w-full rounded-lg" />
          ))}
        </div>
      ) : tasks.length === 0 && page === 1 ? (
        <div className="flex flex-col items-center justify-center py-20">
          <List className="mb-4 h-12 w-12 text-muted-foreground" />
          <h3 className="mb-2 text-lg font-semibold">No tasks yet</h3>
          <p className="text-sm text-muted-foreground">
            Create tasks to see them in list view.
          </p>
        </div>
      ) : (
        <>
          <div className="overflow-x-auto rounded-lg border border-border">
            <table className="w-full text-left text-sm">
              <thead>
                <tr className="border-b border-border bg-muted/50">
                  <SortableHeader
                    label="Title"
                    field="title"
                    currentField={sortField}
                    dir={sortDir}
                    onSort={handleSort}
                    className="min-w-[240px]"
                  />
                  <SortableHeader
                    label="Status"
                    field="status"
                    currentField={sortField}
                    dir={sortDir}
                    onSort={handleSort}
                    className="min-w-[120px]"
                  />
                  <SortableHeader
                    label="Priority"
                    field="priority"
                    currentField={sortField}
                    dir={sortDir}
                    onSort={handleSort}
                    className="min-w-[100px]"
                  />
                  <SortableHeader
                    label="Assignee"
                    field="assignee"
                    currentField={sortField}
                    dir={sortDir}
                    onSort={handleSort}
                    className="min-w-[100px]"
                  />
                  <SortableHeader
                    label="Due Date"
                    field="due_date"
                    currentField={sortField}
                    dir={sortDir}
                    onSort={handleSort}
                    className="min-w-[110px]"
                  />
                  {/* Dynamic custom field columns */}
                  {sortedFieldDefs.map((field) => (
                    <SortableHeader
                      key={field.id}
                      label={field.name}
                      field={field.slug}
                      currentField={sortField}
                      dir={sortDir}
                      onSort={handleSort}
                      className="min-w-[100px]"
                    />
                  ))}
                </tr>
              </thead>
              <tbody>
                {sortedTasks.map((task) => {
                  const status = statusMap.get(task.status_id);
                  const pConfig = priorityConfig[task.priority];
                  return (
                    <tr
                      key={task.id}
                      className="cursor-pointer border-b border-border transition-colors hover:bg-muted/30 last:border-b-0"
                      onClick={() => handleTaskClick(task)}
                    >
                      {/* Title */}
                      <td className="px-3 py-2">
                        <span className="font-medium">{task.title}</span>
                      </td>

                      {/* Status */}
                      <td className="px-3 py-2">
                        {status && (
                          <div className="flex items-center gap-1.5">
                            <span
                              className="inline-block h-2 w-2 rounded-full"
                              style={{ backgroundColor: status.color }}
                            />
                            <span className="text-xs">{status.name}</span>
                          </div>
                        )}
                      </td>

                      {/* Priority */}
                      <td className="px-3 py-2">
                        {task.priority !== "none" && (
                          <Badge
                            variant="secondary"
                            className={cn("text-[10px]", pConfig.color)}
                          >
                            {pConfig.label}
                          </Badge>
                        )}
                      </td>

                      {/* Assignee */}
                      <td className="px-3 py-2">
                        <div className="flex items-center gap-1">
                          {task.assignee_type === "agent" ? (
                            <Bot className="h-3.5 w-3.5 text-violet-500" />
                          ) : task.assignee_type === "user" ? (
                            <User className="h-3.5 w-3.5 text-sky-500" />
                          ) : (
                            <span className="text-xs text-muted-foreground">
                              --
                            </span>
                          )}
                          {task.assignee_id && (
                            <span className="text-xs text-muted-foreground">
                              {task.assignee_id.slice(0, 8)}
                            </span>
                          )}
                        </div>
                      </td>

                      {/* Due Date */}
                      <td className="px-3 py-2">
                        <span className="text-xs">
                          {task.due_date
                            ? formatDate(task.due_date)
                            : "--"}
                        </span>
                      </td>

                      {/* Custom field columns */}
                      {sortedFieldDefs.map((field) => (
                        <td key={field.id} className="px-3 py-2">
                          <CustomFieldRenderer
                            field={field}
                            value={task.custom_fields?.[field.slug]}
                            onChange={() => {}}
                            readOnly
                            compact
                          />
                        </td>
                      ))}
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>

          {/* Pagination footer */}
          {total > 0 && (
            <div className="flex items-center justify-between text-xs text-muted-foreground">
              <span>
                Showing {rangeStart}&#8211;{rangeEnd} of {total} task{total !== 1 ? "s" : ""}
                {sortedFieldDefs.length > 0 &&
                  ` | ${sortedFieldDefs.length} custom field${sortedFieldDefs.length !== 1 ? "s" : ""}`}
              </span>
              <div className="flex items-center gap-2">
                <Button
                  variant="outline"
                  size="sm"
                  className="h-7 px-2 text-xs"
                  disabled={page <= 1}
                  onClick={() => goToPage(page - 1)}
                >
                  <ChevronLeft className="mr-0.5 h-3.5 w-3.5" />
                  Previous
                </Button>
                <span className="tabular-nums">
                  Page {page} of {totalPages}
                </span>
                <Button
                  variant="outline"
                  size="sm"
                  className="h-7 px-2 text-xs"
                  disabled={!hasMore && page >= totalPages}
                  onClick={() => goToPage(page + 1)}
                >
                  Next
                  <ChevronRight className="ml-0.5 h-3.5 w-3.5" />
                </Button>
              </div>
            </div>
          )}
        </>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Sortable table header
// ---------------------------------------------------------------------------

function SortableHeader({
  label,
  field,
  currentField,
  dir,
  onSort,
  className,
}: {
  label: string;
  field: string;
  currentField: string;
  dir: SortDir;
  onSort: (field: string) => void;
  className?: string;
}) {
  const isActive = currentField === field;

  return (
    <th
      className={cn(
        "cursor-pointer select-none px-3 py-2 text-xs font-semibold text-muted-foreground transition-colors hover:text-foreground",
        className,
      )}
      onClick={() => onSort(field)}
    >
      <div className="flex items-center gap-1">
        {label}
        {isActive ? (
          dir === "asc" ? (
            <ArrowUp className="h-3 w-3" />
          ) : (
            <ArrowDown className="h-3 w-3" />
          )
        ) : (
          <ArrowUpDown className="h-3 w-3 opacity-30" />
        )}
      </div>
    </th>
  );
}
