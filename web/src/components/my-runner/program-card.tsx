import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "@/components/ui/toast";
import { isWorkspaceAdmin, useWorkspaceRole } from "@/components/my-runner/use-workspace-role";
import { useExtensionCatalog, type CatalogEntry } from "@/hooks/useExtensionCatalog";
import { apiErrorMessage } from "@/lib/api-error";
import { allowedDomains, capabilityText, consentTokens, describeCapabilities, domainText } from "@/lib/extension-capabilities";
import { connectWorkspaceExtension } from "@/lib/extension-connect";
import { listRepoUpdates, type RepoUpdate } from "@/lib/extension-repo";
import { saveWorkspaceExtensionSettings } from "@/lib/extension-settings";
import { cn } from "@/lib/cn";
import { RUNNER_PRODUCT_ID, RUNNER_URL_SETTING } from "@/lib/runner-product";
import { useWorkspaceStore } from "@/stores/workspace";

const validUrl = (v: string) => {
  try {
    const u = new URL(v);
    return u.protocol === "http:" || u.protocol === "https:";
  } catch {
    return false;
  }
};

/**
 * The runner program (catalog entry `agent-runner`) on the Runner screen: it is
 * installed from the catalog, connected here by the address Mesh reaches it at,
 * switched on or off here, and its version and pending update are shown here.
 * The extension screens only link to this card. Workspace owners and admins only.
 */
export function RunnerProgramCard() {
  const { t } = useTranslation("runner");
  const wsId = useWorkspaceStore((s) => s.currentWorkspace?.id);
  const role = useWorkspaceRole(wsId);
  const catalog = useExtensionCatalog();
  if (!isWorkspaceAdmin(role)) return null;
  const entry = catalog.entries.find((e) => e.id === RUNNER_PRODUCT_ID);

  return (
    <Card data-testid="my-runner-program">
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-2 text-base">
          {t("program.title")}
          {entry?.installed && entry.version && <span className="text-xs font-normal text-muted-foreground">{t("program.version", { version: entry.version })}</span>}
        </CardTitle>
        {entry?.connection && <ProgramHealth entry={entry} />}
      </CardHeader>
      <CardContent className="space-y-4">
        {catalog.state === "loading" && <Skeleton className="h-9 w-64" />}
        {catalog.state === "error" && (
          <div role="alert" className="flex flex-wrap items-center gap-3 text-sm text-destructive">
            <span>{t("loadFailed")}</span>
            <Button size="sm" variant="outline" onClick={catalog.reload}>{t("retry")}</Button>
          </div>
        )}
        {catalog.state === "ready" && <ProgramBody entry={entry} wsId={wsId} onChange={catalog.refresh} />}
      </CardContent>
    </Card>
  );
}

function ProgramHealth({ entry }: { entry: CatalogEntry }) {
  const { t } = useTranslation("runner");
  const health = entry.status?.health;
  const key = health ? (health.ok ? "ok" : "bad") : "unknown";
  return (
    <CardDescription className="flex items-center gap-2" data-testid="my-runner-program-health">
      <span aria-hidden className={cn("h-2.5 w-2.5 shrink-0 rounded-full", health ? (health.ok ? "bg-success" : "bg-destructive") : "bg-muted-foreground/40")} />
      <span>{t(`program.health.${key}`)}</span>
      {health && !health.ok && health.message && <span className="min-w-0 break-words">{health.message}</span>}
    </CardDescription>
  );
}

function ProgramBody({ entry, wsId, onChange }: { entry: CatalogEntry | undefined; wsId: string | undefined; onChange: () => void }) {
  const { t } = useTranslation("runner");
  const wsSlug = useWorkspaceStore((s) => s.currentWorkspace?.slug) ?? "";
  if (!entry || !entry.installed) {
    return (
      <div className="flex flex-wrap items-center gap-3 text-sm" data-testid="my-runner-program-missing">
        <span className="text-muted-foreground">{t("program.notInstalled")}</span>
        {entry && <Link to={`/w/${wsSlug}/extensions/${RUNNER_PRODUCT_ID}`} className="font-medium text-primary hover:underline">{t("program.openCatalog")}</Link>}
      </div>
    );
  }
  // A blocked program cannot be edited; a disallowed one cannot be connected, but an existing
  // connection stays editable. Runner is switched on and off by the one switch of the admin card.
  // The pending update is shown either way.
  const restricted = entry.blocked || !entry.allowed;
  return (
    <>
      {restricted && (
        <p className="text-sm text-muted-foreground" data-testid="my-runner-program-not-allowed">{entry.blocked ? entry.blockedReason : t("program.notAllowed")}</p>
      )}
      {!entry.blocked && (entry.allowed || entry.connection) && <AddressForm entry={entry} wsId={wsId} onChange={onChange} />}
      <ProgramUpdate wsSlug={wsSlug} />
    </>
  );
}

function AddressForm({ entry, wsId, onChange }: { entry: CatalogEntry; wsId: string | undefined; onChange: () => void }) {
  const { t } = useTranslation("runner");
  const { t: ti } = useTranslation("integrations");
  const saved = String(entry.connection?.settings?.[RUNNER_URL_SETTING] ?? "");
  const [url, setUrl] = useState(saved);
  const [consented, setConsented] = useState(false);
  const [busy, setBusy] = useState(false);
  const [touched, setTouched] = useState(false);
  useEffect(() => { setUrl(saved); }, [saved]);

  const first = !entry.connection;
  const lines = describeCapabilities(entry.manifest.capabilities);
  const domains = allowedDomains(entry.manifest.capabilities);
  const bad = touched && !validUrl(url.trim());
  const submit = async () => {
    setTouched(true);
    if (!wsId || !validUrl(url.trim())) return;
    setBusy(true);
    try {
      const settings = { ...(entry.connection?.settings ?? {}), [RUNNER_URL_SETTING]: url.trim() };
      if (first) await connectWorkspaceExtension(wsId, entry.id, settings, consentTokens(entry.manifest.capabilities));
      else await saveWorkspaceExtensionSettings(wsId, entry.id, settings);
      toast.success(t(first ? "program.connected" : "program.saved"));
      onChange();
    } catch (e) {
      toast.error(apiErrorMessage(e, t("program.failed")));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form noValidate className="space-y-3" onSubmit={(e) => { e.preventDefault(); void submit(); }} data-testid="my-runner-program-address">
      {first && <p className="text-sm text-muted-foreground">{t("program.connectHint")}</p>}
      <div className="max-w-xl space-y-1.5">
        <label htmlFor="runner-url" className="text-sm font-medium">{t("program.url")}</label>
        <Input
          id="runner-url" type="text" inputMode="url" value={url} placeholder={t("program.urlPlaceholder")}
          aria-invalid={bad || undefined} aria-describedby={bad ? "runner-url-error" : undefined}
          onChange={(e) => setUrl(e.target.value)} onBlur={() => setTouched(true)}
        />
        {bad && <p id="runner-url-error" role="alert" className="text-sm text-destructive">{t("program.invalidUrl")}</p>}
      </div>
      {/* As in the connect wizard: the first connect always asks for consent, and the domains are part of it. */}
      {first && (
        <div className="max-w-xl space-y-2 rounded-lg border border-border p-3">
          {lines.length === 0 && domains.length === 0 ? (
            <p className="text-sm text-muted-foreground">{ti("detail.permissions.none")}</p>
          ) : (
            <ul className="space-y-1" aria-label={ti("detail.tabs.permissions")}>
              {lines.map((l) => (
                <li key={`${l.group}:${l.raw}`} className="flex items-start gap-2 text-sm">
                  <span className="mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full bg-primary" aria-hidden />
                  <span>{capabilityText(ti, l)}</span>
                </li>
              ))}
            </ul>
          )}
          {domains.length > 0 && (
            <div>
              <p className="mb-1 text-sm font-medium">{ti("detail.permissions.domains")}</p>
              <ul className="flex flex-wrap gap-2" data-testid="my-runner-program-domains">
                {domains.map((d) => <li key={d}><code className="rounded bg-muted px-2 py-0.5 text-xs">{domainText(ti, d)}</code></li>)}
              </ul>
            </div>
          )}
          <label className="flex cursor-pointer items-start gap-2 text-sm">
            <input type="checkbox" className="mt-0.5" checked={consented} onChange={(e) => setConsented(e.target.checked)} />
            <span>{ti("connect.permissions.consent")}</span>
          </label>
        </div>
      )}
      <Button type="submit" disabled={busy || (first && !consented) || (!first && url.trim() === saved)}>
        {first ? t(busy ? "program.connecting" : "program.connect") : t(busy ? "program.saving" : "program.save")}
      </Button>
    </form>
  );
}

/**
 * A newer catalog release of the runner program. The list is the catalog's
 * Updates (instance administrators only): for anyone else it is refused and
 * nothing is shown; the update itself is applied there, as for every extension.
 */
function ProgramUpdate({ wsSlug }: { wsSlug: string }) {
  const { t } = useTranslation("runner");
  const [update, setUpdate] = useState<RepoUpdate | null>(null);
  useEffect(() => {
    let live = true;
    listRepoUpdates()
      .then((r) => (r.updates ?? []).find((u) => u.extension_id === RUNNER_PRODUCT_ID && u.update_available) ?? null, () => null)
      .then((u) => { if (live) setUpdate(u); });
    return () => { live = false; };
  }, []);
  if (!update) return null;
  return (
    <div className="flex flex-wrap items-center gap-3 text-sm" data-testid="my-runner-program-update">
      <Badge variant="warning">{t("program.update", { version: update.latest_version ?? update.latest_ref ?? "" })}</Badge>
      <Link to={`/w/${wsSlug}/extensions?tab=updates`} className="font-medium text-primary hover:underline">{t("program.updates")}</Link>
    </div>
  );
}
