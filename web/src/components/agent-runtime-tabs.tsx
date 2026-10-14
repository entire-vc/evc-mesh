import { useCallback, useEffect, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { Trans, useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/cn";
import { formatRelative } from "@/lib/utils";
import { PolicyEditor } from "@/components/agent-runtime-policy-editor";
import {
  controllerSync,
  fetchWorkspaceRuntime,
  findAgentBinding,
  poolObservations,
  preferredVsActual,
  previewBinding,
  type PreviewResult,
  rankRoutes,
  type RuntimeBinding,
  type RuntimeController,
  type RuntimeLoad,
  type RuntimeState,
} from "@/lib/agent-runtime";
import type { Agent } from "@/types";

type TabId = "profile" | "access" | "execution" | "state";

const TABS: { id: TabId }[] = [{ id: "profile" }, { id: "access" }, { id: "execution" }, { id: "state" }];

interface AgentRuntimeTabsProps {
  /** Only the id is read: a foreign (invited) agent has no home profile here. */
  agent: Pick<Agent, "id">;
  workspaceId: string | null;
  /** Existing profile editor content, rendered unchanged in the first tab. */
  profile: ReactNode;
  /** Existing workspace-grant list, rendered in the access tab. */
  access: ReactNode;
}

export function AgentRuntimeTabs({ agent, workspaceId, profile, access }: AgentRuntimeTabsProps) {
  const { t } = useTranslation("integrations");
  const [tab, setTab] = useState<TabId>("profile");
  const [load, setLoad] = useState<RuntimeLoad | "loading">("loading");
  const requestId = useRef(0);
  const tabRefs = useRef<Record<string, HTMLButtonElement | null>>({});

  const reload = useCallback((quiet = false) => {
    const id = ++requestId.current;
    if (!workspaceId) {
      setLoad({ kind: "unavailable" });
      return;
    }
    // quiet: refresh behind an open editor without unmounting it.
    if (!quiet) setLoad("loading");
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
    const i = TABS.findIndex((x) => x.id === tab);
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
        aria-label={t("runtimeTabs.sectionsLabel")}
        onKeyDown={onKeyDown}
        className="flex flex-wrap gap-x-1 border-b border-border"
      >
        {TABS.map((tb) => (
          <button
            key={tb.id}
            ref={(el) => {
              tabRefs.current[tb.id] = el;
            }}
            role="tab"
            id={`agent-tab-${tb.id}`}
            aria-selected={tab === tb.id}
            aria-controls={`agent-panel-${tb.id}`}
            tabIndex={tab === tb.id ? 0 : -1}
            onClick={() => setTab(tb.id)}
            className={cn(
              "border-b-2 px-3 py-2 text-sm font-medium",
              tab === tb.id
                ? "border-primary text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            {t(`runtimeTabs.tabs.${tb.id}`)}
          </button>
        ))}
      </div>

      <div role="tabpanel" id={`agent-panel-${tab}`} aria-labelledby={`agent-tab-${tab}`} className="pt-4">
        {tab === "profile" && profile}
        {tab === "access" && (
          <div className="space-y-5">
            {access}
            <RuntimeSection load={load} onRetry={() => reload()} agent={agent} workspaceId={workspaceId}>
              {(state, found) => <AdmissionBlock binding={found?.binding ?? null} state={state} />}
            </RuntimeSection>
          </div>
        )}
        {tab === "execution" && (
          <RuntimeSection load={load} onRetry={() => reload()} agent={agent} workspaceId={workspaceId}>
            {(state, found) => (
              <ExecutionBlock
                state={state}
                binding={found?.binding ?? null}
                bindingRef={found?.ref ?? ""}
                workspaceId={workspaceId}
                onReload={reload}
              />
            )}
          </RuntimeSection>
        )}
        {tab === "state" && (
          <RuntimeSection load={load} onRetry={() => reload()} agent={agent} workspaceId={workspaceId}>
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
  agent: Pick<Agent, "id">;
  workspaceId: string | null;
  children: (state: RuntimeState, found: { ref: string; binding: RuntimeBinding } | null) => ReactNode;
}) {
  const { t } = useTranslation("integrations");
  if (load === "loading") {
    return (
      <div aria-busy="true" aria-label={t("runtimeTabs.loadingRuntime")} className="space-y-2">
        <Skeleton className="h-20 w-full" />
      </div>
    );
  }
  if (load.kind === "unavailable") {
    return (
      <div role="alert" className="space-y-2 rounded-md border border-destructive/40 p-3">
        <p className="text-sm text-destructive">{t("runtimeTabs.loadError")}</p>
        <Button size="sm" variant="outline" onClick={onRetry}>
          {t("runtimeTabs.retry")}
        </Button>
      </div>
    );
  }
  if (load.kind === "forbidden") {
    return <Note>{t("runtimeTabs.forbidden")}</Note>;
  }
  if (load.kind === "not_configured") {
    return <Note>{t("runtimeTabs.notConfigured")}</Note>;
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

function PreviewPanel({ workspaceId, bindingRef }: { workspaceId: string | null; bindingRef: string }) {
  const { t } = useTranslation("integrations");
  const [result, setResult] = useState<PreviewResult | "loading" | null>(null);
  const run = () => {
    if (!workspaceId) return;
    setResult("loading");
    void previewBinding(workspaceId, bindingRef).then(setResult);
  };
  return (
    <div>
      <Heading>{t("runtimeTabs.preview.heading")}</Heading>
      <Button size="sm" variant="outline" onClick={run} disabled={!workspaceId || result === "loading"}>
        {result === "loading" ? t("runtimeTabs.preview.asking") : t("runtimeTabs.preview.explain")}
      </Button>
      <p className="mt-1 text-xs text-muted-foreground">{t("runtimeTabs.preview.advisory")}</p>
      {result && result !== "loading" && <PreviewResultView result={result} />}
    </div>
  );
}

function PreviewResultView({ result }: { result: PreviewResult }) {
  const { t } = useTranslation("integrations");
  if (result.kind === "forbidden") return <Note>{t("runtimeTabs.preview.forbidden")}</Note>;
  if (result.kind === "stale") return <Note>{t("runtimeTabs.preview.stale")}</Note>;
  if (result.kind === "unavailable") return <Note>{t("runtimeTabs.preview.unavailable")}</Note>;
  const p = result.preview;
  return (
    <div className="mt-2 space-y-1.5 text-sm" data-testid="runtime-preview">
      <p>
        {p.selectedProfileId ? (
          <>
            <Trans
              i18nKey="runtimeTabs.preview.wouldPick"
              ns="integrations"
              values={{ profile: p.selectedProfileId, account: p.selectedAccountRef || "?" }}
              components={{ profile: <span className="font-medium" />, account: <span className="font-medium" /> }}
            />
            {p.preferredAccountRef && (
              <span className="text-muted-foreground" data-testid="runtime-preview-preferred">
                {p.preferredAccountRef === p.selectedAccountRef
                  ? t("runtimeTabs.preview.preferredAccount")
                  : t("runtimeTabs.preview.preferredOther", { account: p.preferredAccountRef })}
              </span>
            )}
          </>
        ) : (
          <>{t("runtimeTabs.preview.noRoute")}</>
        )}
        <span className="text-muted-foreground">{t("runtimeTabs.preview.reason", { reason: p.reason })}</span>
      </p>
      {p.qaMode && (
        <p className="text-xs text-muted-foreground">
          {t("runtimeTabs.preview.qa", { mode: t(`policyEditor.qaModes.${p.qaMode}`, { defaultValue: p.qaMode }) })}
          {p.qaCompromise ? t("runtimeTabs.preview.qaCompromise") : ""}
        </p>
      )}
      {p.trace.length > 0 && (
        <ul className="space-y-0.5 text-xs text-muted-foreground">
          {p.trace.map((tr, i) => (
            <li key={`${tr.profileId}-${i}`}>
              {tr.profileId}: {tr.reason}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

const NoBinding = () => {
  const { t } = useTranslation("integrations");
  return <Note>{t("runtimeTabs.noBinding")}</Note>;
};

function AdmissionBlock({ binding }: { binding: RuntimeBinding | null; state: RuntimeState }) {
  const { t } = useTranslation("integrations");
  if (!binding) return <NoBinding />;
  return (
    <div>
      <Heading>{t("runtimeTabs.admission.heading")}</Heading>
      <div className="space-y-1.5 text-sm">
        <p>{binding.enabled ? t("runtimeTabs.admission.enabled") : t("runtimeTabs.admission.disabled")}</p>
        <p>{t("runtimeTabs.admission.permitted", { profiles: binding.permitted_profiles.join(", ") })}</p>
      </div>
    </div>
  );
}

function ExecutionBlock({
  state,
  binding,
  bindingRef,
  workspaceId,
  onReload,
}: {
  state: RuntimeState;
  binding: RuntimeBinding | null;
  bindingRef: string;
  workspaceId: string | null;
  onReload: (quiet?: boolean) => void;
}) {
  const { t } = useTranslation("integrations");
  const [editing, setEditing] = useState(false);
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
        <PreviewPanel workspaceId={workspaceId} bindingRef={bindingRef} />
      </section>
      {workspaceId && (
        <section className="lg:col-span-2">
          {editing ? (
            <PolicyEditor
              workspaceId={workspaceId}
              bindingRef={bindingRef}
              binding={binding}
              catalog={catalog}
              revision={state.revision}
              onReload={onReload}
              onClose={() => setEditing(false)}
            />
          ) : (
            <Button size="sm" variant="outline" onClick={() => setEditing(true)}>
              {t("runtimeTabs.execution.editRoutes")}
            </Button>
          )}
        </section>
      )}
      <section className="lg:col-span-2">
        <Heading>{t("runtimeTabs.execution.allowedRoutes")}</Heading>
        {routes.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("runtimeTabs.execution.noPermittedRoute")}</p>
        ) : (
          <ol className="space-y-1.5">
            {routes.map((r) => (
              <li
                key={r.profileRef}
                className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border border-border px-2.5 py-1.5 text-sm"
              >
                <span className="w-5 text-muted-foreground">{r.rank}.</span>
                <Badge variant={r.role === "primary" ? "secondary" : "outline"} className="text-xs capitalize">
                  {t(`runtimeTabs.execution.role.${r.role}`)}
                </Badge>
                <span className="font-medium">{r.profileRef}</span>
                <span className="text-muted-foreground">
                  {catalog.accounts[r.profile.account_ref]?.provider ?? "?"} · {r.profile.account_ref} · {r.profile.model} ·{" "}
                  {r.profile.execution_mode}
                </span>
                {r.unavailable && <span className="text-xs text-yellow-600">{t("runtimeTabs.execution.unavailable", { reason: r.unavailable })}</span>}
              </li>
            ))}
          </ol>
        )}
      </section>

      <section>
        <Heading>{t("runtimeTabs.execution.preferredVsActual")}</Heading>
        {prefs.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("runtimeTabs.execution.noPreferred")}</p>
        ) : (
          <ul className="space-y-1.5 text-sm">
            {prefs.map((p) => (
              <li key={p.provider} className="rounded-md border border-border px-2.5 py-1.5">
                <Trans
                  i18nKey="runtimeTabs.execution.wantedProjected"
                  ns="integrations"
                  values={{
                    provider: p.provider,
                    preferred: p.preferredAccount,
                    actual: p.actualAccount ?? t("runtimeTabs.execution.none"),
                  }}
                  components={{ prov: <span className="font-medium" />, pref: <b />, act: <b /> }}
                />
                <div className="text-xs text-muted-foreground">{p.reason}</div>
              </li>
            ))}
          </ul>
        )}
        <p className="mt-1 text-xs text-muted-foreground">
          {t("runtimeTabs.execution.softPreference")}
        </p>
        <p className="mt-1 text-xs text-muted-foreground">
          {t("runtimeTabs.execution.ownerSide")}
        </p>
      </section>

      <section>
        <Heading>{t("runtimeTabs.execution.qaPolicy")}</Heading>
        <div className="space-y-1 text-sm">
          <p>
            <Trans i18nKey="runtimeTabs.execution.qaMode" ns="integrations" values={{ mode: t(`policyEditor.qaModes.${qa.mode}`, { defaultValue: qa.mode }) }} components={{ b: <b /> }} />
          </p>
          <p>
            <Trans
              i18nKey="runtimeTabs.execution.qaFallback"
              ns="integrations"
              values={{ value: qa.quota_fallback ? t("runtimeTabs.execution.on") : t("runtimeTabs.execution.off") }}
              components={{ b: <b /> }}
            />
          </p>
          <p className="text-xs text-muted-foreground">
            {t("runtimeTabs.execution.qaTied")}
          </p>
        </div>
      </section>

      <section className="lg:col-span-2">
        <Heading>{t("runtimeTabs.execution.pools")}</Heading>
        {poolRefs.size === 0 ? (
          <p className="text-sm text-muted-foreground">{t("runtimeTabs.execution.noPools")}</p>
        ) : (
          <ul className="grid gap-1.5 text-sm sm:grid-cols-2">
            {[...poolRefs].map((ref) => {
              const pool = catalog.pools[ref];
              return (
                <li key={ref} className="rounded-md border border-border px-2.5 py-1.5">
                  <span className="font-medium">{ref}</span>
                  {pool ? (
                    <span className="text-muted-foreground">
                      {t("runtimeTabs.execution.poolDetail", {
                        max: pool.max_concurrency,
                        threshold: pool.threshold_ref ?? t("runtimeTabs.execution.notSet"),
                      })}
                      {observed[ref] &&
                        ` · ${observed[ref].state}${observed[ref].verified ? "" : t("runtimeTabs.execution.unverified")}`}
                      {pool.aliases.length > 0 && t("runtimeTabs.execution.aliases", { aliases: pool.aliases.join(", ") })}
                    </span>
                  ) : (
                    <span className="text-xs text-yellow-600">{t("runtimeTabs.execution.notInCatalog")}</span>
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
  const { t } = useTranslation("integrations");
  const entries = Object.entries(state.catalog.controllers);
  return (
    <div>
      <Heading>{t("runtimeTabs.state.desiredRevision")}</Heading>
      <p className="mb-4 text-sm">
        r{state.revision}
        {state.digest && <span className="text-muted-foreground"> · {state.digest.slice(0, 12)}</span>}
      </p>
      <Heading>{t("runtimeTabs.state.controllers")}</Heading>
      <ControllerList state={state} entries={entries} />
    </div>
  );
}

export function ControllerList({ state, entries }: { state: RuntimeState; entries: [string, RuntimeController][] }) {
  const { t } = useTranslation("integrations");
  if (entries.length === 0) return <p className="text-sm text-muted-foreground">{t("runtimeTabs.state.noControllers")}</p>;
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
            {!ctl.enabled && <span className="text-xs text-yellow-600">{t("runtimeTabs.state.controllerDisabled")}</span>}
            {status?.received_at && (
              <span className="text-xs text-muted-foreground">{t("runtimeTabs.state.heartbeat", { when: formatRelative(status.received_at) })}</span>
            )}
          </li>
        );
      })}
    </ul>
  );
}
