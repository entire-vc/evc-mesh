import { useCallback, useEffect, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/cn";
import { formatRelative } from "@/lib/utils";
import {
  controllerSync,
  fetchWorkspaceRuntime,
  findAgentBinding,
  poolObservations,
  preferredVsActual,
  rankRoutes,
  type RuntimeBinding,
  type RuntimeController,
  type RuntimeLoad,
  type RuntimeState,
} from "@/lib/agent-runtime";
import type { Agent } from "@/types";

type TabId = "profile" | "access" | "execution" | "state";

const TABS: { id: TabId; label: string }[] = [
  { id: "profile", label: "Profile" },
  { id: "access", label: "Workspace access" },
  { id: "execution", label: "Execution & routes" },
  { id: "state", label: "Observed state" },
];

interface AgentRuntimeTabsProps {
  agent: Agent;
  workspaceId: string | null;
  /** Existing profile editor content, rendered unchanged in the first tab. */
  profile: ReactNode;
  /** Existing workspace-grant list, rendered in the access tab. */
  access: ReactNode;
}

export function AgentRuntimeTabs({ agent, workspaceId, profile, access }: AgentRuntimeTabsProps) {
  const [tab, setTab] = useState<TabId>("profile");
  const [load, setLoad] = useState<RuntimeLoad | "loading">("loading");
  const requestId = useRef(0);
  const tabRefs = useRef<Record<string, HTMLButtonElement | null>>({});

  const reload = useCallback(() => {
    const id = ++requestId.current;
    if (!workspaceId) {
      setLoad({ kind: "unavailable" });
      return;
    }
    setLoad("loading");
    void fetchWorkspaceRuntime(workspaceId).then((result) => {
      // A slower response for a previous agent/workspace must not overwrite a newer one.
      if (requestId.current === id) setLoad(result);
    });
  }, [workspaceId]);

  useEffect(() => {
    reload();
    return () => {
      requestId.current++;
    };
  }, [reload, agent.id]);

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const i = TABS.findIndex((t) => t.id === tab);
    let next = i;
    if (e.key === "ArrowRight") next = (i + 1) % TABS.length;
    else if (e.key === "ArrowLeft") next = (i - 1 + TABS.length) % TABS.length;
    else return;
    const target = TABS[next];
    if (!target) return;
    e.preventDefault();
    setTab(target.id);
    tabRefs.current[target.id]?.focus();
  };

  return (
    <div className="mt-4">
      <div
        role="tablist"
        aria-label="Agent sections"
        onKeyDown={onKeyDown}
        className="flex flex-wrap gap-x-1 border-b border-border"
      >
        {TABS.map((t) => (
          <button
            key={t.id}
            ref={(el) => {
              tabRefs.current[t.id] = el;
            }}
            role="tab"
            id={`agent-tab-${t.id}`}
            aria-selected={tab === t.id}
            aria-controls={`agent-panel-${t.id}`}
            tabIndex={tab === t.id ? 0 : -1}
            onClick={() => setTab(t.id)}
            className={cn(
              "border-b-2 px-3 py-2 text-sm font-medium",
              tab === t.id
                ? "border-primary text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            {t.label}
          </button>
        ))}
      </div>

      <div role="tabpanel" id={`agent-panel-${tab}`} aria-labelledby={`agent-tab-${tab}`} className="pt-4">
        {tab === "profile" && profile}
        {tab === "access" && (
          <div className="space-y-5">
            {access}
            <RuntimeSection load={load} onRetry={reload} agent={agent} workspaceId={workspaceId}>
              {(state, found) => <AdmissionBlock binding={found?.binding ?? null} state={state} />}
            </RuntimeSection>
          </div>
        )}
        {tab === "execution" && (
          <RuntimeSection load={load} onRetry={reload} agent={agent} workspaceId={workspaceId}>
            {(state, found) => <ExecutionBlock state={state} binding={found?.binding ?? null} />}
          </RuntimeSection>
        )}
        {tab === "state" && (
          <RuntimeSection load={load} onRetry={reload} agent={agent} workspaceId={workspaceId}>
            {(state) => <StateBlock state={state} />}
          </RuntimeSection>
        )}
      </div>
    </div>
  );
}

function RuntimeSection({
  load,
  onRetry,
  agent,
  workspaceId,
  children,
}: {
  load: RuntimeLoad | "loading";
  onRetry: () => void;
  agent: Agent;
  workspaceId: string | null;
  children: (state: RuntimeState, found: { ref: string; binding: RuntimeBinding } | null) => ReactNode;
}) {
  if (load === "loading") {
    return (
      <div aria-busy="true" aria-label="Loading runtime" className="space-y-2">
        <Skeleton className="h-20 w-full" />
      </div>
    );
  }
  if (load.kind === "unavailable") {
    return (
      <div role="alert" className="space-y-2 rounded-md border border-destructive/40 p-3">
        <p className="text-sm text-destructive">Runtime data could not be loaded. Nothing below is assumed.</p>
        <Button size="sm" variant="outline" onClick={onRetry}>
          Retry
        </Button>
      </div>
    );
  }
  if (load.kind === "forbidden") {
    return <Note>Runtime settings belong to the resource-owner workspace and are visible to its admins only.</Note>;
  }
  if (load.kind === "not_configured") {
    return <Note>No runtime connection in this workspace. The agent runs in direct mode; runtime facts are unknown.</Note>;
  }
  const found = workspaceId ? findAgentBinding(load.state.catalog, agent.id, workspaceId) : null;
  return <>{children(load.state, found)}</>;
}

function Note({ children }: { children: ReactNode }) {
  return (
    <p role="status" className="rounded-md border border-dashed border-border p-3 text-sm text-muted-foreground">
      {children}
    </p>
  );
}

function Heading({ children }: { children: ReactNode }) {
  return (
    <h3 className="mb-1.5 text-xs font-medium uppercase tracking-wider text-muted-foreground">{children}</h3>
  );
}

const NoBinding = () => <Note>This agent has no runtime binding in this workspace.</Note>;

function AdmissionBlock({ binding }: { binding: RuntimeBinding | null; state: RuntimeState }) {
  if (!binding) return <NoBinding />;
  return (
    <div>
      <Heading>Runtime admission</Heading>
      <div className="space-y-1.5 text-sm">
        <p>{binding.enabled ? "Binding enabled" : "Binding disabled"}</p>
        <p>Permitted profiles: {binding.permitted_profiles.join(", ")}</p>
      </div>
    </div>
  );
}

function ExecutionBlock({ state, binding }: { state: RuntimeState; binding: RuntimeBinding | null }) {
  if (!binding) return <NoBinding />;
  const { catalog } = state;
  const observed = poolObservations(state.controllers);
  const routes = rankRoutes(catalog, binding, { enabled: state.enabled, drainRequested: state.drainRequested }, observed);
  const prefs = preferredVsActual(catalog, binding, routes);
  const poolRefs = new Set<string>();
  for (const r of routes) {
    const acc = catalog.accounts[r.profile.account_ref];
    for (const p of acc?.quota_pools_by_mode[r.profile.execution_mode] ?? []) poolRefs.add(p);
  }
  const { qa } = binding.policy;

  return (
    <div className="grid gap-6 lg:grid-cols-2">
      <section className="lg:col-span-2">
        <Heading>Allowed routes, in order</Heading>
        {routes.length === 0 ? (
          <p className="text-sm text-muted-foreground">No permitted route.</p>
        ) : (
          <ol className="space-y-1.5">
            {routes.map((r) => (
              <li
                key={r.profileRef}
                className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border border-border px-2.5 py-1.5 text-sm"
              >
                <span className="w-5 text-muted-foreground">{r.rank}.</span>
                <Badge variant={r.role === "primary" ? "secondary" : "outline"} className="text-xs capitalize">
                  {r.role.replace("_", " ")}
                </Badge>
                <span className="font-medium">{r.profileRef}</span>
                <span className="text-muted-foreground">
                  {catalog.accounts[r.profile.account_ref]?.provider ?? "?"} · {r.profile.account_ref} · {r.profile.model} ·{" "}
                  {r.profile.execution_mode}
                </span>
                {r.unavailable && <span className="text-xs text-yellow-600">Unavailable: {r.unavailable}</span>}
              </li>
            ))}
          </ol>
        )}
      </section>

      <section>
        <Heading>Preferred vs actual account</Heading>
        {prefs.length === 0 ? (
          <p className="text-sm text-muted-foreground">No preferred account set.</p>
        ) : (
          <ul className="space-y-1.5 text-sm">
            {prefs.map((p) => (
              <li key={p.provider} className="rounded-md border border-border px-2.5 py-1.5">
                <span className="font-medium">{p.provider}</span> · wanted account: <b>{p.preferredAccount}</b> · projected route:{" "}
                <b>{p.actualAccount ?? "none"}</b>
                <div className="text-xs text-muted-foreground">{p.reason}</div>
              </li>
            ))}
          </ul>
        )}
        <p className="mt-1 text-xs text-muted-foreground">
          Soft preference: grants no access, overrides no grant, API opt-in, QA or pool. The projected route is the
          first usable one by policy and the latest pool observations, not a record of a live launch.
        </p>
        <p className="mt-1 text-xs text-muted-foreground">
          Owner-side view: the receiving workspace&apos;s own admission is not part of this data, so a route marked
          usable here is not proof that this workspace admits it.
        </p>
      </section>

      <section>
        <Heading>QA policy</Heading>
        <div className="space-y-1 text-sm">
          <p>
            Mode: <b>{qa.mode}</b>
          </p>
          <p>
            Quota-only fallback: <b>{qa.quota_fallback ? "on" : "off"}</b>
          </p>
          <p className="text-xs text-muted-foreground">
            Tied to the exact artifact revision; reviewer differs from every author.
          </p>
        </div>
      </section>

      <section className="lg:col-span-2">
        <Heading>Pools</Heading>
        {poolRefs.size === 0 ? (
          <p className="text-sm text-muted-foreground">No pools mapped.</p>
        ) : (
          <ul className="grid gap-1.5 text-sm sm:grid-cols-2">
            {[...poolRefs].map((ref) => {
              const pool = catalog.pools[ref];
              return (
                <li key={ref} className="rounded-md border border-border px-2.5 py-1.5">
                  <span className="font-medium">{ref}</span>
                  {pool ? (
                    <span className="text-muted-foreground">
                      {" "}
                      · max {pool.max_concurrency} · threshold {pool.threshold_ref ?? "not set"}
                      {observed[ref] && ` · ${observed[ref].state}${observed[ref].verified ? "" : " (unverified)"}`}
                      {pool.aliases.length > 0 && ` · aliases: ${pool.aliases.join(", ")}`}
                    </span>
                  ) : (
                    <span className="text-xs text-yellow-600"> · not in catalog</span>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </section>
    </div>
  );
}

function StateBlock({ state }: { state: RuntimeState }) {
  const entries = Object.entries(state.catalog.controllers);
  return (
    <div>
      <Heading>Desired revision</Heading>
      <p className="mb-4 text-sm">
        r{state.revision}
        {state.digest && <span className="text-muted-foreground"> · {state.digest.slice(0, 12)}</span>}
      </p>
      <Heading>Controllers</Heading>
      <ControllerList state={state} entries={entries} />
    </div>
  );
}

export function ControllerList({ state, entries }: { state: RuntimeState; entries: [string, RuntimeController][] }) {
  if (entries.length === 0) return <p className="text-sm text-muted-foreground">No controllers in the catalog.</p>;
  return (
    <ul className="space-y-1.5">
      {entries.map(([ref, ctl]) => {
        const status = state.controllers.find((c) => c.controller_ref === ref);
        const sync = controllerSync(status, state.revision, state.digest);
        return (
          <li
            key={ref}
            className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border border-border px-2.5 py-1.5 text-sm"
          >
            <span className="font-medium">{ref}</span>
            <Badge variant={sync.label === "applied" ? "secondary" : "outline"} className="text-xs">
              {sync.label}
            </Badge>
            <span className="text-muted-foreground">{sync.detail}</span>
            {!ctl.enabled && <span className="text-xs text-yellow-600">controller disabled</span>}
            {status?.received_at && (
              <span className="text-xs text-muted-foreground">heartbeat {formatRelative(status.received_at)}</span>
            )}
          </li>
        );
      })}
    </ul>
  );
}
