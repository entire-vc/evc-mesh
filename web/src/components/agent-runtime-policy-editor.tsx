import { useState } from "react";
import { useTranslation } from "react-i18next";
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
  const { t } = useTranslation("integrations");
  if (result.kind === "ok") return <p role="status" className="text-sm">{t("policyEditor.saved", { revision: result.revision })}</p>;
  const text =
    result.kind === "forbidden"
      ? t("policyEditor.forbidden")
      : result.kind === "stale"
        ? t("policyEditor.stale")
        : result.kind === "invalid"
          ? result.message
            ? t("policyEditor.invalidWithMessage", { message: result.message })
            : t("policyEditor.invalid")
          : t("policyEditor.saveFailed");
  return <p role="alert" className="text-sm text-destructive">{text}</p>;
}

export function PolicyEditor({ workspaceId, bindingRef, binding, catalog, revision, onReload, onClose }: PolicyEditorProps) {
  const { t } = useTranslation("integrations");
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
      aria-label={t("policyEditor.formLabel")}
      className="space-y-4 rounded-md border border-border p-3"
      onSubmit={(e) => {
        e.preventDefault();
        if (!stale && !saved) save();
      }}
    >
      <fieldset className="space-y-1.5">
        <legend className="mb-1 text-xs font-medium uppercase tracking-wider text-muted-foreground">
          {t("policyEditor.allowedRoutes")}
        </legend>
        <p className="text-xs text-muted-foreground">
          {t("policyEditor.allowedHint")}
        </p>
        {allowed.map((ref, i) => (
          <div key={ref} className="flex flex-wrap items-center gap-2 text-sm">
            <span className="w-5 text-muted-foreground">{i + 1}.</span>
            <span className="font-medium">{ref}</span>
            <label className="flex items-center gap-1 text-xs">
              <input type="checkbox" checked={primary.includes(ref)} onChange={() => setPrimary(toggle(primary, ref))} />
              {t("policyEditor.primary")}
            </label>
            <Button type="button" size="sm" variant="ghost" aria-label={t("policyEditor.moveUp", { ref })} disabled={i === 0} onClick={() => move(i, -1)}>
              {t("policyEditor.up")}
            </Button>
            <Button type="button" size="sm" variant="ghost" aria-label={t("policyEditor.moveDown", { ref })} disabled={i === allowed.length - 1} onClick={() => move(i, 1)}>
              {t("policyEditor.down")}
            </Button>
            <Button type="button" size="sm" variant="ghost" aria-label={t("policyEditor.removeRef", { ref })} onClick={() => setAllowed(allowed.filter((x) => x !== ref))}>
              {t("policyEditor.remove")}
            </Button>
          </div>
        ))}
        {profileRefs.filter((p) => !allowed.includes(p)).map((ref) => (
          <div key={ref} className="flex items-center gap-2 text-sm text-muted-foreground">
            <span>{ref}</span>
            <Button type="button" size="sm" variant="ghost" aria-label={t("policyEditor.allowRef", { ref })} onClick={() => setAllowed([...allowed, ref])}>
              {t("policyEditor.allow")}
            </Button>
          </div>
        ))}
      </fieldset>

      <fieldset className="space-y-1.5">
        <legend className="mb-1 text-xs font-medium uppercase tracking-wider text-muted-foreground">
          {t("policyEditor.preferredPerProvider")}
        </legend>
        {providers.map((prov) => (
          <label key={prov} className="flex flex-wrap items-center gap-2 text-sm">
            <span className="w-28">{prov}</span>
            <select
              aria-label={t("policyEditor.preferredFor", { provider: prov })}
              className="rounded-md border border-border bg-background px-2 py-1"
              value={preferred[prov] ?? ""}
              onChange={(e) => setPreferred({ ...preferred, [prov]: e.target.value })}
            >
              <option value="">{t("policyEditor.noPreference")}</option>
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
        <p className="text-xs text-muted-foreground">{t("policyEditor.softPreference")}</p>
      </fieldset>

      <fieldset className="space-y-1.5">
        <legend className="mb-1 text-xs font-medium uppercase tracking-wider text-muted-foreground">
          {t("policyEditor.apiReserve")}
        </legend>
        {providers.map((prov) => (
          <label key={prov} className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={apiReserve.includes(prov)} onChange={() => setApiReserve(toggle(apiReserve, prov))} />
            {t("policyEditor.paidFallback", { provider: prov })}
          </label>
        ))}
      </fieldset>

      <fieldset className="space-y-1.5">
        <legend className="mb-1 text-xs font-medium uppercase tracking-wider text-muted-foreground">{t("policyEditor.qa")}</legend>
        <label className="flex items-center gap-2 text-sm">
          {t("policyEditor.mode")}
          <select
            aria-label={t("policyEditor.qaModeLabel")}
            className="rounded-md border border-border bg-background px-2 py-1"
            value={qa}
            onChange={(e) => setQa(e.target.value as QaMode)}
          >
            {QA_MODES.map((m) => (
              <option key={m} value={m}>
                {t(`policyEditor.qaModes.${m}`)}
              </option>
            ))}
          </select>
        </label>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={qaFallback} onChange={(e) => setQaFallback(e.target.checked)} />
          {t("policyEditor.quotaFallback")}
        </label>
      </fieldset>

      {result && result !== "saving" && <Msg result={result} />}
      <div className="flex flex-wrap gap-2">
        {stale ? (
          <Button type="button" size="sm" onClick={() => onReload()}>
            {t("policyEditor.reload")}
          </Button>
        ) : (
          <Button type="submit" size="sm" disabled={result === "saving" || saved || allowed.length === 0}>
            {result === "saving" ? t("policyEditor.saving") : t("policyEditor.save")}
          </Button>
        )}
        <Button type="button" size="sm" variant="outline" onClick={onClose}>
          {saved ? t("policyEditor.close") : t("policyEditor.cancel")}
        </Button>
      </div>
    </form>
  );
}
