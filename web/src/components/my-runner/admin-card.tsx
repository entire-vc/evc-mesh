import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "@/components/ui/toast";
import { useExtensionCatalog } from "@/hooks/useExtensionCatalog";
import { apiErrorMessage } from "@/lib/api-error";
import { setWorkspaceExtensionEnabled } from "@/lib/extension-settings";
import { useWorkspaceStore } from "@/stores/workspace";
import { isWorkspaceAdmin, useWorkspaceRole } from "@/components/my-runner/use-workspace-role";
import { RUNNER_SERVER_ID } from "@/lib/runner-product";

export const RUNNER_EXTENSION_ID = RUNNER_SERVER_ID;

/**
 * The one place Runner is switched on or off for the workspace. The extension
 * route also writes the catalog's enabled flag, which is what the controllers
 * drain on. Shown to workspace owners and admins only.
 */
export function RunnerAdminCard() {
  const { t } = useTranslation("runner");
  const { currentWorkspace } = useWorkspaceStore();
  const catalog = useExtensionCatalog();
  const [busy, setBusy] = useState(false);
  const [confirmOff, setConfirmOff] = useState(false);
  const wsId = currentWorkspace?.id;

  const myRole = useWorkspaceRole(wsId);
  if (!isWorkspaceAdmin(myRole)) return null;
  const entry = catalog.entries.find((e) => e.id === RUNNER_EXTENSION_ID);
  const on = Boolean(entry?.status?.enabled);

  const turn = async (next: boolean) => {
    if (!wsId) return;
    setBusy(true);
    try {
      // confirm: the inline question was already answered, also for an instance-wide Runner.
      await setWorkspaceExtensionEnabled(wsId, RUNNER_EXTENSION_ID, next, true);
      toast.success(t(next ? "admin.enabled" : "admin.disabled"));
      setConfirmOff(false);
      catalog.refresh();
    } catch (e) {
      toast.error(apiErrorMessage(e, t(next ? "admin.failed" : "admin.disableFailed")));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card data-testid="my-runner-admin">
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-2 text-base">
          {t("admin.title")}
          {catalog.state === "ready" && entry && <Badge variant={on ? "success" : "outline"}>{t(on ? "admin.enabled" : "admin.off")}</Badge>}
        </CardTitle>
        {/* No hint until the status is known: a loading card must not claim Runner is off. */}
        {catalog.state === "ready" && entry && <CardDescription>{on ? t("admin.onHint") : t("admin.offHint")}</CardDescription>}
      </CardHeader>
      <CardContent>
        {catalog.state === "loading" && <Skeleton className="h-9 w-40" />}
        {catalog.state === "error" && (
          <div role="alert" className="flex flex-wrap items-center gap-3 text-sm text-destructive">
            <span>{t("loadFailed")}</span>
            <Button size="sm" variant="outline" onClick={catalog.reload}>{t("retry")}</Button>
          </div>
        )}
        {catalog.state === "ready" && entry && !on && (
          <Button disabled={busy} onClick={() => void turn(true)}>{t(busy ? "admin.enabling" : "admin.enable")}</Button>
        )}
        {catalog.state === "ready" && entry && on && !confirmOff && (
          <Button variant="outline" disabled={busy} onClick={() => setConfirmOff(true)}>{t("admin.disable")}</Button>
        )}
        {catalog.state === "ready" && entry && on && confirmOff && (
          // Inline, not a modal: switching off drains every runner of the workspace.
          <div role="group" aria-label={t("admin.disable")} className="space-y-3 rounded-lg border border-warning/50 p-3" data-testid="my-runner-disable-confirm">
            <p className="text-sm">{t("admin.disableConfirm")}</p>
            <div className="flex flex-wrap gap-2">
              <Button size="sm" variant="destructive" disabled={busy} onClick={() => void turn(false)}>
                {t(busy ? "admin.disabling" : "admin.disableYes")}
              </Button>
              <Button size="sm" variant="outline" disabled={busy} onClick={() => setConfirmOff(false)}>{t("admin.cancel")}</Button>
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
