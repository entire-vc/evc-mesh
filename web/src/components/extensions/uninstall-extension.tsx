import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { toast } from "@/components/ui/toast";
import { apiErrorMessage } from "@/lib/api-error";
import { uninstallExtension } from "@/lib/extension-instance";
import { deleteTheme } from "@/lib/themes";
import { isRunnerProduct } from "@/lib/runner-product";
import type { CatalogEntry } from "@/hooks/useExtensionCatalog";

/** Parts of Mesh shipped as extensions that are switched off, never removed. */
const SYSTEM_PARTS = new Set(["runner", "dashboards"]);

/**
 * Whether the instance administrator may remove this entry from the instance: an installed
 * catalog or repository extension (or theme). A built-in is switched off instead.
 */
export function canUninstall(entry: Pick<CatalogEntry, "id" | "installed" | "source">, isInstanceAdmin: boolean): boolean {
  return isInstanceAdmin && entry.installed && (entry.source === "catalog" || entry.source === "repo")
    && !SYSTEM_PARTS.has(entry.id) && !isRunnerProduct(entry.id);
}

/**
 * «Uninstall» on the extension page (D2): asks for confirmation, then removes the extension
 * from the instance. Its data is kept; a theme goes through the theme removal, which also
 * resets the workspaces that chose it.
 */
export function UninstallExtensionButton({ entry, isInstanceAdmin, onUninstalled }: {
  entry: Pick<CatalogEntry, "id" | "name" | "kind" | "installed" | "source">;
  isInstanceAdmin: boolean;
  onUninstalled: () => void;
}) {
  const { t } = useTranslation("integrations");
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  if (!canUninstall(entry, isInstanceAdmin)) return null;
  const theme = entry.kind === "theme";

  const confirm = () => {
    setBusy(true);
    (theme ? deleteTheme(entry.id) : uninstallExtension(entry.id))
      .then(() => {
        toast.success(t("detail.action.uninstalled", { name: entry.name }));
        setOpen(false);
        onUninstalled();
      })
      .catch((e) => toast.error(apiErrorMessage(e, t("installed.actionFailed"))))
      .finally(() => setBusy(false));
  };

  return (
    <>
      <Button variant="outline" className="text-destructive hover:text-destructive" disabled={busy} onClick={() => setOpen(true)}>
        <Trash2 className="mr-1.5 h-4 w-4" aria-hidden />
        {busy ? t("detail.action.uninstalling") : t("detail.action.uninstall")}
      </Button>
      <ConfirmDialog
        open={open}
        onClose={() => !busy && setOpen(false)}
        onConfirm={confirm}
        title={t("detail.action.uninstallTitle", { name: entry.name })}
        description={t(theme ? "detail.action.uninstallThemeBody" : "detail.action.uninstallBody")}
        confirmText={t("detail.action.uninstall")}
        variant="destructive"
        isLoading={busy}
      />
    </>
  );
}
