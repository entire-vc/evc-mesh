import { useCallback, useEffect, useState } from "react";
import { Plug } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { toast } from "@/components/ui/toast";
import { api } from "@/lib/api";
import { apiErrorMessage } from "@/lib/api-error";
import type { OAuthGrant } from "@/types";

function formatDate(iso: string): string {
  return new Date(iso).toLocaleDateString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
  });
}

/** Apps the signed-in user has let into a workspace through the OAuth consent
 *  screen, with a way to take each one back. Revoking stops the app's tokens at
 *  once; the connector agent it created stays in the workspace. */
export function ConnectedAppsPage() {
  const [grants, setGrants] = useState<OAuthGrant[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [pending, setPending] = useState<OAuthGrant | null>(null);
  const [revoking, setRevoking] = useState(false);

  const load = useCallback(() => {
    setLoadError(null);
    api<{ grants: OAuthGrant[] | null }>("/api/v1/oauth/grants")
      .then((res) => setGrants(res.grants ?? []))
      .catch((err) => setLoadError(apiErrorMessage(err, "Could not load connected apps.")));
  }, []);

  useEffect(load, [load]);

  const confirmRevoke = async () => {
    if (!pending) return;
    setRevoking(true);
    try {
      await api(`/api/v1/oauth/grants/${pending.id}`, { method: "DELETE" });
      setPending(null);
      // Mark it revoked now: if the reload below fails, the list must not keep
      // showing an app as active next to a "revoked" confirmation.
      const revokedAt = new Date().toISOString();
      setGrants((gs) =>
        (gs ?? []).map((g) => (g.id === pending.id ? { ...g, revoked_at: revokedAt } : g)),
      );
      toast.success(`Access revoked for ${pending.client_name}`);
      load();
    } catch (err) {
      toast.error(apiErrorMessage(err, "Could not revoke access."));
    } finally {
      setRevoking(false);
    }
  };

  const active = (grants ?? []).filter((g) => !g.revoked_at);
  const revoked = (grants ?? []).filter((g) => g.revoked_at);

  return (
    <div className="mx-auto w-full max-w-3xl space-y-6 p-4 sm:p-6">
      <div className="space-y-1">
        <h1 className="text-xl font-semibold">Connected apps</h1>
        <p className="text-sm text-muted-foreground">
          Apps you have allowed to work in your workspaces. Revoking access stops the
          app right away; the connector agent it created stays in the workspace.
        </p>
      </div>

      {loadError && (
        <p className="text-sm text-destructive" role="alert">
          {loadError}
        </p>
      )}

      {grants === null && !loadError && <Skeleton className="h-24 w-full" />}

      {grants !== null && active.length === 0 && (
        <Card>
          <CardContent className="flex items-center gap-3 p-6 text-sm text-muted-foreground">
            <Plug className="h-4 w-4 shrink-0" />
            No apps are connected.
          </CardContent>
        </Card>
      )}

      <div className="space-y-3">
        {active.map((g) => (
          <GrantRow key={g.id} grant={g} onRevoke={() => setPending(g)} />
        ))}
      </div>

      {revoked.length > 0 && (
        <div className="space-y-3">
          <h2 className="text-sm font-medium text-muted-foreground">Revoked</h2>
          {revoked.map((g) => (
            <GrantRow key={g.id} grant={g} />
          ))}
        </div>
      )}

      <ConfirmDialog
        open={pending !== null}
        onClose={() => setPending(null)}
        onConfirm={confirmRevoke}
        title="Revoke access?"
        description={
          pending
            ? `${pending.client_name} will stop working in ${pending.workspace.name} immediately. To connect it again, it has to ask for access again.`
            : ""
        }
        confirmText="Revoke access"
        variant="destructive"
        isLoading={revoking}
      />
    </div>
  );
}

function GrantRow({ grant, onRevoke }: { grant: OAuthGrant; onRevoke?: () => void }) {
  const isRevoked = Boolean(grant.revoked_at);
  return (
    <Card className={isRevoked ? "opacity-60" : undefined}>
      <CardContent className="flex flex-col gap-3 p-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <p className="break-words font-medium">{grant.client_name}</p>
            {isRevoked && <Badge variant="outline">Revoked</Badge>}
          </div>
          <p className="text-sm text-muted-foreground">
            Workspace {grant.workspace.name} · acts as {grant.agent_name}
          </p>
          <p className="text-xs text-muted-foreground">
            {isRevoked
              ? `Revoked ${formatDate(grant.revoked_at as string)}`
              : `Connected ${formatDate(grant.created_at)}`}
          </p>
        </div>
        {!isRevoked && onRevoke && (
          <Button variant="outline" size="sm" onClick={onRevoke}>
            Revoke access
          </Button>
        )}
      </CardContent>
    </Card>
  );
}
