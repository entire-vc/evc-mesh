import type React from "react";
import { useState } from "react";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
import { DatePickerPopover } from "@/components/date-picker-popover";
import { cn } from "@/lib/cn";
import type { CustomFieldDefinition } from "@/types";

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
    case "agent_ref":
      return (
        <Input
          type="text"
          value={(value as string) ?? ""}
          onChange={(e) => onChange(e.target.value || null)}
          placeholder={`Enter ${field.field_type === "user_ref" ? "user" : "agent"} ID...`}
          className={cn("h-8 text-xs", compact && "h-7")}
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
