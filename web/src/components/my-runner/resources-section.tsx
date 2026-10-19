import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { toast } from "@/components/ui/toast";
import { SectionShell } from "@/components/my-runner/section-shell";
import type { Loaded } from "@/components/my-runner/use-load";
import { apiErrorMessage } from "@/lib/api-error";
import {
  CONFIG_SECTIONS, findSecretViolation, parseEntryValue, saveMyRuntime, sectionEntries, withEntry,
  type ConfigSection, type RuntimeConfig, type UserRuntime,
} from "@/lib/my-runner";

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

function ResourcesBody({ doc, onSaved }: { doc: UserRuntime; onSaved: () => void }) {
  const { t } = useTranslation("runner");
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
      await saveMyRuntime(doc.revision, draft);
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

  return (
    <div className="space-y-5">
      <p className="text-sm text-muted-foreground">{t("resources.hint")}</p>
      {CONFIG_SECTIONS.map((section) => {
        const entries = sectionEntries(draft, section);
        return (
          <div key={section} className="space-y-2" data-testid={`my-runner-resources-${section}`}>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h3 className="text-sm font-medium">{t(`resources.sections.${section}`)}</h3>
              <Button size="sm" variant="outline" onClick={() => setEditor({ section, original: null })}>{t("resources.add")}</Button>
            </div>
            {entries.length === 0 && !(editor?.section === section && editor.original === null) && (
              <p className="text-sm text-muted-foreground">{t("resources.empty")}</p>
            )}
            <ul className="space-y-2">
              {entries.map(([name]) => (
                <li key={name} className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-border px-3 py-2">
                  <span className="min-w-0 truncate text-sm font-medium">{name}</span>
                  <span className="flex gap-2">
                    <Button size="sm" variant="ghost" onClick={() => setEditor({ section, original: name })}>{t("resources.edit")}</Button>
                    <Button size="sm" variant="ghost" onClick={() => remove(section, name)}>{t("resources.remove")}</Button>
                  </span>
                </li>
              ))}
            </ul>
            {editor?.section === section && (
              <EntryEditor key={editor.original ?? "new"} config={draft} editor={editor} onApply={apply} onCancel={() => setEditor(null)} />
            )}
          </div>
        );
      })}
      {dirty && (
        <div className="flex flex-wrap items-center gap-3 border-t border-border pt-3" data-testid="my-runner-resources-dirty">
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

export function ResourcesSection({ result, reload }: { result: Loaded<UserRuntime>; reload: () => void }) {
  const { t } = useTranslation("runner");
  return (
    <SectionShell title={t("resources.title")} testId="my-runner-resources" result={result} onRetry={reload}>
      {(doc) => <ResourcesBody doc={doc} onSaved={reload} />}
    </SectionShell>
  );
}
