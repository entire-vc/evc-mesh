// What the page and the dashboard widget share: reads the state's data and draws the panel with its states
// (loading, error, empty, partial). Capabilities of the last usage answer decide which views and breakdowns exist:
// a non-admin gets neither «Pool / account» nor «Subscription limits» (§3.1–3.2).
import { useEffect } from "react";
import { ApiRequestError } from "@/lib/api";
import { useWorkspaceStore } from "@/stores/workspace";
import { loadedAt, useQuota, useUsage } from "./data";
import type { ST } from "./format";
import { ErrorBody, LoadingBody } from "./parts";
import { QuotaPanel } from "./quota-panel";
import { DIMENSIONS, MAX_TOP, isUserUsage, type StatsState } from "./state";
import { capabilitiesKey, sanitize, useCapabilities, useCapsStore } from "./capabilities";
export { allowedDimensions, sanitize, useCapabilities, useCapsStore } from "./capabilities";
import { UsagePanel, isOther, type OthersCount, type UsageLinks } from "./usage-panel";

export function StatsBody({
  state,
  onChange,
  mode,
  links,
  pinned,
  st,
}: {
  state: StatsState;
  onChange: (s: StatsState) => void;
  mode: "page" | "widget";
  links: UsageLinks;
  /** The host page owns this filter (a project's or an agent's page): the empty state cannot offer to drop it. */
  pinned?: "project" | "agent";
  st: ST;
}) {
  const ws = useWorkspaceStore((s) => s.currentWorkspace?.id);
  const setCaps = useCapsStore((s) => s.set);
  const scope = isUserUsage(state) ? "user" : "workspace";
  const caps = useCapabilities(scope);
  const effective = sanitize(state, caps);
  // Discover permissions with a universally available query before requesting an admin-only slice.
  const discovery = !caps && (state.m === "quota" || state.m === "cost" || state.g === "account" || state.s === "account");
  const isQuota = effective.m === "quota" && !discovery;
  const { account: _account, ...publicFilters } = state.f;
  const usage = useUsage(isQuota ? null : discovery ? { ...state, m: "tokens", g: isUserUsage(state) ? "workspace" : "model", s: null, f: publicFilters } : effective);
  const quota = useQuota(isQuota ? effective : null);
  // The API folds the rest into «Others» without a count (§3.1): the same query at the largest top tells how many
  // there are, and is the answer «Expand» shows next, so it is not wasted.
  const folded = !isQuota && !discovery && usage.state === "ready" && effective.n < MAX_TOP && usage.value.rows.some(isOther) && !usage.value.rows.some((r) => typeof r.other_count === "number");
  const full = useUsage(folded ? { ...effective, n: MAX_TOP } : null);
  const othersCount: OthersCount | null = folded && full.state === "ready" ? { all: full.value.rows.filter((r) => !isOther(r)).length, more: full.value.rows.some(isOther) } : null;

  useEffect(() => {
    if (ws && usage.state === "ready" && usage.value.capabilities) setCaps(capabilitiesKey(ws, scope), usage.value.capabilities);
  }, [ws, scope, usage, setCaps]);

  useEffect(() => {
    const next = sanitize(state, caps);
    if (next !== state) onChange(next);
  }, [state, caps, onChange]);

  // The limits are the admin's: a 403 means this viewer has none, so the view goes back to the default.
  const quotaDenied = quota.state === "error" && quota.error instanceof ApiRequestError && quota.error.status === 403;
  useEffect(() => {
    if (quotaDenied && ws) {
      setCaps(ws, { ...(caps ?? { group_by: DIMENSIONS.filter((d) => d !== "account"), cost: true }), quota: false });
      onChange({ ...state, m: caps?.cost === false ? "tokens" : "cost" });
    }
    // only the denial itself moves the view
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [quotaDenied]);

  if (discovery && usage.state !== "error") return <LoadingBody st={st} rows={mode === "page" ? 5 : 3} />;
  if (isQuota) {
    if (quota.state === "loading") return <LoadingBody st={st} rows={mode === "page" ? 5 : 3} />;
    if (quota.state === "error") return <ErrorBody st={st} error={quota.error} retry={quota.retry} />;
    return <QuotaPanel data={quota.value} state={effective} onChange={onChange} mode={mode} copySlice={links.copySlice} st={st} />;
  }
  if (usage.state === "loading") return <LoadingBody st={st} rows={mode === "page" ? 5 : 3} />;
  if (usage.state === "error") return <ErrorBody st={st} error={usage.error} retry={usage.retry} />;
  return <UsagePanel data={usage.value} state={effective} onChange={onChange} mode={mode} links={links} pinned={pinned} othersCount={othersCount} updatedAt={loadedAt(usage.value)} st={st} />;
}
