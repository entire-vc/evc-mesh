import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { toast } from "@/components/ui/toast";
import { SchemaForm, type FieldSchema, type Values } from "@/components/extensions/schema-form";
import { SectionShell } from "@/components/my-runner/section-shell";
import type { Loaded } from "@/components/my-runner/use-load";
import { apiErrorMessage } from "@/lib/api-error";
import {
  CATALOG_SECTIONS, CONFIG_SECTIONS, bindRefs, entrySchema, entrySummary, fieldErrorsByEntry, findSecretViolation, fromFormValue,
  refOptions, saveResources, sectionEntries, toFormValue, isSchemaPath, unknownFields, withEntry,
  type ConfigSection, type ResourceDoc, type RuntimeConfig,
} from "@/lib/my-runner";

/** A filter appears once the lists are too long to scan (the fleet catalog has 60+ bindings). */
const FILTER_FROM = 20;

/** `serverErrors`: the server's reasons for this entry from the last save, by field path. */
type Editor = { section: ConfigSection; original: string | null; serverErrors?: Record<string, string> };

/**
 * One entry, edited only through the form the Runner manifest describes:
 * references are picked from what exists in the draft, never typed.
 */
function EntryEditor({ config, schema, editor, onApply, onCancel }: {
  config: RuntimeConfig; schema: FieldSchema; editor: Editor; onApply: (name: string, value: unknown) => void; onCancel: () => void;
}) {
  const { t } = useTranslation("runner");
  const isNew = editor.original === null;
  const existing = isNew ? undefined : config[editor.section]?.[editor.original!];
  const start = useMemo(() => toFormValue(editor.section, existing, isNew), [editor.section, existing, isNew]);
  const [name, setName] = useState(editor.original ?? "");
  const [nameError, setNameError] = useState<string | null>(null);
  const [values, setValues] = useState<Values>(start);
  // options follow the draft: allowed profiles narrow "first choice" as they are ticked
  const bound = useMemo(() => bindRefs(schema, refOptions(config, editor.section, values)), [schema, config, editor.section, values]);
  const dropped = useMemo(() => unknownFields(schema, existing), [schema, existing]);
  // Reasons for fields the form has go to the field; the rest (an unknown field) are listed above the form.
  const [serverErrors, orphanErrors] = useMemo(() => {
    const atField: Record<string, string> = {};
    const elsewhere: [string, string][] = [];
    for (const [path, code] of Object.entries(editor.serverErrors ?? {})) {
      if (!path) continue;
      const reason = t(`resources.fieldErrors.${code}`, { defaultValue: t("resources.fieldErrors.invalid") });
      if (isSchemaPath(schema, path)) atField[path] = reason;
      else elsewhere.push([path, reason]);
    }
    return [atField, elsewhere] as const;
  }, [editor.serverErrors, schema, t]);
  const entryError = editor.serverErrors?.[""];

  const submit = async (payload: Values) => {
    const n = name.trim();
    const taken = n !== editor.original && config[editor.section]?.[n] !== undefined;
    const problem = !n ? t("resources.nameRequired") : taken ? t("resources.nameTaken") : null;
    setNameError(problem);
    if (problem) return false;
    const value = fromFormValue(editor.section, payload);
    if (editor.section === "accounts" && value.credential_ref === "" && existing) {
      const kept = (existing as Values).credential_ref;
      // an entry added in this draft still holds its typed reference: keep it
      if (typeof kept === "string" && kept !== "") value.credential_ref = kept;
      // the server keeps a hidden reference only under the same name
      else if (n !== editor.original) throw Object.assign(new Error("credential"), { details: { credential_ref: t("resources.credentialOnRename") } });
    }
    const secret = findSecretViolation(value);
    if (secret) {
      setNameError(t("resources.secretRejected", { path: secret }));
      return false;
    }
    onApply(n, value);
  };

  return (
    <div className="space-y-4 rounded-lg border border-primary/40 p-3 sm:p-4" data-testid="my-runner-entry-editor">
      <label className="block space-y-1.5 text-sm">
        <span className="font-medium">{t("resources.name")}</span>
        <Input
          value={name}
          onChange={(e) => { setName(e.target.value); setNameError(null); }}
          className="h-9"
          aria-invalid={nameError ? true : undefined}
        />
        {nameError && <span role="alert" className="block text-xs text-destructive">{nameError}</span>}
      </label>
      {dropped.length > 0 && (
        <p role="alert" className="rounded-md border border-warning/40 bg-warning/10 px-3 py-2 text-sm">
          {t("resources.unknownFields", { fields: dropped.join(", ") })}
        </p>
      )}
      {orphanErrors.length > 0 && (
        <ul role="alert" className="space-y-0.5 text-sm text-destructive" data-testid="my-runner-entry-server-errors">
          {orphanErrors.map(([path, reason]) => <li key={path}><code className="font-mono text-xs">{path}</code>: {reason}</li>)}
        </ul>
      )}
      {entryError && <p role="alert" className="text-sm text-destructive">{t(`resources.fieldErrors.${entryError}`, { defaultValue: t("resources.fieldErrors.invalid") })}</p>}
      <SchemaForm
        schema={bound}
        manifest={{}}
        value={start}
        onSubmit={submit}
        onValuesChange={setValues}
        submitLabel={t("resources.apply")}
        serverErrors={serverErrors}
        actions={<Button type="button" variant="outline" onClick={onCancel}>{t("resources.cancel")}</Button>}
      />
    </div>
  );
}

function EntrySummary({ value }: { value: unknown }) {
  const { t } = useTranslation("runner");
  const s = entrySummary(value);
  const parts = [...s.text, ...(s.max === null ? [] : [t("resources.summary.max", { n: s.max })])];
  if (parts.length === 0 && !s.disabled) return null;
  return (
    <span className="block min-w-0 truncate text-xs text-muted-foreground">
      {s.disabled && <span className="mr-1 font-medium text-warning">{t("resources.summary.disabled")}</span>}
      {parts.join(" · ")}
    </span>
  );
}

function matches(name: string, value: unknown, query: string): boolean {
  if (!query) return true;
  const s = entrySummary(value);
  return [name, ...s.text].some((x) => x.toLowerCase().includes(query));
}

function ResourcesBody({ doc, onSaved }: { doc: ResourceDoc; onSaved: () => void }) {
  const { t } = useTranslation("runner");
  const sections = doc.source === "workspace" ? CATALOG_SECTIONS : CONFIG_SECTIONS;
  const manifest = doc.source === "workspace" ? doc.manifest : null;
  // The personal document has no schema the runner reads: it is shown, not edited.
  const editable = doc.source === "workspace" && manifest !== null;
  const [query, setQuery] = useState("");
  const [draft, setDraft] = useState<RuntimeConfig>(doc.config ?? {});
  const [dirty, setDirty] = useState(false);
  const [editor, setEditor] = useState<Editor | null>(null);
  const [saving, setSaving] = useState(false);

  // A newer server revision replaces the draft (after a save or a conflict reload). Done while
  // rendering, not in an effect: a mount effect could land after the first click and wipe the edit
  // it made (my-runner tests lost "Remove" and the "Add" editor on a loaded CI runner).
  const [shown, setShown] = useState(doc);
  if (shown.revision !== doc.revision || shown.config !== doc.config) {
    setShown(doc);
    setDraft(doc.config ?? {});
    setDirty(false);
    setEditor(null);
  }

  const apply = (name: string, value: unknown) => {
    if (!editor) return;
    let next = draft;
    if (editor.original !== null && editor.original !== name) next = withEntry(next, editor.section, editor.original, undefined);
    setDraft(withEntry(next, editor.section, name, value));
    setDirty(true);
    setEditor(null);
  };

  const remove = (section: ConfigSection, name: string) => {
    setDraft(withEntry(draft, section, name, undefined));
    setDirty(true);
  };

  const save = async () => {
    // The whole draft goes to the server, not just the entry that was edited last.
    const secret = findSecretViolation(draft);
    if (secret) return void toast.error(t("resources.secretRejected", { path: secret }));
    setSaving(true);
    try {
      await saveResources(doc, draft);
      toast.success(t("resources.saved"));
      onSaved();
    } catch (e) {
      const conflict = (e as { status?: number }).status === 409;
      // Reasons per field: reopen the first entry the server refused, each reason at its field.
      const refused = conflict ? [] : fieldErrorsByEntry((e as { details?: Record<string, string> }).details, draft);
      if (refused.length > 0) {
        const first = refused[0]!;
        setEditor({ section: first.section, original: first.name, serverErrors: first.fields });
        toast.error(t("resources.refused", { count: refused.length }));
      } else {
        toast.error(conflict ? t("resources.conflict") : apiErrorMessage(e, t("resources.saveFailed")));
      }
      if (conflict) onSaved();
    } finally {
      setSaving(false);
    }
  };

  const total = useMemo(() => sections.reduce((n, s) => n + sectionEntries(draft, s).length, 0), [draft, sections]);
  const q = total > FILTER_FROM ? query.trim().toLowerCase() : "";

  return (
    <div className="space-y-5">
      <p className="text-sm text-muted-foreground">
        {doc.source === "workspace" ? t("resources.catalogHint", { n: doc.revision }) : t("resources.personalHint")}
      </p>
      {doc.source === "workspace" && !manifest && (
        <p role="alert" className="text-sm text-destructive" data-testid="my-runner-resources-no-schema">{t("resources.noSchema")}</p>
      )}
      {total > FILTER_FROM && (
        <Input
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={t("resources.filter")}
          aria-label={t("resources.filter")}
          className="h-9 sm:max-w-xs"
        />
      )}
      {sections.map((section) => {
        const all = sectionEntries(draft, section);
        const schema = editable ? entrySchema(manifest, section) : null;
        // The entry open in the editor stays listed whatever the filter: a refused save reopens it.
        const entries = all.filter(([name, value]) => (editor?.section === section && editor.original === name) || matches(name, value, q));
        return (
          <div key={section} className="space-y-2" data-testid={`my-runner-resources-${section}`}>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h3 className="text-sm font-medium">{t(`resources.sections.${section}`)}</h3>
              {schema && <Button size="sm" variant="outline" onClick={() => setEditor({ section, original: null })}>{t("resources.add")}</Button>}
            </div>
            {entries.length === 0 && !(editor?.section === section && editor.original === null) && (
              <p className="text-sm text-muted-foreground">{t(all.length > 0 ? "resources.noMatches" : "resources.empty")}</p>
            )}
            {/* The editor opens where the click was: under Add for a new entry, under the row being edited. */}
            {schema && editor?.section === section && editor.original === null && (
              <EntryEditor key="new" config={draft} schema={schema} editor={editor} onApply={apply} onCancel={() => setEditor(null)} />
            )}
            <ul className="space-y-2">
              {entries.map(([name, value]) => (
                <li key={name} className="space-y-2">
                  <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-border px-3 py-2">
                    <span className="min-w-0 flex-1">
                      <span className="block break-all text-sm font-medium">{name}</span>
                      <EntrySummary value={value} />
                    </span>
                    {schema && (
                      <span className="flex shrink-0 gap-2">
                        <Button size="sm" variant="ghost" onClick={() => setEditor({ section, original: name })}>{t("resources.edit")}</Button>
                        <Button size="sm" variant="ghost" onClick={() => remove(section, name)}>{t("resources.remove")}</Button>
                      </span>
                    )}
                  </div>
                  {schema && editor?.section === section && editor.original === name && (
                    <EntryEditor
                      key={`${name}:${JSON.stringify(editor.serverErrors ?? {})}`}
                      config={draft}
                      schema={schema}
                      editor={editor}
                      onApply={apply}
                      onCancel={() => setEditor(null)}
                    />
                  )}
                </li>
              ))}
            </ul>
          </div>
        );
      })}
      {dirty && (
        // Sticky: after an edit deep in a long list, Save stays in reach instead of 60 rows below.
        <div className="sticky bottom-0 z-10 -mx-1 flex flex-wrap items-center gap-3 border-t border-border bg-card px-1 py-3" data-testid="my-runner-resources-dirty">
          <span className="text-sm text-muted-foreground">{t("resources.unsaved")}</span>
          <Button size="sm" disabled={saving} onClick={() => void save()}>{t(saving ? "resources.saving" : "resources.save")}</Button>
          <Button size="sm" variant="outline" disabled={saving} onClick={() => { setDraft(doc.config ?? {}); setDirty(false); setEditor(null); }}>
            {t("resources.discard")}
          </Button>
        </div>
      )}
    </div>
  );
}

export function ResourcesSection({ result, reload }: { result: Loaded<ResourceDoc>; reload: () => void }) {
  const { t } = useTranslation("runner");
  return (
    <SectionShell title={t("resources.title")} testId="my-runner-resources" result={result} onRetry={reload}>
      {(doc) => <ResourcesBody doc={doc} onSaved={reload} />}
    </SectionShell>
  );
}
