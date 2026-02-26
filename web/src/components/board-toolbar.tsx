/**
 * BoardToolbar — ClickUp-style controls for the Board page.
 *
 * Layout:
 *   [Group: Status ▾]  [Subtasks]  [Sort ▾]  [Filter ▾]  [Closed]  [Assignee ▾]  [Search…]    [+ New Task]
 *
 * The toolbar only owns its own UI — filtering/grouping state lives in the
 * parent (BoardPage) so the board columns can react to it.
 */

import { Search, ChevronDown } from "lucide-react";
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
import {
  TagFilterDropdown,
  CustomFieldFilterDialog,
  type CFFilters,
} from "@/components/view-filters";
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

  // Tag filters
  allTags: string[];
  selectedTags: string[];
  onTagsChange: (tags: string[]) => void;

  // Custom field filters (new CFFilters shape)
  cfFilters: CFFilters;
  onCFFiltersChange: (v: CFFilters) => void;
  filterableFields: CustomFieldDefinition[];

  // Legacy custom field filters (kept for SavedViews compatibility)
  customFieldFilters: Record<string, unknown>;
  onCustomFieldFiltersChange: (v: Record<string, unknown>) => void;

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
  allTags,
  selectedTags,
  onTagsChange,
  cfFilters,
  onCFFiltersChange,
  filterableFields,
  customFieldFilters: _customFieldFilters,
  onCustomFieldFiltersChange: _onCustomFieldFiltersChange,
  onNewTask,
}: BoardToolbarProps) {

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

      {/* Tag filter */}
      <TagFilterDropdown
        allTags={allTags}
        selectedTags={selectedTags}
        onChange={onTagsChange}
      />

      {/* Custom field filters (full modal dialog, all field types) */}
      <CustomFieldFilterDialog
        fields={filterableFields}
        filters={cfFilters}
        onChange={onCFFiltersChange}
      />

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
