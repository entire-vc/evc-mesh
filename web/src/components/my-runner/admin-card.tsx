import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "@/components/ui/toast";
import { useExtensionCatalog } from "@/hooks/useExtensionCatalog";
import { api, ApiRequestError } from "@/lib/api";
import { apiErrorMessage } from "@/lib/api-error";
import { setWorkspaceExtensionEnabled } from "@/lib/extension-settings";
import { useWorkspaceStore } from "@/stores/workspace";

export const RUNNER_EXTENSION_ID = "runner";

/**
 * The workspace admin's whole part in Runner: one button that turns the
 * extension on. Everything else is the operator's, on this page. Shown to
 * workspace owners and admins only.
 */
export function RunnerAdminCard() {
  const { t } = useTranslation("runner");
  const { currentWorkspace } = useWorkspaceStore();
  const catalog = useExtensionCatalog();
  const [busy, setBusy] = useState(false);
  const wsId = currentWorkspace?.id;

  // The shell does not load the role and the shared store keeps whichever response landed
  // last, so the card asks for its own workspace's role and keeps it in local state, keyed
  // by workspace: a late answer for another workspace can never unlock this one.
  const [role, setRole] = useState<{ ws: string; role: string | null } | null>(null);
  useEffect(() => {
    if (!wsId) return;
    let live = true;
    setRole(null);
    api<{ role: string }>(`/api/v1/workspaces/${wsId}/members/me`)
      .then((me) => me?.role ?? null, () => null)
      .then((r) => { if (live) setRole({ ws: wsId, role: r }); });
    return () => { live = false; };
  }, [wsId]);

  const myRole = role && role.ws === wsId ? role.role : null;
  if (myRole !== "owner" && myRole !== "admin") return null;
  const entry = catalog.entries.find((e) => e.id === RUNNER_EXTENSION_ID);
  const on = Boolean(entry?.status?.enabled);

  const enable = async () => {
    if (!wsId) return;
    setBusy(true);
    try {
      await setWorkspaceExtensionEnabled(wsId, RUNNER_EXTENSION_ID, true);
      toast.success(t("admin.enabled"));
      catalog.refresh();
    } catch (e) {
      // 404: the workspace has no Runner connection row yet, which the enable route cannot create.
      toast.error(e instanceof ApiRequestError && e.status === 404 ? t("admin.notConnected") : apiErrorMessage(e, t("admin.failed")));
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
        <CardDescription>{on ? t("admin.onHint") : t("admin.offHint")}</CardDescription>
      </CardHeader>
      {!on && (
      <CardContent>
        {catalog.state === "loading" && <Skeleton className="h-9 w-40" />}
        {catalog.state === "error" && (
          <div role="alert" className="flex flex-wrap items-center gap-3 text-sm text-destructive">
            <span>{t("loadFailed")}</span>
            <Button size="sm" variant="outline" onClick={catalog.reload}>{t("retry")}</Button>
          </div>
        )}
        {catalog.state === "ready" && entry && !on && (
          <Button disabled={busy} onClick={() => void enable()}>{t(busy ? "admin.enabling" : "admin.enable")}</Button>
        )}
      </CardContent>
      )}
    </Card>
  );
}
