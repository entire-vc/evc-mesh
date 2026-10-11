import { useState } from "react";
import { Button } from "@/components/ui/button";
import {
  saveBindingPolicy,
  type QaMode,
  type RuntimeBinding,
  type RuntimeCatalog,
  type SavePolicyResult,
} from "@/lib/agent-runtime";

interface PolicyEditorProps {
  workspaceId: string;
  bindingRef: string;
  binding: RuntimeBinding;
  catalog: RuntimeCatalog;
  /** Catalog revision this edit starts from (sent as if_revision). */
  revision: number;
  /** Re-read the catalog (after a save, or after a conflict). */
  onReload: (quiet?: boolean) => void;
  onClose: () => void;
}

const QA_MODES: QaMode[] = ["off", "prefer", "strict"];

/**
 * Reserve routes are ranked by quota_edges, not by permitted order. Keep the
 * edge topology, but order each target list by the edited permitted order and
 * drop targets that are no longer permitted, so the editor order is the real one.
 */
export function orderQuotaEdges(edges: Record<string, string[]>, allowed: string[]): Record<string, string[]> {
  const out: Record<string, string[]> = {};
  for (const from of allowed) if (edges[from]) out[from] = allowed.filter((t) => edges[from]!.includes(t));
  return out;
}

function Msg({ result }: { result: SavePolicyResult }) {
  if (result.kind === "ok") return <p role="status" className="text-sm">Saved as revision {result.revision}.</p>;
  const text =
    result.kind === "forbidden"
      ? "Only the resource-owner workspace owner or admin can change routes."
      : result.kind === "stale"
        ? "The runtime configuration changed since you opened this editor. Nothing was saved. Reload and redo the change."
        : result.kind === "invalid"
          ? `The server rejected these settings${result.message ? `: ${result.message}` : "."}`
          : "Could not save. Nothing was changed.";
  return <p role="alert" className="text-sm text-destructive">{text}</p>;
}

export function PolicyEditor({ workspaceId, bindingRef, binding, catalog, revision, onReload, onClose }: PolicyEditorProps) {
  const { policy } = binding;
  // Ordered permitted profiles; anything in the catalog but not here is "not allowed".
  const [allowed, setAllowed] = useState<string[]>(binding.permitted_profiles);
  const [primary, setPrimary] = useState<string[]>(policy.primary_profiles);
  const [preferred, setPreferred] = useState<Record<string, string>>(policy.preferred_accounts);
  const [apiReserve, setApiReserve] = useState<string[]>(policy.api_reserve_providers);
  const [qa, setQa] = useState<QaMode>(policy.qa.mode);
  const [qaFallback, setQaFallback] = useState(policy.qa.quota_fallback === true);
  const [result, setResult] = useState<SavePolicyResult | "saving" | null>(null);

  const profileRefs = Object.keys(catalog.profiles);
  const providers = [...new Set(Object.values(catalog.accounts).map((a) => a.provider))].sort();
  const toggle = (list: string[], v: string) => (list.includes(v) ? list.filter((x) => x !== v) : [...list, v]);
  const move = (i: number, d: number) =>
    setAllowed((a) => {
      const j = i + d;
      if (j < 0 || j >= a.length) return a;
      const n = [...a];
      [n[i], n[j]] = [n[j]!, n[i]!];
      return n;
    });
  const stale = result !== null && result !== "saving" && result.kind === "stale";
  const saved = result !== null && result !== "saving" && result.kind === "ok";

  const save = () => {
    setResult("saving");
    const preferredAccounts = Object.fromEntries(Object.entries(preferred).filter(([, v]) => v));
    void saveBindingPolicy(workspaceId, bindingRef, revision, allowed, {
      ...policy,
      primary_profiles: allowed.filter((p) => primary.includes(p)),
      quota_edges: orderQuotaEdges(policy.quota_edges, allowed),
      preferred_accounts: preferredAccounts,
      api_reserve_providers: apiReserve,
      qa: { mode: qa, quota_fallback: qaFallback },
    }).then((r) => {
      setResult(r);
      if (r.kind === "ok") onReload(true);
    });
  };

  return (
    <form
      aria-label="Edit routes"
      className="space-y-4 rounded-md border border-border p-3"
      onSubmit={(e) => {
        e.preventDefault();
        if (!stale && !saved) save();
      }}
    >
      <fieldset className="space-y-1.5">
        <legend className="mb-1 text-xs font-medium uppercase tracking-wider text-muted-foreground">
          Allowed routes, in order
        </legend>
        <p className="text-xs text-muted-foreground">
          Primary routes run first. This list orders only the reserves of one route; it does not make a route primary.
        </p>
        {allowed.map((ref, i) => (
          <div key={ref} className="flex flex-wrap items-center gap-2 text-sm">
            <span className="w-5 text-muted-foreground">{i + 1}.</span>
            <span className="font-medium">{ref}</span>
            <label className="flex items-center gap-1 text-xs">
              <input type="checkbox" checked={primary.includes(ref)} onChange={() => setPrimary(toggle(primary, ref))} />
              Primary
            </label>
            <Button type="button" size="sm" variant="ghost" aria-label={`Move ${ref} up`} disabled={i === 0} onClick={() => move(i, -1)}>
              Up
            </Button>
            <Button type="button" size="sm" variant="ghost" aria-label={`Move ${ref} down`} disabled={i === allowed.length - 1} onClick={() => move(i, 1)}>
              Down
            </Button>
            <Button type="button" size="sm" variant="ghost" aria-label={`Remove ${ref}`} onClick={() => setAllowed(allowed.filter((x) => x !== ref))}>
              Remove
            </Button>
          </div>
        ))}
        {profileRefs.filter((p) => !allowed.includes(p)).map((ref) => (
          <div key={ref} className="flex items-center gap-2 text-sm text-muted-foreground">
            <span>{ref}</span>
            <Button type="button" size="sm" variant="ghost" aria-label={`Allow ${ref}`} onClick={() => setAllowed([...allowed, ref])}>
              Allow
            </Button>
          </div>
        ))}
      </fieldset>

      <fieldset className="space-y-1.5">
        <legend className="mb-1 text-xs font-medium uppercase tracking-wider text-muted-foreground">
          Preferred account per provider
        </legend>
        {providers.map((prov) => (
          <label key={prov} className="flex flex-wrap items-center gap-2 text-sm">
            <span className="w-28">{prov}</span>
            <select
              aria-label={`Preferred account for ${prov}`}
              className="rounded-md border border-border bg-background px-2 py-1"
              value={preferred[prov] ?? ""}
              onChange={(e) => setPreferred({ ...preferred, [prov]: e.target.value })}
            >
              <option value="">No preference</option>
              {Object.entries(catalog.accounts)
                .filter(([, a]) => a.provider === prov)
                .map(([ref]) => (
                  <option key={ref} value={ref}>
                    {ref}
                  </option>
                ))}
            </select>
          </label>
        ))}
        <p className="text-xs text-muted-foreground">A soft preference: it grants no access.</p>
      </fieldset>

      <fieldset className="space-y-1.5">
        <legend className="mb-1 text-xs font-medium uppercase tracking-wider text-muted-foreground">
          API reserve (explicit opt-in)
        </legend>
        {providers.map((prov) => (
          <label key={prov} className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={apiReserve.includes(prov)} onChange={() => setApiReserve(toggle(apiReserve, prov))} />
            Allow paid API fallback for {prov}
          </label>
        ))}
      </fieldset>

      <fieldset className="space-y-1.5">
        <legend className="mb-1 text-xs font-medium uppercase tracking-wider text-muted-foreground">QA</legend>
        <label className="flex items-center gap-2 text-sm">
          Mode
          <select
            aria-label="QA mode"
            className="rounded-md border border-border bg-background px-2 py-1"
            value={qa}
            onChange={(e) => setQa(e.target.value as QaMode)}
          >
            {QA_MODES.map((m) => (
              <option key={m} value={m}>
                {m}
              </option>
            ))}
          </select>
        </label>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={qaFallback} onChange={(e) => setQaFallback(e.target.checked)} />
          Quota-only fallback
        </label>
      </fieldset>

      {result && result !== "saving" && <Msg result={result} />}
      <div className="flex flex-wrap gap-2">
        {stale ? (
          <Button type="button" size="sm" onClick={() => onReload()}>
            Reload
          </Button>
        ) : (
          <Button type="submit" size="sm" disabled={result === "saving" || saved || allowed.length === 0}>
            {result === "saving" ? "Saving…" : "Save routes"}
          </Button>
        )}
        <Button type="button" size="sm" variant="outline" onClick={onClose}>
          {saved ? "Close" : "Cancel"}
        </Button>
      </div>
    </form>
  );
}
