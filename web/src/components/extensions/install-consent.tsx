import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { AlertTriangle } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { toast } from "@/components/ui/toast";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { KindTrustBadges } from "@/components/extensions/catalog";
import { apiErrorMessage } from "@/lib/api-error";
import {
  allowedDomains, capabilityText, describeCapabilities, domainText, type ManifestCapabilities,
} from "@/lib/extension-capabilities";
import { shortSha } from "@/lib/extension-repo";
import type { CatalogEntry } from "@/hooks/useExtensionCatalog";

/** What an administrator agrees to before an install: who publishes it, which commit, which rights. */
export interface ConsentSubject {
  name: string;
  version: string;
  publisher: string;
  commitSha?: string;
  description?: string;
  capabilities?: ManifestCapabilities;
}

/**
 * The consent block both install paths show (repository and catalog): name, version,
 * publisher, commit, and the same permissions and domains as the page's Permissions tab.
 */
export function ConsentSummary({ subject, badges, testId, children }: {
  subject: ConsentSubject;
  badges?: ReactNode;
  testId: string;
  children?: ReactNode;
}) {
  const { t } = useTranslation("integrations");
  const caps = subject.capabilities;
  const lines = describeCapabilities(caps);
  const domains = allowedDomains(caps);
  return (
    <section data-testid={testId} aria-label={t("repoInstall.manifest")} className="space-y-3 rounded-lg border border-border p-3">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="min-w-0 break-words text-sm font-semibold">{subject.name}</h3>
        <Badge variant="outline">{subject.version}</Badge>
        {badges}
      </div>
      {subject.description && <p className="text-sm text-muted-foreground">{subject.description}</p>}
      <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs text-muted-foreground">
        <dt>{t("repoInstall.publisher")}</dt><dd className="min-w-0 break-words">{subject.publisher || t("installConsent.unknown")}</dd>
        <dt>{t("repoInstall.commit")}</dt>
        <dd className="font-mono" title={subject.commitSha || undefined}>{subject.commitSha ? shortSha(subject.commitSha) : t("installConsent.unknown")}</dd>
      </dl>
      <div>
        <p className="mb-1 text-sm font-medium">{t("repoInstall.permissions")}</p>
        {lines.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("repoInstall.noPermissions")}</p>
        ) : (
          <ul className="list-disc space-y-0.5 pl-5 text-sm">
            {lines.map((l) => <li key={`${l.group}:${l.raw}`} title={l.raw}>{capabilityText(t, l)}</li>)}
          </ul>
        )}
      </div>
      {domains.length > 0 && (
        <div>
          <p className="mb-1 text-sm font-medium">{t("detail.permissions.domains")}</p>
          <ul className="flex flex-wrap gap-2">
            {domains.map((d) => <li key={d}><code className="break-all rounded bg-muted px-2 py-0.5 text-xs">{domainText(t, d)}</code></li>)}
          </ul>
          {caps?.network?.reasoning && <p className="mt-2 text-sm text-muted-foreground">{caps.network.reasoning}</p>}
        </div>
      )}
      {children}
    </section>
  );
}

/** True when the entry declares any right or network domain the administrator should see first. */
export function declaresRights(caps: ManifestCapabilities | undefined): boolean {
  return describeCapabilities(caps).length > 0 || allowedDomains(caps).length > 0;
}

/**
 * Install from the catalog behind the same consent the repository install asks for.
 * Cancel sends nothing; Install calls `install` once and keeps the dialog open on an error.
 */
export function CatalogInstallDialog({ entry, install, onClose, onInstalled }: {
  entry: CatalogEntry;
  install: () => Promise<unknown>;
  onClose: () => void;
  onInstalled: () => void;
}) {
  const { t } = useTranslation("integrations");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const close = () => { if (!busy) onClose(); };

  const confirm = async () => {
    setBusy(true);
    setError("");
    try {
      await install();
      toast.success(t("detail.action.installed", { name: entry.name }));
      onInstalled();
      onClose();
    } catch (err) {
      setError(apiErrorMessage(err, t("repoInstall.installFailed")));
      setBusy(false);
    }
  };

  const title = t("installConsent.title", { name: entry.name });
  return (
    <Dialog open onOpenChange={(o) => !o && close()} className="max-w-xl">
      <DialogContent onClose={close} className="max-h-[90vh] overflow-y-auto" role="dialog" aria-label={title}>
        <DialogHeader>
          <DialogTitle className="break-words">{title}</DialogTitle>
          <DialogDescription>{t("installConsent.hint")}</DialogDescription>
        </DialogHeader>

        <div className="mt-4 space-y-4">
          <ConsentSummary
            testId="catalog-install-consent"
            subject={{
              name: entry.name, version: entry.version, publisher: entry.publisher,
              commitSha: entry.commitSha, capabilities: entry.manifest.capabilities,
            }}
            badges={<KindTrustBadges entry={entry} />}
          />
          {error && (
            <p role="alert" data-testid="catalog-install-error" className="flex items-start gap-2 text-sm text-destructive">
              <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden />
              {error}
            </p>
          )}
        </div>

        <DialogFooter className="mt-4">
          <Button variant="outline" disabled={busy} onClick={close}>{t("repoInstall.cancel")}</Button>
          <Button disabled={busy} onClick={() => void confirm()}>
            {busy ? t("repoInstall.installing") : t("repoInstall.install")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
