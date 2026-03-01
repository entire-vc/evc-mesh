import type React from "react";
import { useEffect, useMemo, useRef, useState } from "react";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
import { DatePickerPopover } from "@/components/date-picker-popover";
import { cn } from "@/lib/cn";
import { useWorkspaceStore } from "@/stores/workspace";
import { useMemberStore } from "@/stores/member";
import { useAgentStore } from "@/stores/agent";
import type { Agent, CustomFieldDefinition, WorkspaceMemberWithUser } from "@/types";

// Determine whether to use dark or light text on a given background color.
function getContrastColor(hexColor: string): string {
  const hex = hexColor.replace("#", "");
  if (hex.length < 6) return "#000";
  const r = parseInt(hex.slice(0, 2), 16);
  const g = parseInt(hex.slice(2, 4), 16);
  const b = parseInt(hex.slice(4, 6), 16);
  // Luminance formula
  const luminance = (0.299 * r + 0.587 * g + 0.114 * b) / 255;
  return luminance > 0.5 ? "#000" : "#fff";
}

// Helper to extract typed choices from options.
interface FieldChoice {
  label: string;
  value: string;
  color?: string;
}

function getChoices(options: Record<string, unknown>): FieldChoice[] {
  const raw = options?.choices;
  if (!Array.isArray(raw)) return [];
  // Support both { label, value } objects and plain strings.
  return raw.map((item: unknown) => {
    if (typeof item === "string") {
      return { label: item, value: item };
    }
    const obj = item as Record<string, unknown>;
    return {
      label: String(obj.label ?? obj.value ?? item),
      value: String(obj.value ?? obj.label ?? item),
      color: obj.color ? String(obj.color) : undefined,
    };
  });
}

function getPlaceholder(
  options: Record<string, unknown>,
  fallback: string,
): string {
  const p = options?.placeholder;
  return typeof p === "string" ? p : fallback;
}

// ---------------------------------------------------------------------------
// Searchable dropdown select for user_ref / agent_ref field types
// ---------------------------------------------------------------------------

interface RefSelectOption {
  id: string;
  label: string;
  sublabel?: string;
  badge: string;
}

function RefSelect({
  value,
  onChange,
  options,
  placeholder,
  compact,
}: {
  value: string;
  onChange: (id: string | null) => void;
  options: RefSelectOption[];
  placeholder: string;
  compact?: boolean;
}) {
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  const filtered = useMemo(() => {
    const q = query.toLowerCase();
    if (!q) return options;
    return options.filter(
      (o) =>
        o.label.toLowerCase().includes(q) ||
        (o.sublabel ?? "").toLowerCase().includes(q),
    );
  }, [query, options]);

  const selectedOption = options.find((o) => o.id === value);

  // Close dropdown on outside click.
  useEffect(() => {
    if (!open) return;
    function handleClick(e: MouseEvent) {
      if (
        containerRef.current &&
        !containerRef.current.contains(e.target as Node)
      ) {
        setOpen(false);
      }
    }
    document.addEventListener("mousedown", handleClick);
    return () => document.removeEventListener("mousedown", handleClick);
  }, [open]);

  return (
    <div ref={containerRef} className="relative">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className={cn(
          "flex w-full items-center justify-between rounded-md border border-input bg-background px-3 text-left text-xs shadow-sm hover:bg-accent/50 focus:outline-none focus:ring-1 focus:ring-ring",
          compact ? "h-7" : "h-8",
        )}
      >
        {selectedOption ? (
          <span className="flex items-center gap-1.5 truncate">
            <Badge variant="secondary" className="px-1 py-0 text-[10px]">
              {selectedOption.badge}
            </Badge>
            <span className="truncate">{selectedOption.label}</span>
          </span>
        ) : (
          <span className="text-muted-foreground">{placeholder}</span>
        )}
        <svg
          className="ml-1 h-3 w-3 shrink-0 text-muted-foreground"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
        >
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={2}
            d="M19 9l-7 7-7-7"
          />
        </svg>
      </button>

      {open && (
        <div className="absolute z-50 mt-1 w-full rounded-md border border-border bg-popover shadow-lg">
          <div className="p-1">
            <Input
              autoFocus
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search..."
              className="h-7 text-xs"
            />
          </div>
          <ul className="max-h-48 overflow-y-auto py-1">
            {filtered.length === 0 ? (
              <li className="px-3 py-2 text-xs text-muted-foreground">
                No results
              </li>
            ) : (
              filtered.map((opt) => (
                <li key={opt.id}>
                  <button
                    type="button"
                    className={cn(
                      "flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs hover:bg-accent",
                      value === opt.id && "bg-accent/50 font-medium",
                    )}
                    onClick={() => {
                      onChange(opt.id === value ? null : opt.id);
                      setOpen(false);
                      setQuery("");
                    }}
                  >
                    <Badge variant="secondary" className="px-1 py-0 text-[10px] shrink-0">
                      {opt.badge}
                    </Badge>
                    <span className="flex-1 truncate">{opt.label}</span>
                    {opt.sublabel && (
                      <span className="truncate text-muted-foreground">
                        {opt.sublabel}
                      </span>
                    )}
                  </button>
                </li>
              ))
            )}
          </ul>
          {selectedOption && (
            <div className="border-t border-border px-3 py-1.5">
              <button
                type="button"
                className="text-xs text-destructive hover:underline"
                onClick={() => {
                  onChange(null);
                  setOpen(false);
                  setQuery("");
                }}
              >
                Clear selection
              </button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// UserRefSelect wraps RefSelect, loading workspace members on mount.
function UserRefSelect({
  value,
  onChange,
  compact,
}: {
  value: string;
  onChange: (id: string | null) => void;
  compact?: boolean;
}) {
  const { currentWorkspace } = useWorkspaceStore();
  const { workspaceMembers, fetchWorkspaceMembers } = useMemberStore();

  useEffect(() => {
    if (currentWorkspace && workspaceMembers.length === 0) {
      fetchWorkspaceMembers(currentWorkspace.id);
    }
  }, [currentWorkspace, workspaceMembers.length, fetchWorkspaceMembers]);

  const options: RefSelectOption[] = workspaceMembers.map(
    (m: WorkspaceMemberWithUser) => ({
      id: m.user_id,
      label: m.user.name || m.user.email,
      sublabel: m.user.email,
      badge: m.role,
    }),
  );

  return (
    <RefSelect
      value={value}
      onChange={onChange}
      options={options}
      placeholder="Select user..."
      compact={compact}
    />
  );
}

// AgentRefSelect wraps RefSelect, loading workspace agents on mount.
function AgentRefSelect({
  value,
  onChange,
  compact,
}: {
  value: string;
  onChange: (id: string | null) => void;
  compact?: boolean;
}) {
  const { currentWorkspace } = useWorkspaceStore();
  const { agents, fetchAgents } = useAgentStore();

  useEffect(() => {
    if (currentWorkspace && agents.length === 0) {
      fetchAgents(currentWorkspace.id);
    }
  }, [currentWorkspace, agents.length, fetchAgents]);

  const options: RefSelectOption[] = agents.map((a: Agent) => ({
    id: a.id,
    label: a.name,
    sublabel: a.agent_type,
    badge: a.status,
  }));

  return (
    <RefSelect
      value={value}
      onChange={onChange}
      options={options}
      placeholder="Select agent..."
      compact={compact}
    />
  );
}

export interface CustomFieldRendererProps {
  field: CustomFieldDefinition;
  value: unknown;
  onChange: (value: unknown) => void;
  readOnly?: boolean;
  compact?: boolean;
}

export function CustomFieldRenderer({
  field,
  value,
  onChange,
  readOnly = false,
  compact = false,
}: CustomFieldRendererProps) {
  const [jsonDraft, setJsonDraft] = useState<string>(
    field.field_type === "json" && value != null
      ? JSON.stringify(value, null, 2)
      : "",
  );

  if (readOnly) {
    return <CustomFieldReadOnly field={field} value={value} compact={compact} />;
  }

  switch (field.field_type) {
    case "text":
      return (
        <Input
          type="text"
          value={(value as string) ?? ""}
          onChange={(e) => onChange(e.target.value || null)}
          placeholder={getPlaceholder(field.options, `Enter ${field.name}...`)}
          className={cn("h-8 text-xs", compact && "h-7")}
        />
      );

    case "number": {
      const minVal = field.options?.min as number | undefined;
      const maxVal = field.options?.max as number | undefined;
      return (
        <Input
          type="number"
          value={value != null ? String(value) : ""}
          onChange={(e) => {
            const v = e.target.value;
            onChange(v === "" ? null : Number(v));
          }}
          min={minVal}
          max={maxVal}
          placeholder={getPlaceholder(field.options, `Enter ${field.name}...`)}
          className={cn("h-8 text-xs", compact && "h-7")}
        />
      );
    }

    case "date":
      return (
        <DatePickerPopover
          value={(value as string) ?? null}
          onChange={(val) => onChange(val)}
          placeholder={getPlaceholder(field.options, `Set ${field.name}...`)}
          className={compact ? "text-xs" : ""}
        />
      );

    case "datetime":
      return (
        <DatePickerPopover
          value={(value as string) ?? null}
          onChange={(val) => onChange(val)}
          includeTime
          placeholder={getPlaceholder(field.options, `Set ${field.name}...`)}
          className={compact ? "text-xs" : ""}
        />
      );

    case "select": {
      const choices = getChoices(field.options);
      return (
        <Select
          value={(value as string) ?? ""}
          onChange={(e) => onChange(e.target.value || null)}
          className={cn("h-8 text-xs", compact && "h-7")}
        >
          <option value="">-- Select --</option>
          {choices.map((choice) => (
            <option key={choice.value} value={choice.value}>
              {choice.label}
            </option>
          ))}
        </Select>
      );
    }

    case "multiselect": {
      const selected = Array.isArray(value) ? (value as string[]) : [];
      const choices = getChoices(field.options);
      return (
        <div className="space-y-1">
          {choices.map((choice) => {
            const checked = selected.includes(choice.value);
            return (
              <label
                key={choice.value}
                className="flex items-center gap-2 text-xs"
              >
                <input
                  type="checkbox"
                  checked={checked}
                  onChange={() => {
                    const next = checked
                      ? selected.filter((v) => v !== choice.value)
                      : [...selected, choice.value];
                    onChange(next.length > 0 ? next : null);
                  }}
                  className="h-3.5 w-3.5 rounded border-input"
                />
                {choice.label}
              </label>
            );
          })}
        </div>
      );
    }

    case "url":
      return (
        <Input
          type="url"
          value={(value as string) ?? ""}
          onChange={(e) => onChange(e.target.value || null)}
          placeholder={getPlaceholder(field.options, "https://...")}
          className={cn("h-8 text-xs", compact && "h-7")}
        />
      );

    case "email":
      return (
        <Input
          type="email"
          value={(value as string) ?? ""}
          onChange={(e) => onChange(e.target.value || null)}
          placeholder={getPlaceholder(field.options, "email@example.com")}
          className={cn("h-8 text-xs", compact && "h-7")}
        />
      );

    case "checkbox":
      return (
        <label className="flex items-center gap-2 text-xs">
          <input
            type="checkbox"
            checked={Boolean(value)}
            onChange={(e) => onChange(e.target.checked)}
            className="h-4 w-4 rounded border-input"
          />
          <span className="text-muted-foreground">{field.name}</span>
        </label>
      );

    case "user_ref":
      return (
        <UserRefSelect
          value={(value as string) ?? ""}
          onChange={onChange}
          compact={compact}
        />
      );

    case "agent_ref":
      return (
        <AgentRefSelect
          value={(value as string) ?? ""}
          onChange={onChange}
          compact={compact}
        />
      );

    case "json":
      return (
        <Textarea
          value={jsonDraft}
          onChange={(e) => {
            setJsonDraft(e.target.value);
          }}
          onBlur={() => {
            if (!jsonDraft.trim()) {
              onChange(null);
              return;
            }
            try {
              const parsed = JSON.parse(jsonDraft);
              onChange(parsed);
            } catch {
              // Keep the draft as-is, user can fix it
            }
          }}
          placeholder='{"key": "value"}'
          rows={3}
          className="text-xs font-mono"
        />
      );

    default:
      return (
        <span className="text-xs text-muted-foreground">
          Unsupported field type: {field.field_type}
        </span>
      );
  }
}

// ---------------------------------------------------------------------------
// Read-only display
// ---------------------------------------------------------------------------

function CustomFieldReadOnly({
  field,
  value,
  compact,
}: {
  field: CustomFieldDefinition;
  value: unknown;
  compact?: boolean;
}) {
  if (value == null || value === "") {
    if (compact) return null;
    return <span className="text-xs text-muted-foreground">--</span>;
  }

  switch (field.field_type) {
    case "checkbox":
      return (
        <span
          className={cn(
            "inline-block h-2.5 w-2.5 rounded-full",
            value ? "bg-green-500" : "bg-gray-400",
          )}
          title={value ? "Yes" : "No"}
        />
      );

    case "select": {
      const choices = getChoices(field.options);
      const choice = choices.find((c) => c.value === value);
      const badgeStyle: React.CSSProperties | undefined = choice?.color
        ? { backgroundColor: choice.color, color: getContrastColor(choice.color) }
        : undefined;
      return (
        <Badge
          variant="secondary"
          className={cn("text-[10px]", compact && "px-1 py-0")}
          style={badgeStyle}
        >
          {choice?.label ?? String(value)}
        </Badge>
      );
    }

    case "multiselect": {
      const selected = Array.isArray(value) ? (value as string[]) : [];
      if (selected.length === 0) {
        if (compact) return null;
        return <span className="text-xs text-muted-foreground">--</span>;
      }
      const choices = getChoices(field.options);
      return (
        <div className="flex flex-wrap gap-0.5">
          {selected.map((v) => {
            const choice = choices.find((c) => c.value === v);
            const style: React.CSSProperties | undefined = choice?.color
              ? { backgroundColor: choice.color, color: getContrastColor(choice.color), borderColor: choice.color }
              : undefined;
            return (
              <Badge
                key={v}
                variant="outline"
                className={cn("text-[10px]", compact && "px-1 py-0")}
                style={style}
              >
                {choice?.label ?? v}
              </Badge>
            );
          })}
        </div>
      );
    }

    case "url": {
      const urlStr = String(value);
      let displayUrl = urlStr;
      if (compact) {
        try {
          displayUrl = new URL(urlStr).hostname;
        } catch {
          // not a valid URL, show as-is
        }
      }
      return (
        <a
          href={urlStr}
          target="_blank"
          rel="noopener noreferrer"
          className={cn(
            "text-xs text-primary underline-offset-2 hover:underline",
            compact && "truncate text-[10px]",
          )}
        >
          {displayUrl}
        </a>
      );
    }

    case "date":
    case "datetime":
      return (
        <span className={cn("text-xs", compact && "text-[10px] text-muted-foreground")}>
          {String(value)}
        </span>
      );

    case "json":
      return (
        <pre className="max-h-20 overflow-auto rounded bg-muted p-1 text-[10px] font-mono">
          {typeof value === "string" ? value : JSON.stringify(value, null, 2)}
        </pre>
      );

    case "number":
      return (
        <span className={cn("text-xs", compact && "text-[10px] text-muted-foreground")}>
          {String(value)}
        </span>
      );

    default:
      return (
        <span
          className={cn(
            "text-xs",
            compact && "truncate text-[10px] text-muted-foreground",
          )}
        >
          {String(value)}
        </span>
      );
  }
}

// ---------------------------------------------------------------------------
// Compact display for card preview (only shows non-empty, non-complex fields)
// ---------------------------------------------------------------------------

const CARD_PREVIEW_TYPES = new Set([
  "text",
  "number",
  "select",
  "checkbox",
  "url",
  "email",
  "date",
]);

export function shouldShowInCardPreview(
  field: CustomFieldDefinition,
  value: unknown,
): boolean {
  if (value == null || value === "") return false;
  return CARD_PREVIEW_TYPES.has(field.field_type);
}
