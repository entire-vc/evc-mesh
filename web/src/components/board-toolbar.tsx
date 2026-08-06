/**
 * BoardToolbar — ClickUp-style controls for the Board page.
 *
 * Layout:
 *   [Group: Status ▾]  [Subtasks]  [Sort ▾]  [Filter ▾]  [Closed]  [Assignee ▾]  [Search…]    [+ New Task]
 *
 * The toolbar only owns its own UI — filtering/grouping state lives in the
 * parent (BoardPage) so the board columns can react to it.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { Search, ChevronDown, ChevronUp, ArrowUpDown, Plus, RefreshCw, Filter, Users, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
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
import { AssigneeAvatar } from "@/components/assignee-avatar";
import type { CustomFieldDefinition } from "@/types";

// ---------------------------------------------------------------------------
// Assignee candidate — a project member (or a task's legacy assignee) that
// can be picked in the specific-assignee filter.
// ---------------------------------------------------------------------------

export interface AssigneeCandidate {
  id: string;
  name: string;
  type: "user" | "agent";
}

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export type GroupBy = "status" | "priority" | "assignee";
export type SortBy = "manual" | "updated" | "priority" | "due_date" | "created" | "title";

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

  // Specific-assignee filter (one or several people/agents)
  assigneeCandidates: AssigneeCandidate[];
  assigneeIdsFilter: string[];
  onAssigneeIdsFilterChange: (ids: string[]) => void;

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
  // Recurring task action (optional)
  onNewRecurring?: () => void;
}

const GROUP_BY_LABELS: Record<GroupBy, string> = {
  status: "Status",
  priority: "Priority",
  assignee: "Assignee",
};

const SORT_BY_LABELS: Record<SortBy, string> = {
  manual: "Manual",
  updated: "Last Updated",
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
// Specific-assignee filter dropdown — checkbox multiselect + "Unassigned",
// same interaction shape as TagFilterDropdown in view-filters.tsx.
// ---------------------------------------------------------------------------

const UNASSIGNED_ID = "unassigned";

interface AssigneeFilterDropdownProps {
  candidates: AssigneeCandidate[];
  selectedIds: string[];
  onChange: (ids: string[]) => void;
  className?: string;
}

function AssigneeFilterDropdown({
  candidates,
  selectedIds,
  onChange,
  className,
}: AssigneeFilterDropdownProps) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    function handleClickOutside(e: MouseEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) {
        setOpen(false);
      }
    }
    if (open) {
      document.addEventListener("mousedown", handleClickOutside);
    }
    return () => document.removeEventListener("mousedown", handleClickOutside);
  }, [open]);

  const toggleId = useCallback(
    (id: string) => {
      if (selectedIds.includes(id)) {
        onChange(selectedIds.filter((v) => v !== id));
      } else {
        onChange([...selectedIds, id]);
      }
    },
    [selectedIds, onChange],
  );

  const clearAll = useCallback(() => onChange([]), [onChange]);

  const activeCount = selectedIds.length;

  return (
    <div ref={ref} className={cn("relative inline-block", className)}>
      <Button
        variant="outline"
        size="sm"
        className={cn(
          "h-8 gap-1.5 px-2.5 text-xs",
          activeCount > 0 && "border-primary/50 bg-primary/10 text-primary",
        )}
        onClick={() => setOpen((v) => !v)}
      >
        <Users className="h-3.5 w-3.5" />
        Assignees
        {activeCount > 0 && (
          <Badge variant="secondary" className="ml-0.5 h-4 px-1 text-[10px]">
            {activeCount}
          </Badge>
        )}
        <ChevronDown className="h-3 w-3 opacity-60" />
      </Button>

      {open && (
        <div className="absolute left-0 z-50 mt-2 w-60 rounded-lg border border-border bg-popover p-2 shadow-lg">
          <div className="mb-1.5 flex items-center justify-between px-1">
            <span className="text-xs font-semibold text-foreground">Filter by Assignee</span>
            {activeCount > 0 && (
              <button
                type="button"
                onClick={clearAll}
                className="flex items-center gap-0.5 text-[10px] text-muted-foreground hover:text-destructive"
              >
                <X className="h-3 w-3" />
                Clear
              </button>
            )}
          </div>
          <div className="-mx-1 my-1 h-px bg-border" />
          <div className="max-h-64 overflow-y-auto">
            <label className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-sm hover:bg-accent">
              <input
                type="checkbox"
                checked={selectedIds.includes(UNASSIGNED_ID)}
                onChange={() => toggleId(UNASSIGNED_ID)}
                className="h-3.5 w-3.5 rounded border-input"
              />
              <AssigneeAvatar type="unassigned" size="sm" />
              <span className="truncate">Unassigned</span>
            </label>
            {candidates.map((c) => (
              <label
                key={c.id}
                className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-sm hover:bg-accent"
              >
                <input
                  type="checkbox"
                  checked={selectedIds.includes(c.id)}
                  onChange={() => toggleId(c.id)}
                  className="h-3.5 w-3.5 rounded border-input"
                />
                <AssigneeAvatar name={c.name} type={c.type} size="sm" />
                <span className="truncate">{c.name}</span>
              </label>
            ))}
          </div>
        </div>
      )}
    </div>
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
  assigneeCandidates,
  assigneeIdsFilter,
  onAssigneeIdsFilterChange,
  allTags,
  selectedTags,
  onTagsChange,
  cfFilters,
  onCFFiltersChange,
  filterableFields,
  customFieldFilters: _customFieldFilters,
  onCustomFieldFiltersChange: _onCustomFieldFiltersChange,
  onNewTask,
  onNewRecurring,
}: BoardToolbarProps) {
  const [filtersOpen, setFiltersOpen] = useState(false);

  return (
    <div className="space-y-2">
      {/* Primary row — always visible */}
      <div className="flex flex-wrap items-center gap-2">
        {/* Group By dropdown */}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="outline" size="sm" className="h-8 gap-1.5 px-2.5 text-xs">
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
                className={cn("text-sm", groupBy === g && "font-medium text-primary")}
              >
                {GROUP_BY_LABELS[g]}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>

        {/* Subtasks toggle */}
        <ToggleButton active={showSubtasks} onClick={() => onShowSubtasksChange(!showSubtasks)}>
          Subtasks
        </ToggleButton>

        {/* Sort + secondary filters — hidden on mobile, shown inline on desktop */}
        <div className="hidden md:contents">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="outline" size="sm" className="h-8 gap-1.5 px-2.5 text-xs">
                <ArrowUpDown className="h-3.5 w-3.5 text-muted-foreground" />
                {SORT_BY_LABELS[sortBy]}
                <ChevronDown className="h-3 w-3 opacity-60" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start" className="w-44">
              <DropdownMenuLabel className="text-xs">Sort within column</DropdownMenuLabel>
              <DropdownMenuSeparator />
              {(["manual", "updated", "priority", "due_date", "created", "title"] as SortBy[]).map((s) => (
                <DropdownMenuItem
                  key={s}
                  onClick={() => onSortByChange(s)}
                  className={cn("text-sm", sortBy === s && "font-medium text-primary")}
                >
                  {SORT_BY_LABELS[s]}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>

          <Select value={priorityFilter} onChange={(e) => onPriorityFilterChange(e.target.value)} className="h-8 w-36 text-xs">
            <option value="all">All Priorities</option>
            <option value="urgent">Urgent</option>
            <option value="high">High</option>
            <option value="medium">Medium</option>
            <option value="low">Low</option>
            <option value="none">None</option>
          </Select>

          <TagFilterDropdown allTags={allTags} selectedTags={selectedTags} onChange={onTagsChange} />
          <CustomFieldFilterDialog fields={filterableFields} filters={cfFilters} onChange={onCFFiltersChange} />

          <ToggleButton active={showClosed} onClick={() => onShowClosedChange(!showClosed)}>
            Closed
          </ToggleButton>

          <Select value={assigneeFilter} onChange={(e) => onAssigneeFilterChange(e.target.value)} className="h-8 w-36 text-xs">
            <option value="all">All Assignees</option>
            <option value="user">User</option>
            <option value="agent">Agent</option>
            <option value="unassigned">Unassigned</option>
          </Select>

          <AssigneeFilterDropdown
            candidates={assigneeCandidates}
            selectedIds={assigneeIdsFilter}
            onChange={onAssigneeIdsFilterChange}
          />
        </div>

        {/* Mobile filter toggle */}
        <Button
          variant="outline"
          size="sm"
          className="h-8 gap-1.5 px-2.5 text-xs md:hidden"
          onClick={() => setFiltersOpen(!filtersOpen)}
        >
          <Filter className="h-3.5 w-3.5" />
          Filters
          {filtersOpen ? <ChevronUp className="h-3 w-3" /> : <ChevronDown className="h-3 w-3" />}
        </Button>

        {/* Search */}
        <div className="relative min-w-0 w-full sm:min-w-[140px] sm:w-auto sm:flex-1 sm:max-w-xs">
          <Search className="absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            placeholder="Search tasks..."
            value={searchQuery}
            onChange={(e) => onSearchQueryChange(e.target.value)}
            className="h-8 pl-8 text-xs"
          />
        </div>

        {/* Spacer — only on desktop */}
        <div className="hidden sm:flex sm:flex-1" />

        {/* New Recurring */}
        {onNewRecurring && (
          <Button size="sm" variant="outline" className="h-8 w-8 p-0" onClick={onNewRecurring} title="New Recurring Task">
            <RefreshCw className="h-4 w-4" />
          </Button>
        )}

        {/* New Task */}
        <Button size="sm" className="h-8 w-8 p-0" onClick={onNewTask} title="New Task">
          <Plus className="h-4 w-4" />
        </Button>
      </div>

      {/* Mobile expanded filters */}
      {filtersOpen && (
        <div className="flex flex-wrap items-center gap-2 md:hidden">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="outline" size="sm" className="h-8 gap-1.5 px-2.5 text-xs">
                <ArrowUpDown className="h-3.5 w-3.5 text-muted-foreground" />
                {SORT_BY_LABELS[sortBy]}
                <ChevronDown className="h-3 w-3 opacity-60" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start" className="w-44">
              <DropdownMenuLabel className="text-xs">Sort within column</DropdownMenuLabel>
              <DropdownMenuSeparator />
              {(["manual", "updated", "priority", "due_date", "created", "title"] as SortBy[]).map((s) => (
                <DropdownMenuItem
                  key={s}
                  onClick={() => onSortByChange(s)}
                  className={cn("text-sm", sortBy === s && "font-medium text-primary")}
                >
                  {SORT_BY_LABELS[s]}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>

          <Select value={priorityFilter} onChange={(e) => onPriorityFilterChange(e.target.value)} className="h-8 w-32 text-xs">
            <option value="all">All Priorities</option>
            <option value="urgent">Urgent</option>
            <option value="high">High</option>
            <option value="medium">Medium</option>
            <option value="low">Low</option>
            <option value="none">None</option>
          </Select>

          <TagFilterDropdown allTags={allTags} selectedTags={selectedTags} onChange={onTagsChange} />
          <CustomFieldFilterDialog fields={filterableFields} filters={cfFilters} onChange={onCFFiltersChange} />

          <ToggleButton active={showClosed} onClick={() => onShowClosedChange(!showClosed)}>
            Closed
          </ToggleButton>

          <Select value={assigneeFilter} onChange={(e) => onAssigneeFilterChange(e.target.value)} className="h-8 w-32 text-xs">
            <option value="all">All Assignees</option>
            <option value="user">User</option>
            <option value="agent">Agent</option>
            <option value="unassigned">Unassigned</option>
          </Select>

          <AssigneeFilterDropdown
            candidates={assigneeCandidates}
            selectedIds={assigneeIdsFilter}
            onChange={onAssigneeIdsFilterChange}
          />
        </div>
      )}
    </div>
  );
}
