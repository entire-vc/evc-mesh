import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ShieldAlert } from "lucide-react";
import { Button } from "@/components/ui/button";
import { toast } from "@/components/ui/toast";
import type { CatalogEntry } from "@/hooks/useExtensionCatalog";
import { capabilitiesFromTokens, capabilityText, describeCapabilities } from "@/lib/extension-capabilities";
import { consentWorkspaceExtension } from "@/lib/extension-settings";

/**
 * Rights the installed manifest declares beyond what the workspace consented to. They do not act until an
 * owner or admin agrees; a member sees the same list without the action. The server names the version
 * (409 `version_changed` when it moved meanwhile), so the list is re-read, never agreed blindly.
 */
export function ConsentNotice({ entry, workspaceId, canManage, onChanged }: {
  entry: CatalogEntry; workspaceId: string; canManage: boolean; onChanged: () => void;
}) {
  const { t } = useTranslation("integrations");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<"changed" | "failed" | null>(null);
  const pending = entry.connection?.pending_consent;
  if (!pending) return null;

  const lines = describeCapabilities(capabilitiesFromTokens(pending.added_capabilities, pending.added_events));
  // a record exists iff it has a time; its version and commit may be empty on an older or backfilled consent
  const first = !entry.connection?.consented_at;

  const agree = async () => {
    setBusy(true);
    setError(null);
    try {
      await consentWorkspaceExtension(workspaceId, entry.id, pending.version, pending.commit);
      toast.success(t("installed.consentNotice.agreed", { name: entry.name, version: pending.version }));
      onChanged();
    } catch (e) {
      const conflict = (e as { status?: number } | null)?.status === 409;
      setError(conflict ? "changed" : "failed");
      if (conflict) onChanged();
    } finally {
      setBusy(false);
    }
  };

  return (
    <section
      role="region" aria-label={t("installed.consentNotice.title", { name: entry.name, version: pending.version })}
      data-testid={`consent-notice-${entry.id}`}
      className="mt-3 w-full space-y-2 rounded-lg border border-warning/40 bg-warning/10 p-3"
    >
      <h3 className="flex items-start gap-2 text-sm font-semibold">
        <ShieldAlert className="mt-0.5 h-4 w-4 shrink-0" aria-hidden />
        <span className="min-w-0 break-words">{t("installed.consentNotice.title", { name: entry.name, version: pending.version })}</span>
      </h3>
      <p className="text-sm text-muted-foreground">
        {t(first ? "installed.consentNotice.needsBody" : "installed.consentNotice.body", { name: entry.name })}
      </p>
      <div>
        <p className="mb-1 text-xs font-medium uppercase tracking-wide text-muted-foreground">{t("installed.consentNotice.added")}</p>
        <ul className="space-y-1" aria-label={t("installed.consentNotice.added")}>
          {lines.map((l) => (
            <li key={`${l.group}:${l.raw}`} className="flex items-start gap-2 text-sm">
              <span className="mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full bg-primary" aria-hidden />
              <span className="min-w-0 break-words">{capabilityText(t, l)}</span>
            </li>
          ))}
        </ul>
      </div>
      {error && <p role="alert" className="text-sm text-destructive">{t(`installed.consentNotice.${error}`)}</p>}
      {canManage ? (
        <Button size="sm" disabled={busy} onClick={() => void agree()}>
          {t("installed.consentNotice.agree", { version: pending.version })}
        </Button>
      ) : (
        <p className="text-sm text-muted-foreground">{t("installed.consentNotice.adminOnly")}</p>
      )}
    </section>
  );
}
