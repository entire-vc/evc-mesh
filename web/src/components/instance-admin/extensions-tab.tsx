import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router";
import { Search } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "@/components/ui/toast";
import { SchemaForm, type Values } from "@/components/extensions/schema-form";
import { ExtensionStateBadge, ExtensionStateNote } from "@/components/extensions/extension-state";
import { cn } from "@/lib/cn";
import { extensionState } from "@/lib/extensionState";
import { saveInstanceExtensionSettings } from "@/lib/extension-settings";
import {
  envMigratedDate,
  getInstanceSettings,
  setExtensionAllowed,
  setInstanceAutoUpdate,
  type InstanceExtensionSummary,
  type InstanceOverview,
  type InstanceOverviewItem,
  type InstanceSettingsDto,
} from "@/lib/extension-instance";

export function Switch({ checked, onChange, label, disabled, hideLabel }: { checked: boolean; onChange: (v: boolean) => void; label: string; disabled?: boolean; hideLabel?: boolean }) {
  return (
    <label className="inline-flex cursor-pointer items-center gap-2 text-sm">
      <input
        type="checkbox"
        role="switch"
        aria-label={label}
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
        className="h-4 w-4 accent-primary"
      />
      <span className={hideLabel ? "sr-only" : undefined}>{label}</span>
    </label>
  );
}

const KINDS = ["integration", "plugin", "theme"];
const SOURCES = ["builtin", "catalog", "repo"];
const TRUSTS = ["builtin", "official", "verified", "community", "custom"];

const known = (list: string[], v: string | undefined, fallback: string) => (v && list.includes(v) ? v : fallback);

/** Desktop column template; below md every row is a stacked card with the same cells. */
const GRID = "md:grid md:grid-cols-[minmax(0,2fr)_6.5rem_7rem_7.5rem_9.5rem_6rem_10rem] md:items-center md:gap-4";

function Cell({ label, children, className }: { label: string; children: React.ReactNode; className?: string }) {
  return (
    <div className={cn("flex min-w-0 items-center justify-between gap-3 md:block", className)}>
      <span className="text-xs text-muted-foreground md:sr-only">{label}</span>
      <div className="min-w-0 text-right md:text-left">{children}</div>
    </div>
  );
}

function SettingsForm({ item, summaries, onSaved }: { item: InstanceOverviewItem; summaries: InstanceExtensionSummary[]; onSaved: () => void }) {
  const { t } = useTranslation("integrations");
  const [dto, setDto] = useState<InstanceSettingsDto | null>(null);
  const [enabled, setEnabled] = useState(false);
  const [loadFailed, setLoadFailed] = useState(false);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let live = true;
    setDto(null);
    setLoadFailed(false);
    getInstanceSettings(item.id)
      .then((d) => {
        if (!live) return;
        setDto(d);
        setEnabled(d.enabled);
      })
      .catch(() => live && setLoadFailed(true));
    return () => {
      live = false;
    };
  }, [item.id, attempt]);

  const manifest = summaries.find((s) => s.id === item.id)?.manifest;
  const schema = manifest?.settings?.instance;

  const submit = useCallback(
    async (settings: Values) => {
      await saveInstanceExtensionSettings(item.id, { enabled, settings });
      toast.success(t("instanceAdmin.settings.saved"));
      setDto(await getInstanceSettings(item.id));
      onSaved();
    },
    [item.id, enabled, onSaved, t],
  );

  if (loadFailed) {
    return (
      <div role="alert" className="flex flex-wrap items-center gap-3 text-sm text-destructive">
        {t("instanceAdmin.settings.loadFailed")}
        <Button variant="outline" size="sm" onClick={() => setAttempt((n) => n + 1)}>
          {t("instanceAdmin.retry")}
        </Button>
      </div>
    );
  }
  if (!dto) return <Skeleton className="h-40 w-full rounded-xl" />;
  if (!schema) return <p className="text-sm text-muted-foreground">{t("instanceAdmin.settings.noForm")}</p>;
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <Switch checked={enabled} onChange={setEnabled} label={t("instanceAdmin.settings.enabled")} />
        {dto.source === "env" && (
          <Badge variant="outline" data-testid="env-badge">
            {envMigratedDate(dto.updated_at)
              ? t("instanceAdmin.settings.fromEnv", { date: envMigratedDate(dto.updated_at) })
              : t("instanceAdmin.settings.fromEnvNoDate")}
          </Badge>
        )}
      </div>
      <SchemaForm
        key={`${item.id}-${dto.updated_at ?? "none"}`}
        schema={schema}
        manifest={manifest}
        value={dto.settings}
        onSubmit={submit}
        submitLabel={t("instanceAdmin.settings.save")}
      />
    </div>
  );
}

function Row({ item, summaries, open, highlighted, busy, onToggleOpen, onAllow, onSaved }: {
  item: InstanceOverviewItem;
  summaries: InstanceExtensionSummary[];
  open: boolean;
  highlighted: boolean;
  busy: boolean;
  onToggleOpen: () => void;
  onAllow: (v: boolean) => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation("integrations");
  const state = extensionState({ kind: item.kind, allowed: item.allowed, blocked: !!item.blocked, blockedReason: item.blocked_reason, status: { enabled: item.enabled, source: "instance" } });
  const ref = useRef<HTMLLIElement>(null);
  useEffect(() => {
    // Arrival by `?ext=`: bring the row into view once; later clicks must not jump the page.
    if (highlighted) ref.current?.scrollIntoView?.({ block: "start" });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const panelId = `instance-settings-${item.id}`;
  const keysState = item.settings_source === "env" ? "env" : item.settings_source === "admin" ? "admin" : "none";

  return (
    <li
      ref={ref}
      data-testid={`ext-row-${item.id}`}
      className={cn("scroll-mt-4 px-4 py-3", highlighted && "bg-primary/5 ring-1 ring-inset ring-primary/40")}
    >
      <div className={cn("space-y-2 md:space-y-0", GRID)}>
        <div className="min-w-0">
          <p className="break-words text-sm font-medium">{item.name}</p>
          <p className="break-all text-xs text-muted-foreground">
            {item.id}
            {item.version ? ` · v${item.version}` : ""}
          </p>
          <p className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground md:hidden">
            <span>{t(`instanceAdmin.kind.${known(KINDS, item.kind, "plugin")}`)}</span>
            {item.source && <span>· {t(`instanceAdmin.source.${known(SOURCES, item.source, "catalog")}`)}</span>}
            <Badge variant="secondary">{t(`instanceAdmin.trust.${known(TRUSTS, item.trust_level, "custom")}`)}</Badge>
          </p>
        </div>
        <Cell className="hidden md:block" label={t("instanceAdmin.table.kind")}>
          <span className="text-sm">{t(`instanceAdmin.kind.${known(KINDS, item.kind, "plugin")}`)}</span>
        </Cell>
        <Cell className="hidden md:block" label={t("instanceAdmin.table.source")}>
          <span className="text-sm" data-testid={`ext-source-${item.id}`}>
            {item.source ? t(`instanceAdmin.source.${known(SOURCES, item.source, "catalog")}`) : "—"}
          </span>
        </Cell>
        <Cell className="hidden md:block" label={t("instanceAdmin.table.trust")}>
          <Badge variant="secondary">{t(`instanceAdmin.trust.${known(TRUSTS, item.trust_level, "custom")}`)}</Badge>
        </Cell>
        <Cell label={t("instanceAdmin.table.allowed")}>
          <div className="flex items-center justify-end gap-2 md:justify-start">
            <Switch
              hideLabel
              checked={item.allowed}
              disabled={busy}
              label={t("instanceAdmin.allow.toggle", { name: item.name })}
              onChange={onAllow}
            />
            <span className="text-left leading-tight">
              <span className="block" data-testid={`allow-state-${item.id}`}>
                <ExtensionStateBadge scope="instance" state={state} />
              </span>
              {state.state === "blocked" && <ExtensionStateNote state={state} />}
              <span className="block text-xs text-muted-foreground">
                {t(item.allow_explicit ? "instanceAdmin.allow.explicit" : "instanceAdmin.allow.default")}
              </span>
            </span>
          </div>
        </Cell>
        <Cell label={t("instanceAdmin.table.workspaces")}>
          <span className="text-sm tabular-nums" data-testid={`ext-workspaces-${item.id}`}>
            {item.enabled_workspaces === undefined ? "—" : item.enabled_workspaces}
          </span>
        </Cell>
        <Cell label={t("instanceAdmin.table.keys")}>
          {item.has_instance_form ? (
            <div className="flex items-center justify-end gap-2 md:justify-start">
              <span className={cn("text-xs", keysState === "none" ? "text-muted-foreground" : "text-foreground")}>
                {t(`instanceAdmin.keys.${keysState}`)}
              </span>
              <Button variant={open ? "secondary" : "outline"} size="sm" aria-expanded={open} aria-controls={panelId} onClick={onToggleOpen}>
                {t(open ? "instanceAdmin.keys.close" : "instanceAdmin.keys.configure")}
              </Button>
            </div>
          ) : (
            <span className="text-xs text-muted-foreground">{t("instanceAdmin.keys.noForm")}</span>
          )}
        </Cell>
      </div>
      {open && item.has_instance_form && (
        <section id={panelId} data-testid="instance-settings" aria-label={t("instanceAdmin.settings.titleFor", { name: item.name })} className="mt-3 rounded-lg border border-border bg-background p-4">
          <h3 className="mb-1 text-sm font-semibold">{t("instanceAdmin.settings.titleFor", { name: item.name })}</h3>
          <p className="mb-4 text-xs text-muted-foreground">{t("instanceAdmin.settings.body")}</p>
          <SettingsForm item={item} summaries={summaries} onSaved={onSaved} />
        </section>
      )}
    </li>
  );
}

export function ExtensionsTab({ overview, summaries, onChanged }: { overview: InstanceOverview; summaries: InstanceExtensionSummary[]; onChanged: () => void }) {
  const { t } = useTranslation("integrations");
  const [search, setSearch] = useSearchParams();
  const asked = search.get("ext");
  const [arrivedAt] = useState(() => (overview.items.some((i) => i.id === asked) ? asked : null));
  const [openId, setOpenId] = useState<string | null>(() => overview.items.find((i) => i.id === asked && i.has_instance_form)?.id ?? null);
  const [query, setQuery] = useState("");
  const [kind, setKind] = useState("");
  const [source, setSource] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [autoBusy, setAutoBusy] = useState(false);

  const kinds = useMemo(() => KINDS.filter((k) => overview.items.some((i) => i.kind === k)), [overview.items]);
  const sources = useMemo(() => SOURCES.filter((s) => overview.items.some((i) => i.source === s)), [overview.items]);
  const q = query.trim().toLowerCase();
  const rows = overview.items.filter(
    (i) => (!kind || i.kind === kind) && (!source || i.source === source) && (!q || i.name.toLowerCase().includes(q) || i.id.toLowerCase().includes(q)),
  );
  const filtered = q !== "" || kind !== "" || source !== "";

  const toggleOpen = (id: string) => {
    const next = openId === id ? null : id;
    setOpenId(next);
    // Keep the address shareable: `?ext=` names the open row.
    const params = new URLSearchParams(search);
    if (next) params.set("ext", next);
    else params.delete("ext");
    setSearch(params, { replace: true });
  };

  const allow = async (id: string, allowed: boolean) => {
    setBusy(id);
    try {
      await setExtensionAllowed(id, allowed);
      toast.success(t("instanceAdmin.allow.saved"));
      onChanged();
    } catch {
      toast.error(t("instanceAdmin.allow.saveFailed"));
    } finally {
      setBusy(null);
    }
  };

  const toggleAuto = async (v: boolean) => {
    setAutoBusy(true);
    try {
      await setInstanceAutoUpdate(v);
      toast.success(t("instanceAdmin.allow.saved"));
      onChanged();
    } catch {
      toast.error(t("instanceAdmin.allow.saveFailed"));
    } finally {
      setAutoBusy(false);
    }
  };

  const reset = () => {
    setQuery("");
    setKind("");
    setSource("");
  };

  return (
    <div className="space-y-4">
      <div data-testid="auto-update" className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-border px-4 py-3">
        <div className="min-w-0">
          <p className="text-sm font-medium">{t("instanceAdmin.autoUpdate.title")}</p>
          <p className="text-xs text-muted-foreground">{t("instanceAdmin.autoUpdate.body")}</p>
        </div>
        <Switch checked={overview.auto_update} disabled={autoBusy} onChange={(v) => void toggleAuto(v)} label={t("instanceAdmin.autoUpdate.label")} />
      </div>

      <p className="max-w-prose text-sm text-muted-foreground">{t("instanceAdmin.allow.body")}</p>

      <div className="flex flex-wrap items-end gap-3">
        <div className="relative w-full sm:w-64">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" aria-hidden />
          <Input type="search" value={query} onChange={(e) => setQuery(e.target.value)} placeholder={t("instanceAdmin.filters.search")} aria-label={t("instanceAdmin.filters.search")} className="pl-8" />
        </div>
        <Select aria-label={t("instanceAdmin.table.kind")} value={kind} onChange={(e) => setKind(e.target.value)} className="w-40">
          <option value="">{t("instanceAdmin.filters.allKinds")}</option>
          {kinds.map((k) => (
            <option key={k} value={k}>
              {t(`instanceAdmin.kind.${k}`)}
            </option>
          ))}
        </Select>
        <Select aria-label={t("instanceAdmin.table.source")} value={source} onChange={(e) => setSource(e.target.value)} className="w-44">
          <option value="">{t("instanceAdmin.filters.allSources")}</option>
          {sources.map((s) => (
            <option key={s} value={s}>
              {t(`instanceAdmin.source.${s}`)}
            </option>
          ))}
        </Select>
        <span className="pb-2 text-xs text-muted-foreground" aria-live="polite">
          {t("instanceAdmin.filters.shown", { shown: rows.length, total: overview.items.length })}
        </span>
        {filtered && (
          <Button variant="ghost" size="sm" onClick={reset}>
            {t("instanceAdmin.filters.reset")}
          </Button>
        )}
      </div>

      <div data-testid="allow-list" className="overflow-hidden rounded-xl border border-border">
        <div aria-hidden className={cn("hidden border-b border-border bg-muted/40 px-4 py-2 text-xs font-medium text-muted-foreground", GRID)}>
          <span>{t("instanceAdmin.table.name")}</span>
          <span>{t("instanceAdmin.table.kind")}</span>
          <span>{t("instanceAdmin.table.source")}</span>
          <span>{t("instanceAdmin.table.trust")}</span>
          <span>{t("instanceAdmin.table.allowed")}</span>
          <span>{t("instanceAdmin.table.workspaces")}</span>
          <span>{t("instanceAdmin.table.keys")}</span>
        </div>
        {rows.length === 0 ? (
          <div className="flex flex-col items-center gap-2 px-6 py-12 text-center">
            <p className="text-sm font-medium">{t(filtered ? "instanceAdmin.filters.noMatch" : "instanceAdmin.table.empty")}</p>
            {filtered && (
              <Button variant="outline" size="sm" onClick={reset}>
                {t("instanceAdmin.filters.reset")}
              </Button>
            )}
          </div>
        ) : (
          <ul className="divide-y divide-border" aria-label={t("instanceAdmin.allow.title")}>
            {rows.map((it) => (
              <Row
                key={it.id}
                item={it}
                summaries={summaries}
                open={openId === it.id}
                highlighted={arrivedAt === it.id}
                busy={busy === it.id}
                onToggleOpen={() => toggleOpen(it.id)}
                onAllow={(v) => void allow(it.id, v)}
                onSaved={onChanged}
              />
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
