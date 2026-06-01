import { useState } from "react";
import { X } from "lucide-react";
import { cn } from "@/lib/cn";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import type { Agent, MemoryFilter, MemoryOrderBy } from "@/types";

// emptyFilter is the canonical "no filters applied" state.
export const emptyFilter: MemoryFilter = {
  q: "",
  scope: "all",
  tags: [],
  tagsMode: "all",
  sourceType: "",
  createdBy: "",
  since: "",
  until: "",
  relevanceMin: 0,
  includeExpired: false,
  includeArchived: false,
  orderBy: "created_at:desc",
};

// countActiveFilters counts how many facets diverge from the empty state.
// The free-text query (q) is intentionally excluded — it has its own input.
export function countActiveFilters(f: MemoryFilter): number {
  let n = 0;
  if (f.scope && f.scope !== "all") n++;
  if (f.tags && f.tags.length > 0) n++;
  if (f.sourceType) n++;
  if (f.sourceType === "agent" && f.createdBy) n++;
  if (f.since) n++;
  if (f.until) n++;
  if (typeof f.relevanceMin === "number" && f.relevanceMin > 0) n++;
  if (f.includeExpired) n++;
  if (f.includeArchived) n++;
  if (f.orderBy && f.orderBy !== "created_at:desc") n++;
  return n;
}

// ---------------------------------------------------------------------------
// Segmented control
// ---------------------------------------------------------------------------

interface SegmentedProps<T extends string> {
  value: T;
  options: { value: T; label: string }[];
  onChange: (v: T) => void;
}

function Segmented<T extends string>({ value, options, onChange }: SegmentedProps<T>) {
  return (
    <div className="inline-flex rounded-lg border border-border bg-muted/40 p-0.5">
      {options.map((opt) => (
        <button
          key={opt.value}
          type="button"
          onClick={() => onChange(opt.value)}
          className={cn(
            "rounded-md px-2.5 py-1 text-xs font-medium transition-colors",
            value === opt.value
              ? "bg-background text-foreground shadow-sm"
              : "text-muted-foreground hover:text-foreground",
          )}
        >
          {opt.label}
        </button>
      ))}
    </div>
  );
}

function FieldLabel({ children }: { children: React.ReactNode }) {
  return (
    <label className="text-xs font-medium text-muted-foreground mb-1.5 block">
      {children}
    </label>
  );
}

// ---------------------------------------------------------------------------
// Tag multi-select — toggles distinct tags from current results, plus free input
// ---------------------------------------------------------------------------

interface TagMultiSelectProps {
  selected: string[];
  available: string[];
  onChange: (tags: string[]) => void;
}

function TagMultiSelect({ selected, available, onChange }: TagMultiSelectProps) {
  const [input, setInput] = useState("");

  const toggle = (tag: string) => {
    if (selected.includes(tag)) {
      onChange(selected.filter((t) => t !== tag));
    } else {
      onChange([...selected, tag]);
    }
  };

  const addFromInput = () => {
    const tag = input.trim();
    if (tag && !selected.includes(tag)) onChange([...selected, tag]);
    setInput("");
  };

  // Suggestions = available tags not already selected.
  const suggestions = available.filter((t) => !selected.includes(t));

  return (
    <div className="space-y-2">
      {/* Selected chips */}
      {selected.length > 0 && (
        <div className="flex flex-wrap gap-1">
          {selected.map((tag) => (
            <Badge
              key={tag}
              variant="default"
              className="cursor-pointer gap-1"
              onClick={() => toggle(tag)}
            >
              {tag}
              <X className="h-3 w-3" />
            </Badge>
          ))}
        </div>
      )}

      {/* Free-text add */}
      <Input
        placeholder="Add tag and press Enter…"
        value={input}
        onChange={(e) => setInput(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            addFromInput();
          }
        }}
      />

      {/* Suggestions from current results */}
      {suggestions.length > 0 && (
        <div className="flex flex-wrap gap-1">
          {suggestions.slice(0, 30).map((tag) => (
            <Badge
              key={tag}
              variant="outline"
              className="cursor-pointer hover:bg-accent"
              onClick={() => toggle(tag)}
            >
              {tag}
            </Badge>
          ))}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// MemoryFiltersPanel
// ---------------------------------------------------------------------------

interface MemoryFiltersPanelProps {
  filter: MemoryFilter;
  onChange: (next: MemoryFilter) => void;
  onReset: () => void;
  agents: Agent[];
  availableTags: string[];
}

export function MemoryFiltersPanel({
  filter,
  onChange,
  onReset,
  agents,
  availableTags,
}: MemoryFiltersPanelProps) {
  const set = (patch: Partial<MemoryFilter>) => onChange({ ...filter, ...patch });
  const relPct = Math.round((filter.relevanceMin ?? 0) * 100);

  const presetRange = (days: number) => {
    const now = new Date();
    const from = new Date(now.getTime() - days * 24 * 60 * 60 * 1000);
    const fmt = (d: Date) => d.toISOString().slice(0, 10);
    set({ since: fmt(from), until: fmt(now) });
  };

  return (
    <div className="rounded-xl border border-border bg-card p-4 space-y-4">
      {/* Row 1: Scope + Source */}
      <div className="flex flex-wrap gap-x-8 gap-y-4">
        <div>
          <FieldLabel>Scope</FieldLabel>
          <Segmented
            value={filter.scope ?? "all"}
            onChange={(v) => set({ scope: v })}
            options={[
              { value: "all", label: "All" },
              { value: "workspace", label: "Workspace" },
              { value: "project", label: "Project" },
              { value: "agent", label: "Agent" },
            ]}
          />
        </div>
        <div>
          <FieldLabel>Source</FieldLabel>
          <Segmented
            value={filter.sourceType ?? ""}
            onChange={(v) =>
              set({ sourceType: v, createdBy: v === "agent" ? filter.createdBy : "" })
            }
            options={[
              { value: "", label: "All" },
              { value: "agent", label: "Agent" },
              { value: "human", label: "Human" },
              { value: "system", label: "System" },
            ]}
          />
        </div>
      </div>

      {/* Source agent (only when Source = Agent) */}
      {filter.sourceType === "agent" && (
        <div className="max-w-xs">
          <FieldLabel>Source agent</FieldLabel>
          <Select
            value={filter.createdBy ?? ""}
            onChange={(e) => set({ createdBy: e.target.value })}
          >
            <option value="">Any agent</option>
            {agents.map((a) => (
              <option key={a.id} value={a.id}>
                {a.name}
              </option>
            ))}
          </Select>
        </div>
      )}

      {/* Tags */}
      <div>
        <div className="mb-1.5 flex items-center justify-between">
          <FieldLabel>Tags</FieldLabel>
          <Segmented
            value={filter.tagsMode ?? "all"}
            onChange={(v) => set({ tagsMode: v })}
            options={[
              { value: "all", label: "Match all" },
              { value: "any", label: "Match any" },
            ]}
          />
        </div>
        <TagMultiSelect
          selected={filter.tags ?? []}
          available={availableTags}
          onChange={(tags) => set({ tags })}
        />
      </div>

      {/* Date range */}
      <div>
        <FieldLabel>Created between</FieldLabel>
        <div className="flex flex-wrap items-center gap-2">
          <Input
            type="date"
            value={filter.since ?? ""}
            onChange={(e) => set({ since: e.target.value })}
            className="w-40"
          />
          <span className="text-xs text-muted-foreground">→</span>
          <Input
            type="date"
            value={filter.until ?? ""}
            onChange={(e) => set({ until: e.target.value })}
            className="w-40"
          />
          <div className="flex gap-1">
            <Button type="button" size="sm" variant="outline" onClick={() => presetRange(1)}>
              24h
            </Button>
            <Button type="button" size="sm" variant="outline" onClick={() => presetRange(7)}>
              7d
            </Button>
            <Button type="button" size="sm" variant="outline" onClick={() => presetRange(30)}>
              30d
            </Button>
          </div>
        </div>
      </div>

      {/* Relevance + Sort */}
      <div className="flex flex-wrap gap-x-8 gap-y-4">
        <div className="min-w-[220px]">
          <FieldLabel>Minimum relevance — {relPct}%</FieldLabel>
          <div className="flex items-center gap-3">
            <input
              type="range"
              min={0}
              max={100}
              step={5}
              value={relPct}
              onChange={(e) => set({ relevanceMin: Number(e.target.value) / 100 })}
              className="h-1.5 flex-1 cursor-pointer accent-primary"
            />
            <Button
              type="button"
              size="sm"
              variant={(filter.relevanceMin ?? 0) >= 0.3 ? "secondary" : "outline"}
              onClick={() =>
                set({ relevanceMin: (filter.relevanceMin ?? 0) >= 0.3 ? 0 : 0.3 })
              }
            >
              Hide stale
            </Button>
          </div>
        </div>
        <div className="min-w-[200px]">
          <FieldLabel>Sort by</FieldLabel>
          <Select
            value={filter.orderBy ?? "created_at:desc"}
            onChange={(e) => set({ orderBy: e.target.value as MemoryOrderBy })}
          >
            <option value="created_at:desc">Most recent</option>
            <option value="relevance:desc">Most relevant</option>
            <option value="decayed_relevance:desc">Decayed relevance</option>
          </Select>
        </div>
      </div>

      {/* Include expired / archived + Reset */}
      <div className="flex items-center justify-between border-t border-border pt-3">
        <div className="flex flex-wrap items-center gap-4">
          <label className="flex cursor-pointer items-center gap-2 text-xs text-muted-foreground">
            <input
              type="checkbox"
              checked={filter.includeExpired ?? false}
              onChange={(e) => set({ includeExpired: e.target.checked })}
              className="h-3.5 w-3.5 cursor-pointer accent-primary"
            />
            Include expired memories
          </label>
          <label className="flex cursor-pointer items-center gap-2 text-xs text-muted-foreground">
            <input
              type="checkbox"
              checked={filter.includeArchived ?? false}
              onChange={(e) => set({ includeArchived: e.target.checked })}
              className="h-3.5 w-3.5 cursor-pointer accent-primary"
            />
            Show archived memories
          </label>
        </div>
        <Button type="button" size="sm" variant="ghost" onClick={onReset}>
          Reset filters
        </Button>
      </div>
    </div>
  );
}
