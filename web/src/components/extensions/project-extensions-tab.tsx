import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { AlertTriangle, CheckCircle2, Plus, RefreshCw, Unlink } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { toast } from "@/components/ui/toast";
import { SchemaForm, type FieldSchema } from "@/components/extensions/schema-form";
import { useExtensionCatalog, type CatalogEntry } from "@/hooks/useExtensionCatalog";
import {
  checkProjectExtension, deleteProjectExtension, listProjectActions, listProjectBindings,
  runProjectAction, saveProjectExtension, searchProjectAction, type ActionSearchDoc, type ProjectAction, type ProjectBinding,
} from "@/lib/extension-settings";
import { apiErrorMessage } from "@/lib/api-error";
import * as i18nFmt from "@/i18n/format";
import { ChatSettingsCard } from "@/components/extensions/chat-settings-card";
import { useParams, useSearchParams } from "react-router";

// Chat with Agent has its own settings endpoint and card; the generic binding
// card would show a second, conflicting copy of it.
const CHAT_EXTENSION_ID = "agent-chat";

/** Actions with their own button; `check` has the dedicated «Проверить». */
const EXTRA_ACTION_METHOD = "POST";

function SearchAction({ projectId, id }: { projectId: string; id: string }) {
  const { t } = useTranslation("integrations");
  const [q, setQ] = useState("");
  const [docs, setDocs] = useState<ActionSearchDoc[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(false);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setErr(false);
    try { setDocs(await searchProjectAction(projectId, id, q.trim())); } catch { setErr(true); setDocs(null); } finally { setBusy(false); }
  };
  return (
    <div className="space-y-2" data-testid={`search-${id}`}>
      <form onSubmit={submit} className="flex gap-2">
        <Input value={q} onChange={(e) => setQ(e.target.value)} aria-label={t("projectTab.search.label")} placeholder={t("projectTab.search.placeholder")} />
        <Button type="submit" variant="outline" size="sm" disabled={busy}>{t("projectTab.search.button")}</Button>
      </form>
      {err && <p role="alert" className="text-sm text-destructive">{t("projectTab.search.failed")}</p>}
      {docs && docs.length === 0 && <p className="text-sm text-muted-foreground">{t("projectTab.search.empty")}</p>}
      {docs && docs.length > 0 && (
        <ul className="space-y-1 text-sm">
          {docs.map((d) => <li key={d.id || d.path}><span className="font-medium">{d.title}</span> <span className="text-muted-foreground">{d.path}</span></li>)}
        </ul>
      )}
    </div>
  );
}

function syncVariant(status?: string): "success" | "destructive" | "warning" | "secondary" {
  if (!status) return "secondary";
  if (status === "ok") return "success";
  if (status === "error" || status === "key_expired") return "destructive";
  return "warning";
}

function BindingCard({
  projectId, binding, entry, onChanged,
}: {
  projectId: string; binding: ProjectBinding; entry: CatalogEntry | undefined; onChanged: () => void;
}) {
  const { t } = useTranslation("integrations");
  const id = binding.extension_id;
  const name = entry?.name ?? id;
  const schema = entry?.manifest.settings?.project;
  const [actions, setActions] = useState<ProjectAction[]>([]);
  const [busy, setBusy] = useState<string | null>(null);
  const [checkMsg, setCheckMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [confirmUnbind, setConfirmUnbind] = useState(false);

  useEffect(() => {
    let cancelled = false;
    listProjectActions(projectId, id).then((a) => { if (!cancelled) setActions(a); }).catch(() => {});
    return () => { cancelled = true; };
  }, [projectId, id]);

  const run = async (key: string, fn: () => Promise<void>) => {
    setBusy(key);
    try { await fn(); } catch (err) { toast.error(apiErrorMessage(err, t("projectTab.actionFailed"))); } finally { setBusy(null); }
  };

  const check = () => run("check", async () => {
    const r = await checkProjectExtension(projectId, id);
    setCheckMsg({ ok: r.ok, text: r.message || (r.ok ? t("projectTab.checkOk") : t("projectTab.checkFailed")) });
    onChanged();
  });
  const unbind = () => run("unbind", async () => {
    await deleteProjectExtension(projectId, id);
    setConfirmUnbind(false);
    toast.success(t("projectTab.unbound", { name }));
    onChanged();
  });
  const toggleEnabled = () => run("enabled", async () => {
    await saveProjectExtension(projectId, id, { enabled: !binding.enabled, settings: binding.settings });
    onChanged();
  });
  const extra = actions.filter((a) => a.method === EXTRA_ACTION_METHOD && a.name !== "check");
  const canSearch = actions.some((a) => a.method === "GET" && a.name === "search");

  return (
    <Card data-testid={`binding-${id}`}>
      <CardHeader>
        <div className="flex flex-col gap-3">
          <CardTitle className="flex items-center gap-2">
            {name}
            <Badge variant={binding.enabled ? "success" : "secondary"}>
              {binding.enabled ? t("projectTab.enabled") : t("projectTab.disabled")}
            </Badge>
          </CardTitle>
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" size="sm" onClick={check} disabled={busy !== null}>
              <CheckCircle2 className="mr-1.5 h-4 w-4" aria-hidden />{t("projectTab.check")}
            </Button>
            {extra.map((a) => (
              <Button key={a.name} variant="outline" size="sm" disabled={busy !== null}
                onClick={() => run(a.name, async () => {
                  await runProjectAction(projectId, id, a.name);
                  toast.success(t("projectTab.actionDone", { title: a.title || a.name }));
                  onChanged();
                })}>
                <RefreshCw className="mr-1.5 h-4 w-4" aria-hidden />{a.title || a.name}
              </Button>
            ))}
            <Button variant="outline" size="sm" onClick={toggleEnabled} disabled={busy !== null}>
              {binding.enabled ? t("projectTab.disable") : t("projectTab.enable")}
            </Button>
            <Button variant="outline" size="sm" onClick={() => setConfirmUnbind(true)} disabled={busy !== null}>
              <Unlink className="mr-1.5 h-4 w-4" aria-hidden />{t("projectTab.unbind")}
            </Button>
          </div>
        </div>
        <CardDescription className="flex flex-wrap items-center gap-x-4 gap-y-1" data-testid={`sync-${id}`}>
          <span className="flex items-center gap-1.5">
            {t("projectTab.sync.label")}
            <Badge variant={syncVariant(binding.last_sync_status)}>
              {binding.last_sync_status ? t(`projectTab.sync.status.${binding.last_sync_status}`, { defaultValue: binding.last_sync_status }) : t("projectTab.sync.never")}
            </Badge>
          </span>
          {binding.last_sync_checked_at && (
            <span>{t("projectTab.sync.checkedAt", { when: i18nFmt.formatDateTime(binding.last_sync_checked_at) })}</span>
          )}
          {binding.key_expires_at && (
            <span className={binding.key_expiring_soon ? "text-warning" : undefined}>
              {binding.key_expiring_soon && <AlertTriangle className="mr-1 inline h-3.5 w-3.5" aria-hidden />}
              {t("projectTab.keyExpires", { when: i18nFmt.formatDate(binding.key_expires_at) })}
            </span>
          )}
        </CardDescription>
        {binding.last_sync_error && (
          <p role="alert" className="text-sm text-destructive">{binding.last_sync_error}</p>
        )}
        {checkMsg && (
          <p role="status" className={checkMsg.ok ? "text-sm text-success" : "text-sm text-destructive"}>{checkMsg.text}</p>
        )}
      </CardHeader>
      <CardContent className="space-y-4">
        {canSearch && <SearchAction projectId={projectId} id={id} />}
        {schema ? (
          <SchemaForm
            schema={schema as FieldSchema}
            manifest={entry?.manifest}
            value={binding.settings}
            onSubmit={async (settings) => {
              await saveProjectExtension(projectId, id, { enabled: binding.enabled, settings });
              onChanged();
            }}
          />
        ) : (
          <p className="text-sm text-muted-foreground">{t("projectTab.noSchema")}</p>
        )}
      </CardContent>
      <Dialog open={confirmUnbind} onOpenChange={setConfirmUnbind}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("projectTab.unbindTitle", { name })}</DialogTitle>
            <DialogDescription>{t("projectTab.unbindDesc")}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmUnbind(false)}>{t("projectTab.cancel")}</Button>
            <Button variant="destructive" onClick={unbind} disabled={busy !== null}>{t("projectTab.unbind")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}

function NewBinding({
  projectId, entries, onDone,
}: { projectId: string; entries: CatalogEntry[]; onDone: (extensionId: string) => void }) {
  const { t } = useTranslation("integrations");
  const [pick, setPick] = useState(entries[0]?.id ?? "");
  const entry = entries.find((e) => e.id === pick) ?? entries[0];
  const schema = entry?.manifest.settings?.project;
  if (!entry) return null;
  return (
    <Card data-testid="new-binding">
      <CardHeader>
        <CardTitle>{t("projectTab.bindTitle")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="space-y-1.5">
          <label className="text-sm font-medium" htmlFor="bind-extension">{t("projectTab.pickExtension")}</label>
          <Select id="bind-extension" value={entry.id} onChange={(e) => setPick(e.target.value)}>
            {entries.map((e) => <option key={e.id} value={e.id}>{e.name}</option>)}
          </Select>
        </div>
        {schema && (
          <SchemaForm
            key={entry.id}
            schema={schema as FieldSchema}
            manifest={entry.manifest}
            submitLabel={t("projectTab.bind")}
            onSubmit={async (settings) => {
              await saveProjectExtension(projectId, entry.id, { enabled: true, settings });
              toast.success(t("projectTab.bound", { name: entry.name }));
              onDone(entry.id);
            }}
          />
        )}
      </CardContent>
    </Card>
  );
}

/**
 * «Расширения» tab of project settings (spec 04 §9): the bindings this project
 * has, each a SchemaForm over the manifest's project level plus sync status,
 * «проверить» and «отвязать»; and a way to bind another connected extension.
 */
export function ProjectExtensionsTab({ projectId }: { projectId: string }) {
  const { t } = useTranslation("integrations");
  const catalog = useExtensionCatalog();
  const [bindings, setBindings] = useState<ProjectBinding[] | null>(null);
  const [failed, setFailed] = useState(false);
  const [adding, setAdding] = useState(false);

  // Only the newest request may write state: a slow answer for the previous
  // project must not land under the current one.
  const latest = useRef(0);
  const load = useCallback(() => {
    const seq = ++latest.current;
    setFailed(false);
    listProjectBindings(projectId)
      .then((list) => { if (seq === latest.current) setBindings(list); })
      .catch(() => { if (seq === latest.current) setFailed(true); });
  }, [projectId]);
  useEffect(() => { setBindings(null); load(); }, [load]);

  const byId = useMemo(() => new Map(catalog.entries.map((e) => [e.id, e])), [catalog.entries]);
  const { wsSlug, projectSlug } = useParams();
  const genericBindings = (bindings ?? []).filter((b) => b.extension_id !== CHAT_EXTENSION_ID);
  const bound = new Set((bindings ?? []).map((b) => b.extension_id));
  const bindable = catalog.entries.filter(
    (e) => e.manifest.levels?.project && e.connected && e.enabled && !bound.has(e.id) && e.id !== CHAT_EXTENSION_ID,
  );
  const [searchParams, setSearchParams] = useSearchParams();
  const tabs = [
    ...(byId.has(CHAT_EXTENSION_ID) ? [{ id: CHAT_EXTENSION_ID, name: byId.get(CHAT_EXTENSION_ID)!.name }] : []),
    ...genericBindings.map((b) => ({ id: b.extension_id, name: byId.get(b.extension_id)?.name ?? b.extension_id })),
  ];
  const wanted = searchParams.get("ext");
  const activeId = tabs.find((x) => x.id === wanted)?.id ?? tabs[0]?.id;
  const selectTab = (ext: string) => {
    setAdding(false);
    setSearchParams((prev) => { const n = new URLSearchParams(prev); n.set("ext", ext); return n; }, { replace: true });
  };
  const refresh = (ext?: string) => { setAdding(false); load(); if (ext) selectTab(ext); };

  if (failed || catalog.state === "error") {
    return (
      <Card data-testid="extensions-tab-error">
        <CardContent className="flex flex-col items-start gap-3 py-6">
          <p role="alert" className="flex items-center gap-2 text-sm text-destructive">
            <AlertTriangle className="h-4 w-4" aria-hidden />{t("projectTab.loadFailed")}
          </p>
          <Button variant="outline" size="sm" onClick={() => { load(); catalog.reload(); }}>{t("projectTab.retry")}</Button>
        </CardContent>
      </Card>
    );
  }
  if (bindings === null || catalog.state === "loading") {
    return (
      <div className="space-y-3" data-testid="extensions-tab-loading">
        <Skeleton className="h-24 w-full" /><Skeleton className="h-48 w-full" />
      </div>
    );
  }

  const activeBinding = genericBindings.find((b) => b.extension_id === activeId);

  return (
    <div className="mx-auto max-w-2xl space-y-4" data-testid="extensions-tab">
      <p className="text-sm text-muted-foreground">{t("projectTab.intro")}</p>
      <div className="flex flex-wrap items-center gap-2 border-b border-border pb-2" role="tablist">
        {tabs.map((x) => {
          const on = !adding && x.id === activeId;
          return (
            <Button key={x.id} role="tab" aria-selected={on} data-testid={`ext-tab-${x.id}`} variant={on ? "secondary" : "ghost"} size="sm" onClick={() => selectTab(x.id)}>
              {x.name}
            </Button>
          );
        })}
        {bindable.length > 0 && (
          <Button size="sm" variant={adding ? "secondary" : "outline"} className="ml-auto" onClick={() => setAdding(true)}>
            <Plus className="mr-1.5 h-4 w-4" aria-hidden />{t("projectTab.bindButton")}
          </Button>
        )}
      </div>
      {adding && <NewBinding key={projectId} projectId={projectId} entries={bindable} onDone={refresh} />}
      {!adding && activeId === CHAT_EXTENSION_ID && <ChatSettingsCard projectId={projectId} chatPath={`/w/${wsSlug}/p/${projectSlug}/chat`} />}
      {!adding && activeBinding && activeId !== CHAT_EXTENSION_ID && (
        <BindingCard key={`${projectId}:${activeBinding.extension_id}`} projectId={projectId} binding={activeBinding} entry={byId.get(activeBinding.extension_id)} onChanged={load} />
      )}
      {/* Chat has its own on/off and is always configurable, so it is a tab even
          with no binding; the empty state still speaks for the bindings. */}
      {!adding && genericBindings.length === 0 && (
        <Card data-testid="extensions-tab-empty">
          <CardContent className="py-6 text-sm text-muted-foreground">
            {bindable.length > 0 ? t("projectTab.empty") : t("projectTab.emptyNoneConnected")}
          </CardContent>
        </Card>
      )}
    </div>
  );
}
