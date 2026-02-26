/**
 * view-filters.tsx — shared tag + custom field filter components
 * Used in both BoardPage and ListViewPage toolbars.
 */

import { useCallback, useRef, useEffect, useState } from "react";
import { Tag, SlidersHorizontal, X, ChevronDown } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from "@/components/ui/dialog";
import { cn } from "@/lib/cn";
import type { CustomFieldDefinition, Task } from "@/types";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export interface CFFilterValue {
  fieldSlug: string;
  operator: "eq" | "contains" | "gte" | "lte";
  value: string;
}

// Internal representation for dialog state: one entry per active field slug
export type CFFilters = Record<string, CFFilterEntry>;

export interface CFFilterEntry {
  type: "text" | "select" | "multiselect" | "number" | "checkbox" | "date";
  // For text/url/email/json: contains string
  textValue?: string;
  // For select: exact value or "all"
  selectValue?: string;
  // For multiselect: array of selected values
  multiselectValues?: string[];
  // For number: min/max
  numMin?: string;
  numMax?: string;
  // For checkbox: "all" | "checked" | "unchecked"
  checkboxValue?: string;
  // For date/datetime: from / to (ISO date strings, YYYY-MM-DD)
  dateFrom?: string;
  dateTo?: string;
}

// ---------------------------------------------------------------------------
// Pure filter function — apply tag + CF filters to a task array
// ---------------------------------------------------------------------------

export function applyViewFilters(
  tasks: Task[],
  selectedTags: string[],
  cfFilters: CFFilters,
): Task[] {
  return tasks.filter((task) => {
    // Tag filter: task must have ANY of the selected tags
    if (selectedTags.length > 0) {
      const taskLabels = task.labels ?? [];
      const hasTag = selectedTags.some((t) => taskLabels.includes(t));
      if (!hasTag) return false;
    }

    // Custom field filters
    for (const [slug, entry] of Object.entries(cfFilters)) {
      const rawValue = task.custom_fields?.[slug];

      if (entry.type === "text") {
        if (!entry.textValue) continue;
        const strVal = rawValue != null ? String(rawValue) : "";
        if (!strVal.toLowerCase().includes(entry.textValue.toLowerCase())) {
          return false;
        }
      } else if (entry.type === "select") {
        if (!entry.selectValue || entry.selectValue === "all") continue;
        if (rawValue !== entry.selectValue) return false;
      } else if (entry.type === "multiselect") {
        if (!entry.multiselectValues || entry.multiselectValues.length === 0) continue;
        // Task value can be a single string or array of strings
        const taskVals: string[] = Array.isArray(rawValue)
          ? (rawValue as string[])
          : rawValue != null
            ? [String(rawValue)]
            : [];
        const hasMatch = entry.multiselectValues.some((v) => taskVals.includes(v));
        if (!hasMatch) return false;
      } else if (entry.type === "number") {
        const numVal = rawValue != null ? Number(rawValue) : null;
        if (entry.numMin !== undefined && entry.numMin !== "") {
          if (numVal == null || numVal < Number(entry.numMin)) return false;
        }
        if (entry.numMax !== undefined && entry.numMax !== "") {
          if (numVal == null || numVal > Number(entry.numMax)) return false;
        }
      } else if (entry.type === "checkbox") {
        if (!entry.checkboxValue || entry.checkboxValue === "all") continue;
        const boolVal = Boolean(rawValue);
        if (entry.checkboxValue === "checked" && !boolVal) return false;
        if (entry.checkboxValue === "unchecked" && boolVal) return false;
      } else if (entry.type === "date") {
        const taskDate = rawValue != null ? String(rawValue).slice(0, 10) : null;
        if (entry.dateFrom) {
          if (!taskDate || taskDate < entry.dateFrom) return false;
        }
        if (entry.dateTo) {
          if (!taskDate || taskDate > entry.dateTo) return false;
        }
      }
    }

    return true;
  });
}

// ---------------------------------------------------------------------------
// Count active CF filters
// ---------------------------------------------------------------------------

export function countActiveCFFilters(cfFilters: CFFilters): number {
  let count = 0;
  for (const entry of Object.values(cfFilters)) {
    if (entry.type === "text" && entry.textValue) count++;
    else if (entry.type === "select" && entry.selectValue && entry.selectValue !== "all") count++;
    else if (entry.type === "multiselect" && entry.multiselectValues && entry.multiselectValues.length > 0) count++;
    else if (entry.type === "number" && (entry.numMin || entry.numMax)) count++;
    else if (entry.type === "checkbox" && entry.checkboxValue && entry.checkboxValue !== "all") count++;
    else if (entry.type === "date" && (entry.dateFrom || entry.dateTo)) count++;
  }
  return count;
}

// ---------------------------------------------------------------------------
// Tag filter dropdown — uses a custom popover (not DropdownMenu, since we need
// checkboxes that stay open on click)
// ---------------------------------------------------------------------------

interface TagFilterDropdownProps {
  allTags: string[];
  selectedTags: string[];
  onChange: (tags: string[]) => void;
  className?: string;
}

export function TagFilterDropdown({
  allTags,
  selectedTags,
  onChange,
  className,
}: TagFilterDropdownProps) {
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

  const toggleTag = useCallback(
    (tag: string) => {
      if (selectedTags.includes(tag)) {
        onChange(selectedTags.filter((t) => t !== tag));
      } else {
        onChange([...selectedTags, tag]);
      }
    },
    [selectedTags, onChange],
  );

  const clearAll = useCallback(() => onChange([]), [onChange]);

  const activeCount = selectedTags.length;

  // If there are no tags at all across tasks, don't render
  if (allTags.length === 0) return null;

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
        <Tag className="h-3.5 w-3.5" />
        {activeCount > 0 && (
          <Badge variant="secondary" className="ml-0.5 h-4 px-1 text-[10px]">
            {activeCount}
          </Badge>
        )}
        <ChevronDown className="h-3 w-3 opacity-60" />
      </Button>

      {open && (
        <div className="absolute left-0 z-50 mt-2 w-56 rounded-lg border border-border bg-popover p-2 shadow-lg">
          <div className="mb-1.5 flex items-center justify-between px-1">
            <span className="text-xs font-semibold text-foreground">Filter by Tag</span>
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
          <div className="max-h-56 overflow-y-auto">
            {allTags.map((tag) => (
              <label
                key={tag}
                className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-sm hover:bg-accent"
              >
                <input
                  type="checkbox"
                  checked={selectedTags.includes(tag)}
                  onChange={() => toggleTag(tag)}
                  className="h-3.5 w-3.5 rounded border-input"
                />
                <span className="truncate">{tag}</span>
              </label>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Custom Field Filter Dialog
// ---------------------------------------------------------------------------

interface CustomFieldFilterDialogProps {
  fields: CustomFieldDefinition[];
  filters: CFFilters;
  onChange: (filters: CFFilters) => void;
  className?: string;
}

export function CustomFieldFilterDialog({
  fields,
  filters,
  onChange,
  className,
}: CustomFieldFilterDialogProps) {
  const [open, setOpen] = useState(false);
  // Local draft — committed on Apply
  const [draft, setDraft] = useState<CFFilters>({});

  // When dialog opens, seed draft from current filters
  const handleOpen = useCallback(() => {
    setDraft(structuredClone(filters));
    setOpen(true);
  }, [filters]);

  const handleApply = useCallback(() => {
    onChange(draft);
    setOpen(false);
  }, [draft, onChange]);

  const handleClear = useCallback(() => {
    setDraft({});
    onChange({});
    setOpen(false);
  }, [onChange]);

  const activeCount = countActiveCFFilters(filters);

  const updateEntry = useCallback(
    (slug: string, patch: Partial<CFFilterEntry>) => {
      setDraft((prev) => ({
        ...prev,
        [slug]: { ...(prev[slug] ?? {}), ...patch } as CFFilterEntry,
      }));
    },
    [],
  );

  if (fields.length === 0) return null;

  return (
    <>
      <Button
        variant="outline"
        size="sm"
        className={cn(
          "h-8 gap-1.5 px-2.5 text-xs",
          activeCount > 0 && "border-primary/50 bg-primary/10 text-primary",
          className,
        )}
        onClick={handleOpen}
      >
        <SlidersHorizontal className="h-3.5 w-3.5" />
        Filters
        {activeCount > 0 && (
          <Badge variant="secondary" className="ml-0.5 h-4 px-1 text-[10px]">
            {activeCount}
          </Badge>
        )}
      </Button>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-w-lg max-h-[80vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>Filter by Custom Fields</DialogTitle>
          </DialogHeader>

          <div className="space-y-4 py-2">
            {fields.map((field) => (
              <CFFieldFilterRow
                key={field.id}
                field={field}
                entry={draft[field.slug]}
                onChange={(patch) => updateEntry(field.slug, patch)}
              />
            ))}
          </div>

          <DialogFooter>
            <Button variant="outline" size="sm" onClick={handleClear}>
              Clear all
            </Button>
            <Button size="sm" onClick={handleApply}>
              Apply
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

// ---------------------------------------------------------------------------
// Per-field filter row inside the dialog
// ---------------------------------------------------------------------------

function CFFieldFilterRow({
  field,
  entry,
  onChange,
}: {
  field: CustomFieldDefinition;
  entry: CFFilterEntry | undefined;
  onChange: (patch: Partial<CFFilterEntry>) => void;
}) {
  const ft = field.field_type;

  // Resolve choices for select/multiselect
  const choices = (
    (field.options?.choices ?? []) as Array<string | { label: string; value: string }>
  ).map((c) => {
    if (typeof c === "string") return { label: c, value: c };
    return { label: c.label, value: c.value };
  });

  return (
    <div className="space-y-1.5">
      <label className="text-xs font-medium text-muted-foreground">{field.name}</label>

      {/* text / url / email / user_ref / agent_ref / json — text contains */}
      {(ft === "text" ||
        ft === "url" ||
        ft === "email" ||
        ft === "user_ref" ||
        ft === "agent_ref" ||
        ft === "json") && (
        <Input
          placeholder={`Contains...`}
          className="h-8 text-xs"
          value={entry?.textValue ?? ""}
          onChange={(e) =>
            onChange({ type: "text", textValue: e.target.value })
          }
        />
      )}

      {/* select */}
      {ft === "select" && (
        <Select
          className="h-8 text-xs"
          value={entry?.selectValue ?? "all"}
          onChange={(e) =>
            onChange({ type: "select", selectValue: e.target.value })
          }
        >
          <option value="all">Any</option>
          {choices.map((c) => (
            <option key={c.value} value={c.value}>
              {c.label}
            </option>
          ))}
        </Select>
      )}

      {/* multiselect — checklist */}
      {ft === "multiselect" && (
        <div className="flex flex-wrap gap-1.5 rounded-md border border-border p-2">
          {choices.length === 0 && (
            <span className="text-xs text-muted-foreground">No options defined</span>
          )}
          {choices.map((c) => {
            const selected = (entry?.multiselectValues ?? []).includes(c.value);
            return (
              <label key={c.value} className="flex cursor-pointer items-center gap-1.5 text-xs">
                <input
                  type="checkbox"
                  checked={selected}
                  className="h-3 w-3 rounded border-input"
                  onChange={() => {
                    const prev = entry?.multiselectValues ?? [];
                    const next = selected
                      ? prev.filter((v) => v !== c.value)
                      : [...prev, c.value];
                    onChange({ type: "multiselect", multiselectValues: next });
                  }}
                />
                {c.label}
              </label>
            );
          })}
        </div>
      )}

      {/* number — min/max range */}
      {ft === "number" && (
        <div className="flex items-center gap-2">
          <Input
            type="number"
            placeholder="Min"
            className="h-8 w-24 text-xs"
            value={entry?.numMin ?? ""}
            onChange={(e) =>
              onChange({ type: "number", numMin: e.target.value })
            }
          />
          <span className="text-xs text-muted-foreground">—</span>
          <Input
            type="number"
            placeholder="Max"
            className="h-8 w-24 text-xs"
            value={entry?.numMax ?? ""}
            onChange={(e) =>
              onChange({ type: "number", numMax: e.target.value })
            }
          />
        </div>
      )}

      {/* checkbox */}
      {ft === "checkbox" && (
        <Select
          className="h-8 text-xs"
          value={entry?.checkboxValue ?? "all"}
          onChange={(e) =>
            onChange({ type: "checkbox", checkboxValue: e.target.value })
          }
        >
          <option value="all">Any</option>
          <option value="checked">Checked</option>
          <option value="unchecked">Unchecked</option>
        </Select>
      )}

      {/* date / datetime — from / to */}
      {(ft === "date" || ft === "datetime") && (
        <div className="flex items-center gap-2">
          <div className="flex flex-col gap-0.5">
            <span className="text-[10px] text-muted-foreground">From</span>
            <Input
              type="date"
              className="h-8 text-xs"
              value={entry?.dateFrom ?? ""}
              onChange={(e) =>
                onChange({ type: "date", dateFrom: e.target.value })
              }
            />
          </div>
          <div className="flex flex-col gap-0.5">
            <span className="text-[10px] text-muted-foreground">To</span>
            <Input
              type="date"
              className="h-8 text-xs"
              value={entry?.dateTo ?? ""}
              onChange={(e) =>
                onChange({ type: "date", dateTo: e.target.value })
              }
            />
          </div>
        </div>
      )}
    </div>
  );
}
