import { useCallback, useEffect, useRef, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { ControllerList } from "@/components/agent-runtime-tabs";
import { fetchWorkspaceRuntime, poolObservations, type RuntimeLoad, type RuntimeState } from "@/lib/agent-runtime";

// Read-only inventory of the workspace runtime: controllers, accounts, pools,
// desired vs applied revision. Credential references are never rendered.
export function RuntimeResourcesCard({ workspaceId }: { workspaceId: string }) {
  const [load, setLoad] = useState<RuntimeLoad | "loading">("loading");
  const requestId = useRef(0);

  const reload = useCallback(() => {
    const id = ++requestId.current;
    setLoad("loading");
    void fetchWorkspaceRuntime(workspaceId).then((r) => {
      if (requestId.current === id) setLoad(r);
    });
  }, [workspaceId]);

  useEffect(() => {
    reload();
    return () => {
      requestId.current++;
    };
  }, [reload]);

  return (
    <Card className="mt-4 max-w-4xl" aria-labelledby="runtime-resources-title">
      <CardHeader>
        <CardTitle id="runtime-resources-title" className="text-base">
          Controllers and resources
        </CardTitle>
      </CardHeader>
      <CardContent>
        {load === "loading" && (
          <div aria-busy="true" aria-label="Loading runtime">
            <Skeleton className="h-24 w-full" />
          </div>
        )}
        {load !== "loading" && load.kind === "unavailable" && (
          <div role="alert" className="space-y-2">
            <p className="text-sm text-destructive">Runtime data could not be loaded. Nothing below is assumed.</p>
            <Button size="sm" variant="outline" onClick={reload}>
              Retry
            </Button>
          </div>
        )}
        {load !== "loading" && load.kind === "forbidden" && (
          <p role="status" className="text-sm text-muted-foreground">
            Runtime settings are visible to workspace admins only.
          </p>
        )}
        {load !== "loading" && load.kind === "not_configured" && (
          <p role="status" className="text-sm text-muted-foreground">
            No runtime connection in this workspace. Agents run in direct mode; runtime facts are unknown.
          </p>
        )}
        {load !== "loading" && load.kind === "ok" && <Inventory state={load.state} />}
      </CardContent>
    </Card>
  );
}

function H({ children }: { children: string }) {
  return <h3 className="mb-1.5 text-xs font-medium uppercase tracking-wider text-muted-foreground">{children}</h3>;
}

function Inventory({ state }: { state: RuntimeState }) {
  const { catalog } = state;
  const controllers = Object.entries(catalog.controllers);
  const accounts = Object.entries(catalog.accounts);
  const pools = Object.entries(catalog.pools);
  const observed = poolObservations(state.controllers);
  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <span>
          Desired r{state.revision}
          {state.digest && <span className="text-muted-foreground"> · {state.digest.slice(0, 12)}</span>}
        </span>
        <Badge variant={state.enabled ? "secondary" : "outline"} className="text-xs">
          {state.enabled ? "enabled" : "disabled"}
        </Badge>
        {state.drainRequested && <Badge variant="outline" className="text-xs">draining</Badge>}
      </div>

      <section>
        <H>Controllers</H>
        <ControllerList state={state} entries={controllers} />
        {controllers.length > 0 && (
          <ul className="mt-1.5 space-y-0.5 text-xs text-muted-foreground">
            {controllers.map(([ref, c]) => (
              <li key={ref}>
                {ref}: {c.host} · {c.version}
                {c.capabilities.length > 0 && ` · ${c.capabilities.join(", ")}`}
              </li>
            ))}
          </ul>
        )}
      </section>

      <section>
        <H>Accounts</H>
        {accounts.length === 0 ? (
          <p className="text-sm text-muted-foreground">No accounts in the catalog.</p>
        ) : (
          <ul className="space-y-1.5 text-sm">
            {accounts.map(([ref, a]) => (
              <li key={ref} className="rounded-md border border-border px-2.5 py-1.5">
                <span className="font-medium">{ref}</span>
                <span className="text-muted-foreground">
                  {" "}
                  · {a.provider}
                  {Object.entries(a.quota_pools_by_mode).map(([mode, p]) => ` · ${mode}: ${(p ?? []).join(", ")}`)}
                </span>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section>
        <H>Pools</H>
        {pools.length === 0 ? (
          <p className="text-sm text-muted-foreground">No pools in the catalog.</p>
        ) : (
          <ul className="grid gap-1.5 text-sm sm:grid-cols-2">
            {pools.map(([ref, p]) => {
              const o = observed[ref];
              return (
                <li key={ref} className="rounded-md border border-border px-2.5 py-1.5">
                  <span className="font-medium">{ref}</span>
                  <span className="text-muted-foreground">
                    {" "}
                    · {p.provider} · max {p.max_concurrency} · threshold {p.threshold_ref ?? "not set"}
                    {p.aliases.length > 0 && ` · aliases: ${p.aliases.join(", ")}`}
                    {o ? ` · ${o.state}${o.verified ? "" : " (unverified)"}` : " · no observation"}
                  </span>
                </li>
              );
            })}
          </ul>
        )}
      </section>
    </div>
  );
}
