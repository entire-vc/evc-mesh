import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, useNavigate, useParams } from "react-router";
import { AlertTriangle, MoreHorizontal, PackageSearch, Settings2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "@/components/ui/toast";
import { ConfirmDialog } from "@/components/confirm-dialog";
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { SchemaForm, type FieldSchema, type Values } from "@/components/extensions/schema-form";
import { ExtensionIcon, KindTrustBadges } from "@/components/extensions/catalog";
import { ExtensionStatusPanel, VCS_EXTENSION_IDS, VcsRepoList, instanceManaged } from "@/components/extensions/status-panel";
import { useIsInstanceAdmin } from "@/lib/extension-admin";
import type { CatalogEntry, CatalogState } from "@/hooks/useExtensionCatalog";
import { consentTokens, describeCapabilities } from "@/lib/extension-capabilities";
import { installedStatus, workspaceSettingsSchema } from "@/lib/extension-installed";
import {
  connectWorkspaceExtension, disconnectWorkspaceExtension, saveWorkspaceExtensionSettings,
  setWorkspaceExtensionEnabled, testWorkspaceExtension,
} from "@/lib/extension-settings";
import { apiErrorMessage } from "@/lib/api-error";
import { useWorkspaceStore } from "@/stores/workspace";
import { useMemberStore } from "@/stores/member";
import { cn } from "@/lib/cn";

// A dialog belongs to the workspace it was opened in: it keeps writing there even if the user switches away.
type Dlg =
  | { type: "settings"; entry: CatalogEntry; wsId: string }
  | { type: "disable-instance"; entry: CatalogEntry; wsId: string }
  | { type: "remove"; entry: CatalogEntry; wsId: string };

function SettingsDialog({ entry, workspaceId, onClose, onDone }: {
  entry: CatalogEntry; workspaceId: string; onClose: () => void; onDone: () => void;
}) {
  const { t } = useTranslation("integrations");
  const { wsSlug = "" } = useParams();
  const isInstanceAdmin = useIsInstanceAdmin();
  // Same predicate as the row: an instance-governed extension is connected, never offered Connect again.
  const connected = entry.connected;
  const managed = instanceManaged(entry);
  // Own credentials over an instance connection are an explicit opt-in, folded away by default.
  const [own, setOwn] = useState(false);
  // What a submit writes to: the workspace's own connection row, if it has one.
  const hasOwn = Boolean(entry.connection);
  const [agree, setAgree] = useState(hasOwn);
  const schema = workspaceSettingsSchema(entry);
  const lines = describeCapabilities(entry.manifest.capabilities);
  const showForm = !managed || own;
  const ownRef = useRef<HTMLDivElement>(null);
  // On a phone the opened form starts below the fold; bring it to the user.
  useEffect(() => { if (own) ownRef.current?.scrollIntoView?.({ block: "start", behavior: "smooth" }); }, [own]);

  const submit = async (settings: Values) => {
    if (hasOwn) await saveWorkspaceExtensionSettings(workspaceId, entry.id, settings);
    else await connectWorkspaceExtension(workspaceId, entry.id, settings, consentTokens(entry.manifest.capabilities));
    toast.success(t(hasOwn ? "installed.saved" : "installed.connected", { name: entry.name }));
    onDone();
  };

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} className="max-w-xl">
      <DialogContent onClose={onClose} className="max-h-[85vh] overflow-y-auto" role="dialog" aria-label={entry.name}>
        <DialogHeader>
          <DialogTitle>{t(connected ? "installed.settingsTitle" : "installed.connectTitle", { name: entry.name })}</DialogTitle>
          <DialogDescription>{t(managed ? "installed.managedHint" : connected ? "installed.settingsHint" : "installed.connectHint")}</DialogDescription>
        </DialogHeader>
        {connected && <ExtensionStatusPanel entry={entry} className="mt-4" instanceNote={!managed} />}
        {managed && (
          <div className="mt-4 space-y-4">
            {VCS_EXTENSION_IDS.has(entry.id) && <VcsRepoList entry={entry} />}
            {isInstanceAdmin ? (
              <Link to={`/w/${wsSlug}/instance/extensions?ext=${encodeURIComponent(entry.id)}`} className="inline-block text-sm text-primary hover:underline">
                {t("installed.editInstance")}
              </Link>
            ) : (
              <p className="text-sm text-muted-foreground">{t("installed.instanceCredentials")}</p>
            )}
          </div>
        )}
        {managed && !own && (
          <DialogFooter className="mt-4">
            {schema && (
              <Button variant="ghost" onClick={() => setOwn(true)}>{t("installed.useOwn")}</Button>
            )}
            <Button variant="outline" onClick={onClose}>{t("installed.close")}</Button>
          </DialogFooter>
        )}
        {managed && own && (
          <div ref={ownRef} className="mt-4 flex flex-wrap items-center justify-between gap-2 border-t border-border pt-4">
            <p className="text-sm text-muted-foreground">{t("installed.useOwnHint")}</p>
            <Button variant="outline" size="sm" onClick={() => setOwn(false)}>{t("installed.cancel")}</Button>
          </div>
        )}
        {showForm && !hasOwn && (
          <div className="mt-4 space-y-2 rounded-lg border border-border p-3">
            <p className="text-sm font-medium">{t("installed.consentTitle")}</p>
            {lines.length === 0 ? (
              <p className="text-sm text-muted-foreground">{t("detail.permissions.none")}</p>
            ) : (
              <ul className="space-y-1 text-sm">
                {lines.map((l) => <li key={`${l.group}:${l.raw}`}>· {t(l.key, l.values)}</li>)}
              </ul>
            )}
            {entry.trust === "custom" && (
              <p role="note" className="rounded bg-destructive/10 px-2 py-1 text-sm text-destructive">{t("installed.customWarning")}</p>
            )}
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={agree} onChange={(e) => setAgree(e.target.checked)} className="h-4 w-4 accent-primary" />
              {t("installed.consentCheck")}
            </label>
          </div>
        )}
        {showForm && <div className="mt-4">
          {schema ? (
            <SchemaForm
              schema={schema as FieldSchema}
              manifest={entry.manifest}
              value={entry.connection?.settings as Values | undefined}
              onSubmit={submit}
              submitLabel={t(hasOwn ? "installed.save" : "installed.connect")}
              disabled={!agree}
            />
          ) : (
            <>
              <p className="text-sm text-muted-foreground">{t("installed.noSettings")}</p>
              {!hasOwn && (
                <DialogFooter className="mt-4">
                  <Button disabled={!agree} onClick={() => void submit({}).catch((e) => toast.error(apiErrorMessage(e, t("installed.actionFailed"))))}>
                    {t("installed.connect")}
                  </Button>
                </DialogFooter>
              )}
            </>
          )}
        </div>}
      </DialogContent>
    </Dialog>
  );
}

function RemoveDialog({ entry, onClose, onConfirm }: {
  entry: CatalogEntry; onClose: () => void; onConfirm: (data: "keep" | "delete") => void;
}) {
  const { t } = useTranslation("integrations");
  const [data, setData] = useState<"keep" | "delete">("keep");
  const opt = (value: "keep" | "delete", disabled = false) => (
    <label className={cn("flex items-start gap-2 text-sm", disabled && "opacity-50")}>
      <input type="radio" name="data-fate" value={value} checked={data === value} disabled={disabled}
        onChange={() => setData(value)} className="mt-1 h-4 w-4 accent-primary" />
      <span>
        {t(`installed.data.${value}`)}
        <span className="block text-xs text-muted-foreground">{t(disabled ? "installed.data.deleteUnsupported" : `installed.data.${value}Hint`)}</span>
      </span>
    </label>
  );
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent onClose={onClose} role="dialog" aria-label={t("installed.removeTitle", { name: entry.name })}>
        <DialogHeader>
          <DialogTitle>{t("installed.removeTitle", { name: entry.name })}</DialogTitle>
          <DialogDescription>{t("installed.removeBody")}</DialogDescription>
        </DialogHeader>
        <fieldset className="mt-4 space-y-3">
          <legend className="mb-2 text-sm font-medium">{t("installed.data.title")}</legend>
          {opt("keep")}
          {opt("delete", !entry.canPurgeData)}
        </fieldset>
        <DialogFooter className="mt-4">
          <Button variant="outline" onClick={onClose}>{t("installed.cancel")}</Button>
          <Button variant="destructive" onClick={() => onConfirm(data)}>{t("installed.remove")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function Row({ entry, canManage, busy, onToggle, onSettings, onTest, onRemove }: {
  entry: CatalogEntry; canManage: boolean; busy: boolean;
  onToggle: (e: CatalogEntry) => void; onSettings: (e: CatalogEntry) => void;
  onTest: (e: CatalogEntry) => void; onRemove: (e: CatalogEntry) => void;
}) {
  const { t } = useTranslation("integrations");
  const { wsSlug = "" } = useParams();
  const navigate = useNavigate();
  const st = installedStatus(entry);
  const on = Boolean(entry.status?.enabled);
  const connected = entry.connected;
  const health = entry.status?.health;
  const healthLabel = t(health ? (health.ok ? "installed.health.ok" : "installed.health.bad") : "installed.health.unknown");
  const elsewhere = entry.managedElsewhere ? t("installed.managedElsewhere") : undefined;
  const noRemove = !canManage || !entry.connection || entry.managedElsewhere;
  return (
    <li className="rounded-xl border border-border bg-card p-4" data-ext={entry.id}>
      <div className="flex flex-wrap items-start gap-3">
        <ExtensionIcon entry={entry} />
        <div className="min-w-[12rem] flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <Link to={`/w/${wsSlug}/extensions/${entry.id}`} className="text-sm font-semibold hover:underline">{entry.name}</Link>
            <span className="text-xs text-muted-foreground">{entry.version}</span>
            <KindTrustBadges entry={entry} />
          </div>
          <p className="mt-1 flex items-center gap-2 text-sm" data-testid={`status-${entry.id}`}>
            <span role="img" aria-label={healthLabel} title={healthLabel} className={cn("h-2.5 w-2.5 shrink-0 rounded-full", health ? (health.ok ? "bg-success" : "bg-destructive") : "bg-muted-foreground/40")} />
            {t(st.key, st.values)}
          </p>
          {entry.blocked && st.kind !== "blocked" && (
            <p className="mt-1 text-xs text-destructive">{t("installed.status.blocked", { reason: entry.blockedReason || "-" })}</p>
          )}
          {entry.status?.last_error && st.kind !== "error" && (
            <p className="mt-1 text-xs text-muted-foreground">{t("installed.lastError", { message: entry.status.last_error })}</p>
          )}
        </div>
        {entry.kind === "theme" ? (
          // a theme is chosen in its own screen (settings, preview, apply); it has no switch, check or removal here
          <div className="flex items-center gap-2 pl-[3.25rem] sm:pl-0">
            <Button variant="outline" size="sm" disabled={!canManage || entry.blocked}
              onClick={() => navigate(`/w/${wsSlug}/extensions/${entry.id}/connect`)}>
              <Settings2 className="mr-1 h-4 w-4" aria-hidden />
              {t(entry.themeActive ? "installed.configureTheme" : "installed.chooseTheme")}
            </Button>
          </div>
        ) : (
        <div className="flex items-center gap-2 pl-[3.25rem] sm:pl-0">
          <button
            type="button" role="switch" aria-checked={on}
            aria-label={t("installed.toggle", { name: entry.name })}
            disabled={!canManage || busy || entry.blocked || (!on && !entry.connection && entry.managedElsewhere)}
            title={!on && !entry.connection ? elsewhere : undefined}
            onClick={() => onToggle(entry)}
            className={cn("relative h-6 w-11 shrink-0 rounded-full transition-colors disabled:opacity-50", on ? "bg-primary" : "bg-muted")}
          >
            <span className={cn("absolute top-0.5 h-5 w-5 rounded-full bg-background shadow transition-all", on ? "left-[1.375rem]" : "left-0.5")} />
          </button>
          <Button variant="outline" size="sm" disabled={!canManage || busy || entry.blocked || entry.managedElsewhere}
            title={elsewhere} onClick={() => onSettings(entry)}>
            <Settings2 className="mr-1 h-4 w-4" aria-hidden />
            {t(connected ? "installed.settings" : "installed.connect")}
          </Button>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon" aria-label={t("installed.more", { name: entry.name })}>
                <MoreHorizontal className="h-4 w-4" aria-hidden />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem disabled={!canManage || !connected} onClick={() => onTest(entry)}>{t("installed.test")}</DropdownMenuItem>
              <DropdownMenuItem disabled title={t("installed.rollbackSoon")}>{t("installed.rollback")}</DropdownMenuItem>
              <DropdownMenuItem disabled={noRemove} title={elsewhere} onClick={() => onRemove(entry)} className="text-destructive">{t("installed.remove")}</DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
        )}
      </div>
    </li>
  );
}

export function InstalledExtensions({ catalog }: { catalog: CatalogState }) {
  const { t } = useTranslation("integrations");
  const { currentWorkspace } = useWorkspaceStore();
  const { myRole } = useMemberStore();
  const canManage = myRole === "owner" || myRole === "admin";
  const wsId = currentWorkspace?.id ?? "";
  const [dlg, setDlg] = useState<Dlg | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  useEffect(() => { setDlg(null); }, [wsId]);

  const run = async (id: string, fn: () => Promise<unknown>, ok?: string) => {
    setBusy(id);
    try {
      await fn();
      if (ok) toast.success(ok);
      catalog.refresh();
    } catch (e) {
      toast.error(apiErrorMessage(e, t("installed.actionFailed")));
    } finally {
      setBusy(null);
    }
  };

  const toggle = (e: CatalogEntry) => {
    const on = Boolean(e.status?.enabled);
    if (!on) {
      if (e.connection) void run(e.id, () => setWorkspaceExtensionEnabled(wsId, e.id, true));
      else if (!e.managedElsewhere) setDlg({ type: "settings", entry: e, wsId });
      return;
    }
    // Switching off what the instance governs only switches it off here; say so first.
    if (e.status?.source === "instance") setDlg({ type: "disable-instance", entry: e, wsId });
    else void run(e.id, () => setWorkspaceExtensionEnabled(wsId, e.id, false));
  };

  const test = (e: CatalogEntry) =>
    run(e.id, async () => {
      const r = await testWorkspaceExtension(wsId, e.id);
      if (r.ok) toast.success(t("installed.testOk", { name: e.name }));
      else toast.error(t("installed.testFailed", { name: e.name, message: r.message || "-" }));
    });

  if (catalog.state === "loading") {
    return (
      <div role="status" aria-label={t("installed.loading")} className="space-y-3">
        <Skeleton className="h-20 w-full rounded-xl" /><Skeleton className="h-20 w-full rounded-xl" />
      </div>
    );
  }
  if (catalog.state === "error") {
    return (
      <div role="alert" className="flex flex-col items-center gap-2 rounded-xl border border-dashed border-border px-6 py-12 text-center">
        <AlertTriangle className="h-8 w-8 text-muted-foreground" aria-hidden />
        <p className="text-sm font-medium">{t("installed.errorTitle")}</p>
        <p className="max-w-prose text-sm text-muted-foreground">{t("installed.errorBody")}</p>
        <Button variant="outline" size="sm" onClick={catalog.reload}>{t("extensionsPage.catalog.retry")}</Button>
      </div>
    );
  }
  const rows = catalog.entries.filter((e) => e.installed && !e.hidden);
  if (rows.length === 0) {
    return (
      <div className="flex flex-col items-center gap-2 rounded-xl border border-dashed border-border px-6 py-12 text-center">
        <PackageSearch className="h-8 w-8 text-muted-foreground" aria-hidden />
        <p className="text-sm font-medium">{t("installed.emptyTitle")}</p>
        <p className="max-w-prose text-sm text-muted-foreground">{t("installed.emptyBody")}</p>
      </div>
    );
  }

  return (
    <div className="space-y-3">
      {!canManage && <Badge variant="secondary">{t("installed.readOnly")}</Badge>}
      <ul className="space-y-3 max-w-3xl">
        {rows.map((e) => (
          <Row key={e.id} entry={e} canManage={canManage} busy={busy === e.id}
            onToggle={toggle} onSettings={(x) => setDlg({ type: "settings", entry: x, wsId })}
            onTest={test} onRemove={(x) => setDlg({ type: "remove", entry: x, wsId })} />
        ))}
      </ul>

      {dlg?.type === "settings" && (
        <SettingsDialog entry={dlg.entry} workspaceId={dlg.wsId} onClose={() => setDlg(null)}
          onDone={() => { setDlg(null); catalog.refresh(); }} />
      )}
      <ConfirmDialog
        open={dlg?.type === "disable-instance"}
        onClose={() => setDlg(null)}
        title={t("installed.disableInstanceTitle", { name: dlg?.entry.name ?? "" })}
        description={t("installed.disableInstanceBody")}
        confirmText={t("installed.disableInstanceConfirm")}
        variant="destructive"
        onConfirm={() => {
          const e = dlg?.entry;
          const ws = dlg?.wsId ?? wsId;
          setDlg(null);
          if (e) void run(e.id, () => setWorkspaceExtensionEnabled(ws, e.id, false, true));
        }}
      />
      {dlg?.type === "remove" && (
        <RemoveDialog entry={dlg.entry} onClose={() => setDlg(null)}
          onConfirm={(data) => {
            const e = dlg.entry;
            const ws = dlg.wsId;
            setDlg(null);
            void run(e.id, () => disconnectWorkspaceExtension(ws, e.id, data), t("installed.removed", { name: e.name }));
          }} />
      )}
    </div>
  );
}
