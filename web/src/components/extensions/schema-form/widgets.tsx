import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ArrowDown, ArrowUp, ImagePlus, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { cn } from "@/lib/cn";
import { hexToHsl, hslToHex } from "@/components/themes/color";
import type { FieldSchema } from "./types";

export interface PickerOption {
  value: string;
  label: string;
}

const HEX_EXAMPLE = "#1a73e8"; // color-ok: placeholder text, not a style
export const HEX_COLOR = /^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/;
/** Largest image a setting may carry: it is stored inside the settings JSON as a data URL. */
export const IMAGE_MAX_BYTES = 200 * 1024;
const IMAGE_TYPES = ["image/png", "image/jpeg", "image/svg+xml", "image/webp"];

/** Font stacks a theme may ask for by name; the value stored is the stack, so no font file is loaded. */
export const FONT_OPTIONS: readonly PickerOption[] = [
  { value: "system-ui, sans-serif", label: "System" },
  { value: "Inter, system-ui, sans-serif", label: "Inter" },
  { value: "Georgia, 'Times New Roman', serif", label: "Georgia" },
  { value: "ui-serif, Charter, serif", label: "Charter" },
  { value: "ui-monospace, 'SF Mono', Menlo, monospace", label: "Monospace" },
];

interface BaseProps {
  id: string;
  disabled: boolean;
  invalid?: boolean;
  describedBy?: string;
  label: string;
}

const expandHex = (h: string) => (h.length === 4 ? `#${h[1]}${h[1]}${h[2]}${h[2]}${h[3]}${h[3]}` : h);

/** `#rrggbb` value: native picker for choosing, text input for pasting an exact value.
 *  `fallback` is what applies while the field is unset (same encoding as the value): the swatch and the
 *  placeholder show it; without one the swatch stays empty instead of painting a colour nobody chose. */
export function ColorWidget({ id, disabled, invalid, describedBy, label, value, hsl, fallback, onChange }: BaseProps & { value: unknown; hsl?: boolean; fallback?: string; onChange: (v: string | undefined) => void }) {
  const { t } = useTranslation("integrations");
  // a token-bound field stores "H S% L%"; the user always sees and types #hex
  const stored = typeof value === "string" ? value : "";
  const shown = hsl ? (hslToHex(stored) ?? stored) : stored;
  const [typed, setTyped] = useState<string | null>(null);
  const text = typed ?? shown;
  const valid = HEX_COLOR.test(text);
  const fallbackHex = fallback ? (hsl ? hslToHex(fallback) : HEX_COLOR.test(fallback) ? fallback : null) : null;
  const empty = text === "" && !fallbackHex;
  const swatch = valid ? expandHex(text) : text === "" && fallbackHex ? expandHex(fallbackHex) : "#000000"; // color-ok: <input type=color> needs some value; hidden while empty
  const emit = (v: string) => {
    setTyped(v === "" || HEX_COLOR.test(v) ? null : v);
    if (v === "") return onChange(undefined);
    // malformed text goes up as is so form validation blocks the save instead of keeping the old value
    if (!HEX_COLOR.test(v)) return onChange(v);
    onChange(hsl ? (hexToHsl(v) ?? undefined) : v);
  };
  return (
    <div className="flex items-center gap-2">
      <input
        type="color"
        aria-label={t("schemaForm.color.pick", { name: label })}
        disabled={disabled}
        value={swatch}
        data-empty={empty || undefined}
        onChange={(e) => emit(e.target.value)}
        className={cn(
          "h-9 w-12 shrink-0 cursor-pointer rounded-lg border border-input bg-background p-1 disabled:cursor-not-allowed disabled:opacity-50",
          empty && "border-dashed [&::-moz-color-swatch]:opacity-0 [&::-webkit-color-swatch]:opacity-0",
        )}
      />
      <Input
        id={id}
        disabled={disabled}
        aria-invalid={invalid || (text !== "" && !valid) || undefined}
        aria-describedby={describedBy}
        placeholder={fallbackHex ?? HEX_EXAMPLE}
        spellCheck={false}
        maxLength={7}
        value={text}
        onChange={(e) => emit(e.target.value)}
        className="font-mono"
      />
      {text !== "" && !valid && (
        <span role="alert" className="shrink-0 text-xs text-destructive">
          {t("schemaForm.color.invalid")}
        </span>
      )}
    </div>
  );
}

/** Pick from the font stacks; the field's `enum` (with `x-options` labels) wins over the built-in list. */
export function FontWidget({ field, id, disabled, invalid, describedBy, value, onChange }: BaseProps & { field: FieldSchema; value: unknown; onChange: (v: string | undefined) => void }) {
  const { t } = useTranslation("integrations");
  const options: PickerOption[] = field.enum ? field.enum.map((e) => ({ value: String(e), label: field["x-options"]?.[String(e)] ?? String(e) })) : [...FONT_OPTIONS];
  const current = typeof value === "string" ? value : "";
  return (
    <div className="space-y-2">
      <Select id={id} disabled={disabled} aria-invalid={invalid || undefined} aria-describedby={describedBy} value={current} onChange={(e) => onChange(e.target.value === "" ? undefined : e.target.value)}>
        <option value="">{t("schemaForm.font.themeDefault")}</option>
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </Select>
      {current !== "" && (
        <p className="truncate rounded-md border border-border px-3 py-2 text-sm" style={{ fontFamily: current }}>
          {t("schemaForm.font.sample")}
        </p>
      )}
    </div>
  );
}

/** Image kept in the settings as a data URL (≤200 KB): a logo, a login background. */
export function ImageWidget({ id, disabled, label, value, onChange }: BaseProps & { value: unknown; onChange: (v: string | undefined) => void }) {
  const { t } = useTranslation("integrations");
  const input = useRef<HTMLInputElement>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const src = typeof value === "string" && value.startsWith("data:image/") ? value : null;

  const pick = (file: File | undefined) => {
    if (!file) return;
    setProblem(null);
    if (!IMAGE_TYPES.includes(file.type)) return setProblem(t("schemaForm.image.badType"));
    if (file.size > IMAGE_MAX_BYTES) return setProblem(t("schemaForm.image.tooLarge", { kb: IMAGE_MAX_BYTES / 1024 }));
    const reader = new FileReader();
    reader.onload = () => onChange(String(reader.result));
    reader.onerror = () => setProblem(t("schemaForm.image.readFailed"));
    reader.readAsDataURL(file);
  };

  return (
    <div className="flex items-center gap-3">
      <div className="flex h-16 w-24 shrink-0 items-center justify-center overflow-hidden rounded-lg border bg-muted">
        {src ? <img src={src} alt={label} className="h-full w-full object-contain" /> : <ImagePlus className="h-5 w-5 text-muted-foreground" aria-hidden />}
      </div>
      <div className="space-y-1.5">
        <input ref={input} id={id} type="file" accept={IMAGE_TYPES.join(",")} className="sr-only" disabled={disabled} onChange={(e) => { pick(e.target.files?.[0]); e.target.value = ""; }} />
        <div className="flex gap-2">
          <Button type="button" variant="outline" size="sm" disabled={disabled} onClick={() => input.current?.click()}>
            {src ? t("schemaForm.image.replace") : t("schemaForm.image.upload")}
          </Button>
          {src && (
            <Button type="button" variant="outline" size="sm" disabled={disabled} aria-label={t("schemaForm.image.removeNamed", { name: label })} onClick={() => onChange(undefined)}>
              <Trash2 className="h-4 w-4" aria-hidden />
            </Button>
          )}
        </div>
        {problem && (
          <p role="alert" className="text-xs text-destructive">
            {problem}
          </p>
        )}
      </div>
    </div>
  );
}

/** Radio cards over the field's `enum`; `x-options` gives the labels. */
export function LayoutWidget({ field, id, disabled, describedBy, label, value, onChange }: BaseProps & { field: FieldSchema; value: unknown; onChange: (v: string) => void }) {
  const options = (field.enum ?? []).map((e) => String(e));
  return (
    <div id={id} role="radiogroup" aria-label={label} aria-describedby={describedBy} className="grid grid-cols-2 gap-2 sm:grid-cols-3">
      {options.map((o) => {
        const on = value === o;
        return (
          <button
            key={o}
            type="button"
            role="radio"
            aria-checked={on}
            disabled={disabled}
            onClick={() => onChange(o)}
            className={cn(
              "rounded-lg border px-3 py-2 text-left text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50",
              on ? "border-primary bg-primary/5 font-medium" : "border-border hover:bg-muted",
            )}
          >
            {field["x-options"]?.[o] ?? o}
          </button>
        );
      })}
    </div>
  );
}

function move<T>(list: T[], from: number, to: number): T[] {
  if (to < 0 || to >= list.length) return list;
  const next = list.slice();
  const [item] = next.splice(from, 1);
  next.splice(to, 0, item as T);
  return next;
}

/**
 * An ordered list of ids: rows with ↑/↓ (keyboard-first, no drag needed).
 * With `options` the user also picks what is in the list (field-picker, widget-picker);
 * without, the list only reorders what it was given (order-list).
 */
export function OrderedWidget({
  id,
  disabled,
  describedBy,
  label,
  value,
  options,
  onChange,
  labelOf,
}: BaseProps & { value: unknown; options?: PickerOption[]; onChange: (v: string[]) => void; labelOf?: (v: string) => string }) {
  const { t } = useTranslation("integrations");
  const items = Array.isArray(value) ? (value as unknown[]).map(String) : [];
  const name = (v: string) => options?.find((o) => o.value === v)?.label ?? labelOf?.(v) ?? v;
  const rest = (options ?? []).filter((o) => !items.includes(o.value));
  return (
    <div id={id} aria-describedby={describedBy} className="space-y-2">
      {items.length === 0 && <p className="text-sm text-muted-foreground">{t("schemaForm.order.empty")}</p>}
      <ol className="space-y-1.5">
        {items.map((v, i) => (
          <li key={v} className="flex items-center gap-2 rounded-lg border border-border px-2 py-1.5">
            <span className="min-w-0 flex-1 truncate px-1 text-sm">{name(v)}</span>
            <Button type="button" variant="ghost" size="sm" disabled={disabled || i === 0} aria-label={t("schemaForm.order.up", { name: name(v) })} onClick={() => onChange(move(items, i, i - 1))}>
              <ArrowUp className="h-4 w-4" aria-hidden />
            </Button>
            <Button type="button" variant="ghost" size="sm" disabled={disabled || i === items.length - 1} aria-label={t("schemaForm.order.down", { name: name(v) })} onClick={() => onChange(move(items, i, i + 1))}>
              <ArrowDown className="h-4 w-4" aria-hidden />
            </Button>
            {options && (
              <Button type="button" variant="ghost" size="sm" disabled={disabled} aria-label={t("schemaForm.order.remove", { name: name(v) })} onClick={() => onChange(items.filter((x) => x !== v))}>
                <Trash2 className="h-4 w-4" aria-hidden />
              </Button>
            )}
          </li>
        ))}
      </ol>
      {options && rest.length > 0 && (
        <Select
          aria-label={t("schemaForm.order.addNamed", { name: label })}
          disabled={disabled}
          value=""
          onChange={(e) => e.target.value && onChange([...items, e.target.value])}
        >
          <option value="">{t("schemaForm.order.add")}</option>
          {rest.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </Select>
      )}
    </div>
  );
}
