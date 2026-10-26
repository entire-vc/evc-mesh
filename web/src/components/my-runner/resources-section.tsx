import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { toast } from "@/components/ui/toast";
import { SectionShell } from "@/components/my-runner/section-shell";
import type { Loaded } from "@/components/my-runner/use-load";
import { apiErrorMessage } from "@/lib/api-error";
import {
  CATALOG_SECTIONS, CONFIG_SECTIONS, entrySummary, findSecretViolation, parseEntryValue, saveResources, sectionEntries, withEntry,
  type ConfigSection, type ResourceDoc, type RuntimeConfig,
} from "@/lib/my-runner";

/** A filter appears once the lists are too long to scan (the fleet catalog has 60+ bindings). */
const FILTER_FROM = 20;

type Editor = { section: ConfigSection; original: string | null };

function EntryEditor({ config, editor, onApply, onCancel }: {
  config: RuntimeConfig; editor: Editor; onApply: (name: string, value: unknown) => void; onCancel: () => void;
}) {
  const { t } = useTranslation("runner");
  const existing = editor.original === null ? undefined : config[editor.section]?.[editor.original];
  const [name, setName] = useState(editor.original ?? "");
  const [text, setText] = useState(existing === undefined ? "{}" : JSON.stringify(existing, null, 2));
  const [error, setError] = useState<string | null>(null);

  const apply = () => {
    const n = name.trim();
    if (!n) return setError(t("resources.nameRequired"));
    if (n !== editor.original && config[editor.section]?.[n] !== undefined) return setError(t("resources.nameTaken"));
    const parsed = parseEntryValue(text);
    if (!parsed.ok) return setError(t("resources.valueInvalid"));
    const secret = findSecretViolation(parsed.value);
    if (secret) return setError(t("resources.secretRejected", { path: secret }));
    onApply(n, parsed.value);
  };

  return (
    <div className="space-y-3 rounded-lg border border-primary/40 p-3" data-testid="my-runner-entry-editor">
      <label className="block space-y-1 text-sm">
        <span className="text-muted-foreground">{t("resources.name")}</span>
        <Input value={name} onChange={(e) => setName(e.target.value)} className="h-9" />
      </label>
      <label className="block space-y-1 text-sm">
        <span className="text-muted-foreground">{t("resources.value")}</span>
        <Textarea value={text} onChange={(e) => setText(e.target.value)} rows={8} spellCheck={false} className="font-mono text-xs" />
      </label>
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
      <div className="flex flex-wrap gap-2">
        <Button size="sm" onClick={apply}>{t("resources.apply")}</Button>
        <Button size="sm" variant="outline" onClick={onCancel}>{t("resources.cancel")}</Button>
      </div>
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
  const [query, setQuery] = useState("");
  const [draft, setDraft] = useState<RuntimeConfig>(doc.config ?? {});
  const [dirty, setDirty] = useState(false);
  const [editor, setEditor] = useState<Editor | null>(null);
  const [saving, setSaving] = useState(false);

  // A newer server revision replaces the draft (after a save or a conflict reload).
  useEffect(() => {
    setDraft(doc.config ?? {});
    setDirty(false);
    setEditor(null);
  }, [doc.revision, doc.config]);

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
      toast.error(conflict ? t("resources.conflict") : apiErrorMessage(e, t("resources.saveFailed")));
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
        {doc.source === "workspace" ? t("resources.catalogHint", { n: doc.revision }) : t("resources.hint")}
      </p>
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
        const entries = all.filter(([name, value]) => matches(name, value, q));
        return (
          <div key={section} className="space-y-2" data-testid={`my-runner-resources-${section}`}>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h3 className="text-sm font-medium">{t(`resources.sections.${section}`)}</h3>
              <Button size="sm" variant="outline" onClick={() => setEditor({ section, original: null })}>{t("resources.add")}</Button>
            </div>
            {entries.length === 0 && !(editor?.section === section && editor.original === null) && (
              <p className="text-sm text-muted-foreground">{t(all.length > 0 ? "resources.noMatches" : "resources.empty")}</p>
            )}
            {/* The editor opens where the click was: under Add for a new entry, under the row being edited. */}
            {editor?.section === section && editor.original === null && (
              <EntryEditor key="new" config={draft} editor={editor} onApply={apply} onCancel={() => setEditor(null)} />
            )}
            <ul className="space-y-2">
              {entries.map(([name, value]) => (
                <li key={name} className="space-y-2">
                  <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-border px-3 py-2">
                    <span className="min-w-0 flex-1">
                      <span className="block break-all text-sm font-medium">{name}</span>
                      <EntrySummary value={value} />
                    </span>
                    <span className="flex shrink-0 gap-2">
                      <Button size="sm" variant="ghost" onClick={() => setEditor({ section, original: name })}>{t("resources.edit")}</Button>
                      <Button size="sm" variant="ghost" onClick={() => remove(section, name)}>{t("resources.remove")}</Button>
                    </span>
                  </div>
                  {editor?.section === section && editor.original === name && (
                    <EntryEditor key={name} config={draft} editor={editor} onApply={apply} onCancel={() => setEditor(null)} />
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
