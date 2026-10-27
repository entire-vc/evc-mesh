import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
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
import { ExtensionIcon, KindSections, KindTrustBadges } from "@/components/extensions/catalog";
import { ThemeSettingsPanel } from "@/components/themes/theme-settings-panel";
import { applyWorkspaceTheme } from "@/core/themes/runtime";
import { putWorkspaceTheme } from "@/lib/themes";
import { ExtensionStatusPanel, VCS_EXTENSION_IDS, VcsRepoList, instanceManaged } from "@/components/extensions/status-panel";
import { useIsInstanceAdmin } from "@/lib/extension-admin";
import type { CatalogEntry, CatalogState } from "@/hooks/useExtensionCatalog";
import { consentTokens, describeCapabilities } from "@/lib/extension-capabilities";
import { receivingNote, workspaceSettingsSchema } from "@/lib/extension-installed";
import { OPEN_SETTINGS_PARAM } from "@/lib/extension-settings-route";
import { extensionState } from "@/lib/extensionState";
import { ExtensionStateBadge, ExtensionStateNote } from "@/components/extensions/extension-state";
import {
  connectWorkspaceExtension, disconnectWorkspaceExtension, saveWorkspaceExtensionSettings,
  setWorkspaceExtensionEnabled, testWorkspaceExtension,
} from "@/lib/extension-settings";
import { apiErrorMessage } from "@/lib/api-error";
import { useWorkspaceStore } from "@/stores/workspace";
import { useMemberStore } from "@/stores/member";
import { cn } from "@/lib/cn";
import { isRunnerProduct, runnerScreenPath } from "@/lib/runner-product";

// A dialog belongs to the workspace it was opened in: it keeps writing there even if the user switches away.
type Dlg =
  | { type: "settings"; entry: CatalogEntry; wsId: string }
  | { type: "disable-instance"; entry: CatalogEntry; wsId: string }
  | { type: "remove"; entry: CatalogEntry; wsId: string };

// A theme's settings are the theme panel opened on it, in the same window as an extension's (П8).
function ThemeSettingsDialog({ entry, workspaceId, canManage, onClose, onSaved }: {
  entry: CatalogEntry; workspaceId: string; canManage: boolean; onClose: () => void; onSaved: () => void;
}) {
  const { t } = useTranslation("integrations");
  const { myRoleLoaded, myRoleError } = useMemberStore();
  // Save / Reset live in the footer, outside the scroll area, so they never cover a theme card at 393 px
  const [actions, setActions] = useState<HTMLDivElement | null>(null);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} className="max-w-3xl">
      <DialogContent onClose={onClose} className="flex max-h-[85vh] flex-col" role="dialog" aria-label={entry.name}>
        <DialogHeader>
          <DialogTitle>{t("installed.settingsTitle", { name: entry.name })}</DialogTitle>
          <DialogDescription>{t("installed.themeSettingsHint")}</DialogDescription>
        </DialogHeader>
        <div className="-mx-6 mt-4 min-h-0 flex-1 overflow-y-auto px-6 pb-1" data-testid="theme-settings-body">
          <ThemeSettingsPanel
            key={`${workspaceId}:${entry.id}`}
            level="workspace"
            workspaceId={workspaceId}
            initialThemeId={entry.id}
            readOnly={!canManage}
            roleLoading={!canManage && !myRoleLoaded && !myRoleError}
            roleUnknown={!!myRoleError}
            onSaved={onSaved}
            actionsContainer={actions}
          />
        </div>
        <DialogFooter className="-mx-6 mt-0 flex-row items-start justify-between gap-3 border-t px-6 pt-4 sm:space-x-0">
          <div ref={setActions} className="min-w-0 flex-1" />
          <Button variant="outline" onClick={onClose}>{t("installed.close")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function SettingsDialog({ entry, workspaceId, onClose, onDone, onCheck, checking }: {
  entry: CatalogEntry; workspaceId: string; onClose: () => void; onDone: () => void;
  onCheck: () => void; checking: boolean;
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
        {connected && <ExtensionStatusPanel entry={entry} className="mt-4" instanceNote={!managed} onCheck={onCheck} checking={checking} />}
        {managed && (
          <div className="mt-4 space-y-4">
            {VCS_EXTENSION_IDS.has(entry.id) && <VcsRepoList entry={entry} />}
            {isInstanceAdmin ? (
              <Link to={`/w/${wsSlug}/instance?ext=${encodeURIComponent(entry.id)}`} className="inline-block text-sm text-primary hover:underline">
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
          <div ref={ownRef} className="mt-4 border-t border-border pt-4">
            <p className="text-sm text-muted-foreground">{t("installed.useOwnHint")}</p>
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
              actions={managed && own ? <Button type="button" variant="outline" onClick={() => setOwn(false)}>{t("installed.cancel")}</Button> : undefined}
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
  const st = extensionState(entry);
  const receiving = st.state === "available" ? receivingNote(entry) : null;
  const theme = entry.kind === "theme";
  // a theme is on in the workspace when it is the one the workspace paints with
  const on = theme ? Boolean(entry.themeActive) : Boolean(entry.status?.enabled);
  const connected = entry.connected;
  const health = entry.status?.health;
  const healthLabel = t(health ? (health.ok ? "installed.health.ok" : "installed.health.bad") : "installed.health.unknown");
  const elsewhere = entry.managedElsewhere ? t("installed.managedElsewhere") : undefined;
  // Not allowed by the instance administrator: it cannot be switched on or connected; switching it off stays possible.
  const notAllowed = !entry.allowed || Boolean(entry.status?.not_allowed);
  const notAllowedHint = notAllowed ? t("catalog.state.notAllowed") : undefined;
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
          <div className="mt-1 space-y-1" data-testid={`status-${entry.id}`}>
            <p className="flex flex-wrap items-center gap-2">
              {entry.kind !== "theme" && (
                <span role="img" aria-label={healthLabel} title={healthLabel} className={cn("h-2.5 w-2.5 shrink-0 rounded-full", health ? (health.ok ? "bg-success" : "bg-destructive") : "bg-muted-foreground/40")} />
              )}
              <ExtensionStateBadge state={st} />
              {st.state === "error" && canManage && (
                <Button variant="outline" size="sm" className="h-7" disabled={busy} onClick={() => onTest(entry)}>{t("extensionState.check")}</Button>
              )}
            </p>
            <ExtensionStateNote state={st} />
            {receiving && <p className="text-xs text-muted-foreground">{t(receiving.key, receiving.values)}</p>}
          </div>
        </div>
        {isRunnerProduct(entry.id) ? (
          // the runner program has one screen for everything (address, switch, hosts): the Runner section
          <div className="flex items-center gap-2 pl-[3.25rem] sm:pl-0">
            <Button variant="outline" size="sm" title={t("installed.managedInRunner")} onClick={() => navigate(runnerScreenPath(wsSlug))}>
              <Settings2 className="mr-1 h-4 w-4" aria-hidden />
              {t("installed.manageInRunner")}
            </Button>
          </div>
        ) : (
        <div className="flex items-center gap-2 pl-[3.25rem] sm:pl-0">
          <button
            type="button" role="switch" aria-checked={on}
            aria-label={t("installed.toggle", { name: entry.name })}
            disabled={!canManage || busy || entry.blocked || (!on && notAllowed) || (!theme && !on && !entry.connection && entry.managedElsewhere)}
            title={!on && notAllowed ? notAllowedHint : !on && !entry.connection ? elsewhere : undefined}
            onClick={() => onToggle(entry)}
            className={cn("relative h-6 w-11 shrink-0 rounded-full transition-colors disabled:opacity-50", on ? "bg-primary" : "bg-muted")}
          >
            <span className={cn("absolute top-0.5 h-5 w-5 rounded-full bg-background shadow transition-all", on ? "left-[1.375rem]" : "left-0.5")} />
          </button>
          <Button variant="outline" size="sm" disabled={(!canManage && !(theme && on)) || busy || entry.blocked || (!theme && entry.managedElsewhere) || (!connected && !on && notAllowed)}
            title={!connected && !on && notAllowed ? notAllowedHint : theme ? undefined : elsewhere} onClick={() => onSettings(entry)}>
            <Settings2 className="mr-1 h-4 w-4" aria-hidden />
            {t(connected || theme ? "installed.settings" : "installed.connect")}
          </Button>
          {/* a theme has no check, rollback or removal from a workspace: it is switched off instead */}
          {!theme && <DropdownMenu>
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
          </DropdownMenu>}
        </div>
        )}
      </div>
    </li>
  );
}

export function InstalledExtensions({ catalog }: { catalog: CatalogState }) {
  const { t } = useTranslation("integrations");
  const { currentWorkspace } = useWorkspaceStore();
  const { myRole, myRoleLoaded, myRoleError } = useMemberStore();
  const canManage = myRole === "owner" || myRole === "admin";
  const wsId = currentWorkspace?.id ?? "";
  const [dlg, setDlg] = useState<Dlg | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [search, setSearch] = useSearchParams();
  const openId = search.get(OPEN_SETTINGS_PARAM);
  const { wsSlug = "" } = useParams();
  const navigate = useNavigate();

  useEffect(() => { setDlg(null); }, [wsId]);

  // "Open settings" elsewhere lands here with `?settings=<id>`: open that extension's dialog once the
  // list and the role are known, then drop the parameter so closing the dialog does not reopen it.
  const roleKnown = canManage || myRoleLoaded || Boolean(myRoleError);
  useEffect(() => {
    if (!openId || catalog.state !== "ready" || !wsId || !roleKnown) return;
    const target = catalog.entries.find((e) => e.id === openId && e.installed && !e.hidden);
    // the runner program has no settings window: an old `?settings=agent-runner` link goes to the Runner screen
    if (target && isRunnerProduct(target.id)) {
      navigate(runnerScreenPath(wsSlug), { replace: true });
      return;
    }
    // The active theme's window opens read-only for members, as the theme panel did before (detail offers it to all).
    const theme = target?.kind === "theme";
    if (target && !target.blocked && (canManage ? theme || !target.managedElsewhere : theme && target.themeActive)) {
      setDlg({ type: "settings", entry: target, wsId });
    } else if (target) {
      Array.from(document.querySelectorAll<HTMLElement>("[data-ext]"))
        .find((el) => el.dataset.ext === target.id)?.scrollIntoView?.({ block: "center" });
    }
    const p = new URLSearchParams(search);
    p.delete(OPEN_SETTINGS_PARAM);
    setSearch(p, { replace: true });
  }, [openId, catalog.state, catalog.entries, wsId, roleKnown, canManage, search, setSearch, navigate, wsSlug]);

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
    if (e.kind === "theme") {
      // on = this theme paints the workspace; off = the workspace goes back to the instance theme
      const next = e.themeActive ? null : e.id;
      void run(e.id, async () => {
        await putWorkspaceTheme(wsId, next, {});
        if (wsSlug) void applyWorkspaceTheme(wsSlug);
      }, t(next ? "installed.themeOn" : "installed.themeOff", { name: e.name }));
      return;
    }
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
      <KindSections entries={rows} className="max-w-3xl">
        {(items) => (
          <ul className="space-y-3 max-w-3xl">
            {items.map((e) => (
              <Row key={e.id} entry={e} canManage={canManage} busy={busy === e.id}
                onToggle={toggle} onSettings={(x) => setDlg({ type: "settings", entry: x, wsId })}
                onTest={test} onRemove={(x) => setDlg({ type: "remove", entry: x, wsId })} />
            ))}
          </ul>
        )}
      </KindSections>

      {dlg?.type === "settings" && dlg.entry.kind === "theme" && (
        <ThemeSettingsDialog entry={dlg.entry} workspaceId={dlg.wsId} canManage={canManage}
          onClose={() => setDlg(null)} onSaved={catalog.refresh} />
      )}
      {dlg?.type === "settings" && dlg.entry.kind !== "theme" && (
        // the dialog reads the same list entry as the row, re-read after every check: one health for both
        <SettingsDialog entry={catalog.entries.find((x) => x.id === dlg.entry.id) ?? dlg.entry} workspaceId={dlg.wsId}
          onClose={() => setDlg(null)} onDone={() => { setDlg(null); catalog.refresh(); }}
          onCheck={() => void test(dlg.entry)} checking={busy === dlg.entry.id} />
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
