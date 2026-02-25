/**
 * BoardToolbar — ClickUp-style controls for the Board page.
 *
 * Layout:
 *   [Group: Status ▾]  [Subtasks]  [Sort ▾]  [Filter ▾]  [Closed]  [Assignee ▾]  [Search…]    [+ New Task]
 *
 * The toolbar only owns its own UI — filtering/grouping state lives in the
 * parent (BoardPage) so the board columns can react to it.
 */

import { Search, SlidersHorizontal, ChevronDown } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/cn";
import type { CustomFieldDefinition } from "@/types";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export type GroupBy = "status" | "priority" | "assignee";
export type SortBy = "manual" | "priority" | "due_date" | "created" | "title";

export interface BoardToolbarProps {
  // GroupBy
  groupBy: GroupBy;
  onGroupByChange: (v: GroupBy) => void;

  // Sort
  sortBy: SortBy;
  onSortByChange: (v: SortBy) => void;

  // Closed toggle
  showClosed: boolean;
  onShowClosedChange: (v: boolean) => void;

  // Subtasks toggle
  showSubtasks: boolean;
  onShowSubtasksChange: (v: boolean) => void;

  // Search
  searchQuery: string;
  onSearchQueryChange: (v: string) => void;

  // Priority filter
  priorityFilter: string;
  onPriorityFilterChange: (v: string) => void;

  // Assignee filter
  assigneeFilter: string;
  onAssigneeFilterChange: (v: string) => void;

  // Custom field filters
  customFieldFilters: Record<string, unknown>;
  onCustomFieldFiltersChange: (v: Record<string, unknown>) => void;
  filterableFields: CustomFieldDefinition[];

  // New task action
  onNewTask: () => void;
}

const GROUP_BY_LABELS: Record<GroupBy, string> = {
  status: "Status",
  priority: "Priority",
  assignee: "Assignee",
};

const SORT_BY_LABELS: Record<SortBy, string> = {
  manual: "Manual",
  priority: "Priority",
  due_date: "Due Date",
  created: "Created",
  title: "Title",
};

// ---------------------------------------------------------------------------
// Small toggle button used for Closed / Subtasks
// ---------------------------------------------------------------------------

function ToggleButton({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "flex h-8 items-center gap-1.5 rounded-md border px-2.5 text-xs font-medium transition-colors",
        active
          ? "border-primary/50 bg-primary/10 text-primary"
          : "border-border bg-transparent text-muted-foreground hover:bg-muted/60 hover:text-foreground",
      )}
    >
      {children}
    </button>
  );
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function BoardToolbar({
  groupBy,
  onGroupByChange,
  sortBy,
  onSortByChange,
  showClosed,
  onShowClosedChange,
  showSubtasks,
  onShowSubtasksChange,
  searchQuery,
  onSearchQueryChange,
  priorityFilter,
  onPriorityFilterChange,
  assigneeFilter,
  onAssigneeFilterChange,
  customFieldFilters,
  onCustomFieldFiltersChange,
  filterableFields,
  onNewTask,
}: BoardToolbarProps) {
  const activeCustomFiltersCount = Object.values(customFieldFilters).filter(
    (v) => v != null && v !== "" && v !== "all",
  ).length;

  return (
    <div className="flex flex-wrap items-center gap-2">
      {/* ---- Left: GroupBy + Subtasks ---- */}

      {/* Group By dropdown */}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            variant="outline"
            size="sm"
            className="h-8 gap-1.5 px-2.5 text-xs"
          >
            <span className="text-muted-foreground">Group:</span>
            {GROUP_BY_LABELS[groupBy]}
            <ChevronDown className="h-3 w-3 opacity-60" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="w-40">
          <DropdownMenuLabel className="text-xs">Group by</DropdownMenuLabel>
          <DropdownMenuSeparator />
          {(["status", "priority", "assignee"] as GroupBy[]).map((g) => (
            <DropdownMenuItem
              key={g}
              onClick={() => onGroupByChange(g)}
              className={cn(
                "text-sm",
                groupBy === g && "font-medium text-primary",
              )}
            >
              {GROUP_BY_LABELS[g]}
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>

      {/* Subtasks toggle */}
      <ToggleButton
        active={showSubtasks}
        onClick={() => onShowSubtasksChange(!showSubtasks)}
      >
        Subtasks
      </ToggleButton>

      {/* ---- Center: Sort + Filters ---- */}

      {/* Sort dropdown */}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            variant="outline"
            size="sm"
            className="h-8 gap-1.5 px-2.5 text-xs"
          >
            <span className="text-muted-foreground">Sort:</span>
            {SORT_BY_LABELS[sortBy]}
            <ChevronDown className="h-3 w-3 opacity-60" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="w-44">
          <DropdownMenuLabel className="text-xs">Sort within column</DropdownMenuLabel>
          <DropdownMenuSeparator />
          {(["manual", "priority", "due_date", "created", "title"] as SortBy[]).map(
            (s) => (
              <DropdownMenuItem
                key={s}
                onClick={() => onSortByChange(s)}
                className={cn(
                  "text-sm",
                  sortBy === s && "font-medium text-primary",
                )}
              >
                {SORT_BY_LABELS[s]}
              </DropdownMenuItem>
            ),
          )}
        </DropdownMenuContent>
      </DropdownMenu>

      {/* Priority filter (existing, kept as a compact Select) */}
      <Select
        value={priorityFilter}
        onChange={(e) => onPriorityFilterChange(e.target.value)}
        className="h-8 w-36 text-xs"
      >
        <option value="all">All Priorities</option>
        <option value="urgent">Urgent</option>
        <option value="high">High</option>
        <option value="medium">Medium</option>
        <option value="low">Low</option>
        <option value="none">None</option>
      </Select>

      {/* Custom field filters */}
      {filterableFields.length > 0 && (
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              variant="outline"
              size="sm"
              className="h-8 gap-1.5 px-2.5 text-xs"
            >
              <SlidersHorizontal className="h-3.5 w-3.5" />
              Filter
              {activeCustomFiltersCount > 0 && (
                <Badge
                  variant="secondary"
                  className="ml-0.5 h-4 px-1 text-[10px]"
                >
                  {activeCustomFiltersCount}
                </Badge>
              )}
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent className="w-56 p-2" align="start">
            <DropdownMenuLabel>Filter by Custom Fields</DropdownMenuLabel>
            <DropdownMenuSeparator />
            <div
              className="space-y-3 p-1"
              onClick={(e) => e.stopPropagation()}
            >
              {filterableFields.map((field) => (
                <div key={field.id} className="space-y-1">
                  <label className="text-xs font-medium text-muted-foreground">
                    {field.name}
                  </label>
                  {field.field_type === "select" && (
                    <Select
                      value={
                        (customFieldFilters[field.slug] as string) ?? "all"
                      }
                      onChange={(e) => {
                        onCustomFieldFiltersChange({
                          ...customFieldFilters,
                          [field.slug]: e.target.value,
                        });
                      }}
                      className="h-7 text-xs"
                    >
                      <option value="all">All</option>
                      {(
                        (field.options?.choices ?? []) as {
                          label: string;
                          value: string;
                        }[]
                      ).map((c) => (
                        <option key={c.value} value={c.value}>
                          {c.label}
                        </option>
                      ))}
                    </Select>
                  )}
                  {field.field_type === "checkbox" && (
                    <Select
                      value={
                        (customFieldFilters[field.slug] as string) ?? "all"
                      }
                      onChange={(e) => {
                        onCustomFieldFiltersChange({
                          ...customFieldFilters,
                          [field.slug]: e.target.value,
                        });
                      }}
                      className="h-7 text-xs"
                    >
                      <option value="all">All</option>
                      <option value="checked">Checked</option>
                      <option value="unchecked">Unchecked</option>
                    </Select>
                  )}
                  {field.field_type === "number" && (
                    <div className="flex items-center gap-1">
                      <Input
                        type="number"
                        placeholder="Min"
                        className="h-7 w-20 text-xs"
                        value={
                          (
                            customFieldFilters[field.slug] as {
                              min?: number;
                              max?: number;
                            }
                          )?.min ?? ""
                        }
                        onChange={(e) => {
                          const prev = (customFieldFilters[field.slug] as {
                            min?: number;
                            max?: number;
                          }) ?? {};
                          onCustomFieldFiltersChange({
                            ...customFieldFilters,
                            [field.slug]: {
                              ...prev,
                              min: e.target.value
                                ? Number(e.target.value)
                                : undefined,
                            },
                          });
                        }}
                      />
                      <span className="text-xs text-muted-foreground">-</span>
                      <Input
                        type="number"
                        placeholder="Max"
                        className="h-7 w-20 text-xs"
                        value={
                          (
                            customFieldFilters[field.slug] as {
                              min?: number;
                              max?: number;
                            }
                          )?.max ?? ""
                        }
                        onChange={(e) => {
                          const prev = (customFieldFilters[field.slug] as {
                            min?: number;
                            max?: number;
                          }) ?? {};
                          onCustomFieldFiltersChange({
                            ...customFieldFilters,
                            [field.slug]: {
                              ...prev,
                              max: e.target.value
                                ? Number(e.target.value)
                                : undefined,
                            },
                          });
                        }}
                      />
                    </div>
                  )}
                </div>
              ))}
              {activeCustomFiltersCount > 0 && (
                <>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem
                    onClick={() => onCustomFieldFiltersChange({})}
                    className="justify-center text-xs text-destructive"
                  >
                    Clear custom filters
                  </DropdownMenuItem>
                </>
              )}
            </div>
          </DropdownMenuContent>
        </DropdownMenu>
      )}

      {/* Show Closed toggle */}
      <ToggleButton
        active={showClosed}
        onClick={() => onShowClosedChange(!showClosed)}
      >
        Closed
      </ToggleButton>

      {/* Assignee filter */}
      <Select
        value={assigneeFilter}
        onChange={(e) => onAssigneeFilterChange(e.target.value)}
        className="h-8 w-36 text-xs"
      >
        <option value="all">All Assignees</option>
        <option value="user">User</option>
        <option value="agent">Agent</option>
        <option value="unassigned">Unassigned</option>
      </Select>

      {/* Search */}
      <div className="relative min-w-[180px] flex-1 max-w-xs">
        <Search className="absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input
          placeholder="Search tasks..."
          value={searchQuery}
          onChange={(e) => onSearchQueryChange(e.target.value)}
          className="h-8 pl-8 text-xs"
        />
      </div>

      {/* Spacer */}
      <div className="flex-1" />

      {/* New Task */}
      <Button size="sm" className="h-8 gap-1.5" onClick={onNewTask}>
        New Task
      </Button>
    </div>
  );
}
