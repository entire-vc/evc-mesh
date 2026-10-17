import { useId, useMemo, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { Plus, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/cn";
import { apiErrorMessage } from "@/lib/api-error";
import { useProjectStore } from "@/stores/project";
import { useAgentStore } from "@/stores/agent";
import { resolveSchema } from "./resolve";
import { buildPayload, initialState, setAtPath } from "./state";
import { validate, type Errors } from "./validate";
import { isVisible } from "./visibility";
import type { FieldError, FieldSchema, Values, Widget } from "./types";

export interface PickerOption {
  value: string;
  label: string;
}

export interface SchemaFormProps {
  /** Settings schema of one level (may hold `$ref` into `manifest`). */
  schema: FieldSchema;
  /** Root the `$ref`s point into: the manifest. */
  manifest?: unknown;
  /** Value from the server; a stored secret arrives as a mask, never as text. */
  value?: Values;
  /** Receives the payload; throw an ApiRequestError to show its `details` per field.
   *  Resolve with `false` when the payload was NOT stored and the caller handled it (e.g. sent the
   *  user elsewhere): the typed secrets then stay as a draft instead of being marked as set. */
  onSubmit: (settings: Values) => Promise<void | false>;
  submitLabel?: string;
  /** Read-only display: no submit button (the caller offers its own way forward). */
  hideSubmit?: boolean;
  disabled?: boolean;
  /** Override the options of the project/agent pickers (defaults: current stores). */
  projectOptions?: PickerOption[];
  agentOptions?: PickerOption[];
}

function widgetOf(f: FieldSchema): Widget | "text" {
  if (f["x-widget"]) return f["x-widget"];
  if (f.type === "boolean") return "toggle";
  if (f.enum) return "select";
  if (f.type === "array") return "list";
  if (f.type === "object" && f.properties) return "group";
  return "text";
}

interface Ctx {
  errors: Errors;
  serverErrors: Record<string, string>;
  secretsSet: Set<string>;
  editingSecrets: Set<string>;
  setEditing: (path: string, on: boolean) => void;
  disabled: boolean;
  projectOptions: PickerOption[];
  agentOptions: PickerOption[];
  onChange: (path: string[], v: unknown) => void;
  idBase: string;
  /** Manifest id: scopes the per-locale field labels (`schemaFields.<id>.<path>`). */
  extId?: string;
}

function FieldMessage({ id, error, server }: { id: string; error?: FieldError; server?: string }) {
  const { t } = useTranslation("integrations");
  const text = server ?? (error ? t(`schemaForm.errors.${error.code}`, error.params) : null);
  if (!text) return null;
  return (
    <p id={id} role="alert" className="text-xs text-destructive">
      {text}
    </p>
  );
}

function Fields({ schema, values, prefix, ctx }: { schema: FieldSchema; values: Values; prefix: string[]; ctx: Ctx }) {
  const required = new Set(schema.required ?? []);
  return (
    <>
      {Object.entries(schema.properties ?? {}).map(([name, f]) =>
        isVisible(f, values) ? (
          <Field key={name} name={name} field={f} value={values[name]} siblings={values} prefix={prefix} required={required.has(name)} ctx={ctx} />
        ) : null,
      )}
    </>
  );
}

function Field({
  name,
  field,
  value,
  prefix,
  required,
  ctx,
}: {
  name: string;
  field: FieldSchema;
  value: unknown;
  siblings: Values;
  prefix: string[];
  required: boolean;
  ctx: Ctx;
}) {
  const { t } = useTranslation("integrations");
  const path = [...prefix, name];
  const key = path.join(".");
  const id = `${ctx.idBase}-${key}`;
  const widget = widgetOf(field);
  // the manifest title is one language; a locale entry (when present) wins so the form follows the UI language
  const label = ctx.extId
    ? t(`schemaFields.${ctx.extId}.${path.join("_")}`, { defaultValue: field.title ?? name })
    : (field.title ?? name);
  const error = ctx.errors[key];
  const server = ctx.serverErrors[key];
  const invalid = Boolean(error || server);
  const helpId = `${id}-help`;
  const errId = `${id}-err`;
  const describedBy = [field["x-help"] || field.description ? helpId : null, invalid ? errId : null].filter(Boolean).join(" ") || undefined;
  const help = field["x-help"] ?? field.description;

  if (widget === "group") {
    const nested = (value && typeof value === "object" ? value : {}) as Values;
    return (
      <fieldset className="space-y-3 rounded-lg border border-border p-3">
        <legend className="px-1 text-sm font-medium">{label}</legend>
        {help && <p className="text-xs text-muted-foreground">{help}</p>}
        <Fields schema={field} values={nested} prefix={path} ctx={ctx} />
      </fieldset>
    );
  }

  const common = { id, disabled: ctx.disabled, "aria-invalid": invalid || undefined, "aria-describedby": describedBy } as const;

  let control: React.ReactNode;
  if (field["x-secret"]) {
    const isSet = ctx.secretsSet.has(key);
    const editing = !isSet || ctx.editingSecrets.has(key);
    control = editing ? (
      <div className="flex gap-2">
        <Input
          {...common}
          type="password"
          autoComplete="new-password"
          value={typeof value === "string" ? value : ""}
          onChange={(e) => ctx.onChange(path, e.target.value)}
        />
        {isSet && (
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => {
              ctx.onChange(path, "");
              ctx.setEditing(key, false);
            }}
          >
            {t("schemaForm.secret.cancel")}
          </Button>
        )}
      </div>
    ) : (
      <div className="flex items-center gap-3">
        <span id={id} className="text-sm text-muted-foreground">
          {t("schemaForm.secret.set")}
        </span>
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={ctx.disabled}
          aria-label={t("schemaForm.secret.changeNamed", { name: label })}
          onClick={() => ctx.setEditing(key, true)}
        >
          {t("schemaForm.secret.change")}
        </Button>
      </div>
    );
  } else if (widget === "toggle") {
    control = (
      <button
        {...common}
        type="button"
        role="switch"
        aria-checked={value === true}
        onClick={() => ctx.onChange(path, value !== true)}
        className={cn(
          "relative inline-flex h-6 w-11 shrink-0 items-center rounded-full border border-transparent transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50",
          value === true ? "bg-primary" : "bg-muted",
        )}
      >
        <span className={cn("inline-block h-5 w-5 rounded-full bg-background shadow transition-transform", value === true ? "translate-x-5" : "translate-x-0.5")} />
      </button>
    );
  } else if (widget === "select" || widget === "project-picker" || widget === "agent-picker") {
    const options: PickerOption[] =
      widget === "project-picker"
        ? ctx.projectOptions
        : widget === "agent-picker"
          ? ctx.agentOptions
          : (field.enum ?? []).map((e) => ({ value: String(e), label: String(e) }));
    control = (
      <Select {...common} value={value == null ? "" : String(value)} onChange={(e) => ctx.onChange(path, e.target.value === "" ? undefined : e.target.value)}>
        <option value="">{t("schemaForm.selectPlaceholder")}</option>
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </Select>
    );
  } else if (widget === "textarea") {
    control = <Textarea {...common} value={typeof value === "string" ? value : ""} onChange={(e) => ctx.onChange(path, e.target.value)} rows={4} />;
  } else if (widget === "list") {
    const items = Array.isArray(value) ? (value as string[]) : [];
    control = (
      <div className="space-y-2" id={id} aria-describedby={describedBy}>
        {items.map((item, i) => (
          <div key={i} className="flex gap-2">
            <Input
              aria-label={t("schemaForm.list.itemNamed", { name: label, n: i + 1 })}
              disabled={ctx.disabled}
              value={item}
              onChange={(e) => ctx.onChange(path, items.map((x, j) => (j === i ? e.target.value : x)))}
            />
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={ctx.disabled}
              aria-label={t("schemaForm.list.removeNamed", { name: label, n: i + 1 })}
              onClick={() => ctx.onChange(path, items.filter((_, j) => j !== i))}
            >
              <X className="h-4 w-4" aria-hidden />
            </Button>
          </div>
        ))}
        <Button type="button" variant="outline" size="sm" disabled={ctx.disabled} onClick={() => ctx.onChange(path, [...items, ""])}>
          <Plus className="mr-1 h-4 w-4" aria-hidden />
          {t("schemaForm.list.add")}
        </Button>
      </div>
    );
  } else {
    const numeric = field.type === "number" || field.type === "integer";
    control = (
      <Input
        {...common}
        type={numeric ? "number" : field.format === "uri" ? "url" : field.format === "email" ? "email" : "text"}
        value={value == null ? "" : String(value)}
        onChange={(e) => {
          const raw = e.target.value;
          ctx.onChange(path, numeric ? (raw === "" ? undefined : Number(raw)) : raw);
        }}
      />
    );
  }

  const inline = widget === "toggle" && !field["x-secret"];
  return (
    <div className={cn("space-y-1.5", inline && "flex items-center justify-between gap-3 space-y-0")}>
      <div className={cn(inline && "space-y-0.5")}>
        <label htmlFor={id} className="text-sm font-medium">
          {label}
          {required && (
            <span className="ml-0.5 text-destructive" aria-hidden>
              *
            </span>
          )}
        </label>
        {help && (
          <p id={helpId} className="text-xs text-muted-foreground">
            {help}
          </p>
        )}
      </div>
      {control}
      <FieldMessage id={errId} error={error} server={server} />
    </div>
  );
}

export function SchemaForm({ schema, manifest, value, onSubmit, submitLabel, hideSubmit = false, disabled = false, projectOptions, agentOptions }: SchemaFormProps) {
  const { t } = useTranslation("integrations");
  const idBase = useId();
  const resolved = useMemo(() => resolveSchema(schema, manifest ?? {}), [schema, manifest]);
  const init = useMemo(() => initialState(resolved, value), [resolved, value]);
  const [values, setValues] = useState<Values>(init.values);
  const [secretsSet, setSecretsSet] = useState<Set<string>>(init.secretsSet);
  const [editingSecrets, setEditingSecrets] = useState<Set<string>>(new Set());
  const [errors, setErrors] = useState<Errors>({});
  const [serverErrors, setServerErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);

  const projects = useProjectStore((s) => s.projects);
  const agents = useAgentStore((s) => s.agents);
  const pOpts = projectOptions ?? projects.map((p) => ({ value: p.id, label: p.name }));
  const aOpts = agentOptions ?? agents.map((a) => ({ value: a.id, label: a.name }));

  const ctx: Ctx = {
    errors,
    serverErrors,
    secretsSet,
    editingSecrets,
    setEditing: (path, on) =>
      setEditingSecrets((prev) => {
        const next = new Set(prev);
        if (on) next.add(path);
        else next.delete(path);
        return next;
      }),
    disabled: disabled || saving,
    projectOptions: pOpts,
    agentOptions: aOpts,
    onChange: (path, v) => {
      setValues((prev) => setAtPath(prev, path, v));
      const key = path.join(".");
      setErrors(({ [key]: _drop, ...rest }) => (void _drop, rest));
      setServerErrors(({ [key]: _drop, ...rest }) => (void _drop, rest));
      setSaved(false);
    },
    idBase,
    extId: typeof (manifest as { id?: unknown } | undefined)?.id === "string" ? (manifest as { id: string }).id : undefined,
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setFormError(null);
    setSaved(false);
    const found = validate(resolved, values, { secretsSet });
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setSaving(true);
    try {
      const payload = buildPayload(resolved, values);
      if ((await onSubmit(payload)) === false) return;
      // typed secrets are stored now: back to "set", draft wiped from the form
      const nextSet = new Set(secretsSet);
      let next = values;
      const walk = (s: FieldSchema, prefix: string[]) => {
        for (const [name, f] of Object.entries(s.properties ?? {})) {
          const p = [...prefix, name];
          if (f.type === "object" && f.properties) walk(f, p);
          else if (f["x-secret"]) {
            // only what was actually sent is stored; a draft hidden by x-visible-if stays a draft
            let sent: unknown = payload;
            for (const k of p) sent = (sent as Values | undefined)?.[k];
            if (typeof sent === "string" && sent !== "") {
              nextSet.add(p.join("."));
              next = setAtPath(next, p, undefined);
            }
          }
        }
      };
      walk(resolved, []);
      setValues(next);
      setSecretsSet(nextSet);
      setEditingSecrets(new Set());
      setSaved(true);
    } catch (err) {
      const details = (err as { details?: Record<string, string> } | null)?.details;
      const mapped: Record<string, string> = {};
      if (details && typeof details === "object") {
        for (const [k, msg] of Object.entries(details)) {
          if (typeof msg === "string") mapped[k.replace(/^settings\./, "")] = msg;
        }
      }
      setServerErrors(mapped);
      if (Object.keys(mapped).length === 0) setFormError(apiErrorMessage(err, t("schemaForm.saveFailed")));
    } finally {
      setSaving(false);
    }
  };

  return (
    <form onSubmit={submit} noValidate className="space-y-4">
      <Fields schema={resolved} values={values} prefix={[]} ctx={ctx} />
      {formError && (
        <p role="alert" className="text-sm text-destructive">
          {formError}
        </p>
      )}
      <div className="flex items-center gap-3">
        {!hideSubmit && (
          <Button type="submit" disabled={disabled || saving}>
            {submitLabel ?? t("schemaForm.save")}
          </Button>
        )}
        {saved && (
          <span role="status" className="text-sm text-muted-foreground">
            {t("schemaForm.saved")}
          </span>
        )}
      </div>
    </form>
  );
}
