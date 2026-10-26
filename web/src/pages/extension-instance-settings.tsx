import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, useParams, useSearchParams } from "react-router";
import { AlertTriangle, ArrowLeft, Lock } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "@/components/ui/toast";
import { SchemaForm, type Values } from "@/components/extensions/schema-form";
import { ApiRequestError } from "@/lib/api";
import { formatDateTime } from "@/i18n/format";
import { saveInstanceExtensionSettings } from "@/lib/extension-settings";
import {
  envMigratedDate,
  getInstanceJournal,
  getInstanceOverview,
  getInstanceSettings,
  listExtensionSummaries,
  setExtensionAllowed,
  setInstanceAutoUpdate,
  type InstanceExtensionSummary,
  type InstanceJournalEntry,
  type InstanceOverview,
  type InstanceSettingsDto,
} from "@/lib/extension-instance";

type LoadState = "loading" | "ready" | "forbidden" | "error";

function Switch({ checked, onChange, label, disabled, compact }: { checked: boolean; onChange: (v: boolean) => void; label: string; disabled?: boolean; compact?: boolean }) {
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
      <span className={compact ? "sr-only md:not-sr-only" : undefined}>{label}</span>
    </label>
  );
}

function trustLabel(t: (k: string) => string, trust: string) {
  const known = ["builtin", "official", "verified", "community", "custom"];
  return t(`instanceAdmin.trust.${known.includes(trust) ? trust : "custom"}`);
}

function AllowList({ overview, onChange }: { overview: InstanceOverview; onChange: () => void }) {
  const { t } = useTranslation("integrations");
  const [busy, setBusy] = useState<string | null>(null);
  const toggle = async (id: string, allowed: boolean) => {
    setBusy(id);
    try {
      await setExtensionAllowed(id, allowed);
      toast.success(t("instanceAdmin.allow.saved"));
      onChange();
    } catch {
      toast.error(t("instanceAdmin.allow.saveFailed"));
    } finally {
      setBusy(null);
    }
  };
  return (
    <Card data-testid="allow-list">
      <CardHeader>
        <CardTitle>{t("instanceAdmin.allow.title")}</CardTitle>
        <CardDescription>{t("instanceAdmin.allow.body")}</CardDescription>
      </CardHeader>
      <CardContent>
        <ul className="divide-y divide-border">
          {overview.items.map((it) => (
            <li key={it.id} data-testid={`allow-row-${it.id}`} className="flex flex-wrap items-center justify-between gap-3 py-3">
              <div className="min-w-0">
                <p className="truncate text-sm font-medium">{it.name}</p>
                <p className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                  <Badge variant="secondary">{trustLabel(t, it.trust_level)}</Badge>
                  <span>{t(it.allow_explicit ? "instanceAdmin.allow.explicit" : "instanceAdmin.allow.default")}</span>
                </p>
              </div>
              <div className="flex items-center gap-3">
                <span className="text-xs text-muted-foreground" data-testid={`allow-state-${it.id}`}>
                  {t(it.allowed ? "instanceAdmin.allow.allowed" : "instanceAdmin.allow.blocked")}
                </span>
                <Switch
                  compact
                  checked={it.allowed}
                  disabled={busy === it.id}
                  label={t("instanceAdmin.allow.toggle", { name: it.name })}
                  onChange={(v) => void toggle(it.id, v)}
                />
              </div>
            </li>
          ))}
        </ul>
      </CardContent>
    </Card>
  );
}

function SettingsSection({ overview, summaries, onSaved }: { overview: InstanceOverview; summaries: InstanceExtensionSummary[]; onSaved: () => void }) {
  const { t } = useTranslation("integrations");
  const withForm = overview.items.filter((i) => i.has_instance_form);
  // `?ext=<id>` comes from a workspace's Settings dialog ("Edit at instance level").
  const [search] = useSearchParams();
  const asked = search.get("ext");
  const [selected, setSelected] = useState(withForm.find((i) => i.id === asked)?.id ?? withForm[0]?.id ?? "");
  const cardRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (asked && asked === selected) cardRef.current?.scrollIntoView?.({ block: "start" });
    // Only on arrival: picking another extension later must not jump the page.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const [dto, setDto] = useState<InstanceSettingsDto | null>(null);
  const [enabled, setEnabled] = useState(false);
  const [loadFailed, setLoadFailed] = useState(false);

  useEffect(() => {
    if (!selected) return;
    let live = true;
    setDto(null);
    setLoadFailed(false);
    getInstanceSettings(selected)
      .then((d) => {
        if (!live) return;
        setDto(d);
        setEnabled(d.enabled);
      })
      .catch(() => live && setLoadFailed(true));
    return () => {
      live = false;
    };
  }, [selected]);

  const manifest = summaries.find((s) => s.id === selected)?.manifest;
  const schema = manifest?.settings?.instance;
  const item = overview.items.find((i) => i.id === selected);

  const submit = useCallback(
    async (settings: Values) => {
      await saveInstanceExtensionSettings(selected, { enabled, settings });
      toast.success(t("instanceAdmin.settings.saved"));
      const fresh = await getInstanceSettings(selected);
      setDto(fresh);
      onSaved();
    },
    [selected, enabled, onSaved, t],
  );

  return (
    <Card data-testid="instance-settings" ref={cardRef}>
      <CardHeader>
        <CardTitle>{t("instanceAdmin.settings.title")}</CardTitle>
        <CardDescription>{t("instanceAdmin.settings.body")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {withForm.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("instanceAdmin.settings.noForm")}</p>
        ) : (
          <>
            <div className="flex flex-wrap items-center gap-3">
              <label htmlFor="instance-ext-pick" className="text-sm font-medium">
                {t("instanceAdmin.settings.pick")}
              </label>
              <Select id="instance-ext-pick" value={selected} onChange={(e) => setSelected(e.target.value)} className="w-56">
                {withForm.map((i) => (
                  <option key={i.id} value={i.id}>
                    {i.name}
                  </option>
                ))}
              </Select>
              {dto?.source === "env" && (
                <Badge variant="outline" data-testid="env-badge">
                  {envMigratedDate(dto.updated_at)
                    ? t("instanceAdmin.settings.fromEnv", { date: envMigratedDate(dto.updated_at) })
                    : t("instanceAdmin.settings.fromEnvNoDate")}
                </Badge>
              )}
            </div>
            {loadFailed && (
              <p role="alert" className="text-sm text-destructive">
                {t("instanceAdmin.settings.loadFailed")}
              </p>
            )}
            {!dto && !loadFailed && <Skeleton className="h-40 w-full rounded-xl" />}
            {dto && schema && item && (
              <div className="space-y-4">
                <Switch checked={enabled} onChange={setEnabled} label={t("instanceAdmin.settings.enabled")} />
                <SchemaForm
                  key={`${selected}-${dto.updated_at ?? "none"}`}
                  schema={schema}
                  manifest={manifest}
                  value={dto.settings}
                  onSubmit={submit}
                  submitLabel={t("instanceAdmin.settings.save")}
                />
              </div>
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
}

function journalLine(t: (k: string, o?: Record<string, unknown>) => string, e: InstanceJournalEntry, names: Map<string, string>) {
  const ext = names.get(e.extension_id) ?? e.extension_id;
  if (e.action === "settings_changed") {
    const list = Array.isArray(e.details.keys) ? (e.details.keys as string[]) : [];
    const parts: string[] = [];
    if (typeof e.details.enabled === "boolean") {
      parts.push(t(`instanceAdmin.journal.enabled_changed_${e.details.enabled ? "on" : "off"}`, { ext }));
    }
    if (list.length > 0) {
      parts.push(t("instanceAdmin.journal.settings_changed", { ext, keys: list.join(", ") }));
    }
    return parts.join(". ");
  }
  if (e.action === "allow_changed") {
    return t(`instanceAdmin.journal.allow_changed_${e.details.allowed ? "on" : "off"}`, { ext });
  }
  if (e.action === "auto_update_changed") {
    return t(`instanceAdmin.journal.auto_update_changed_${e.details.auto_update ? "on" : "off"}`);
  }
  return t("instanceAdmin.journal.other", { action: e.action, ext });
}

function Journal({ entries, names }: { entries: InstanceJournalEntry[]; names: Map<string, string> }) {
  const { t } = useTranslation("integrations");
  return (
    <Card data-testid="journal">
      <CardHeader>
        <CardTitle>{t("instanceAdmin.journal.title")}</CardTitle>
        <CardDescription>{t("instanceAdmin.journal.body")}</CardDescription>
      </CardHeader>
      <CardContent>
        {entries.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("instanceAdmin.journal.empty")}</p>
        ) : (
          <ol className="divide-y divide-border">
            {entries.map((e) => (
              <li key={e.id} className="flex flex-wrap items-baseline justify-between gap-2 py-2 text-sm">
                <span>
                  <span className="font-medium">{e.actor_name || t("instanceAdmin.journal.unknownActor")}</span>
                  {" · "}
                  {journalLine(t, e, names)}
                </span>
                <time dateTime={e.created_at} className="text-xs text-muted-foreground">
                  {formatDateTime(e.created_at)}
                </time>
              </li>
            ))}
          </ol>
        )}
      </CardContent>
    </Card>
  );
}

export function ExtensionInstanceSettingsPage() {
  const { t } = useTranslation("integrations");
  const { wsSlug } = useParams();
  const [state, setState] = useState<LoadState>("loading");
  const [overview, setOverview] = useState<InstanceOverview | null>(null);
  const [journal, setJournal] = useState<InstanceJournalEntry[]>([]);
  const [summaries, setSummaries] = useState<InstanceExtensionSummary[]>([]);
  const [autoBusy, setAutoBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const [ov, jr, sm] = await Promise.all([getInstanceOverview(), getInstanceJournal(), listExtensionSummaries()]);
      setOverview(ov);
      setJournal(jr.entries);
      setSummaries(sm.extensions);
      setState("ready");
    } catch (err) {
      setState(err instanceof ApiRequestError && err.status === 403 ? "forbidden" : "error");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const refreshJournal = useCallback(() => {
    void getInstanceJournal().then((jr) => setJournal(jr.entries), () => undefined);
    void getInstanceOverview().then(setOverview, () => undefined);
  }, []);

  const toggleAuto = async (v: boolean) => {
    setAutoBusy(true);
    try {
      await setInstanceAutoUpdate(v);
      toast.success(t("instanceAdmin.allow.saved"));
      refreshJournal();
    } catch {
      toast.error(t("instanceAdmin.allow.saveFailed"));
    } finally {
      setAutoBusy(false);
    }
  };

  const names = new Map(overview?.items.map((i) => [i.id, i.name]) ?? []);

  return (
    <div className="mx-auto flex h-full w-full max-w-4xl flex-col gap-4 overflow-y-auto pb-8">
      <div>
        <Link to={`/w/${wsSlug}/extensions`} className="mb-2 inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
          <ArrowLeft className="h-4 w-4" aria-hidden />
          {t("instanceAdmin.back")}
        </Link>
        <h1 className="text-xl font-semibold">{t("instanceAdmin.title")}</h1>
        <p className="max-w-prose text-sm text-muted-foreground">{t("instanceAdmin.intro")}</p>
      </div>

      {state === "loading" && (
        <div role="status" aria-label={t("instanceAdmin.loading")} className="space-y-3">
          <Skeleton className="h-24 w-full rounded-xl" />
          <Skeleton className="h-40 w-full rounded-xl" />
        </div>
      )}

      {(state === "forbidden" || state === "error") && (
        <div role={state === "error" ? "alert" : undefined} data-testid={`state-${state}`} className="flex flex-col items-center gap-2 rounded-xl border border-dashed border-border px-6 py-16 text-center">
          {state === "forbidden" ? <Lock className="h-8 w-8 text-muted-foreground" aria-hidden /> : <AlertTriangle className="h-8 w-8 text-muted-foreground" aria-hidden />}
          <p className="text-sm font-medium">{t(state === "forbidden" ? "instanceAdmin.noAccessTitle" : "instanceAdmin.errorTitle")}</p>
          <p className="max-w-prose text-sm text-muted-foreground">{t(state === "forbidden" ? "instanceAdmin.noAccessBody" : "instanceAdmin.errorBody")}</p>
          {state === "error" && (
            <Button variant="outline" size="sm" onClick={() => { setState("loading"); void load(); }}>
              {t("instanceAdmin.retry")}
            </Button>
          )}
        </div>
      )}

      {state === "ready" && overview && (
        <>
          <Card data-testid="auto-update">
            <CardHeader>
              <CardTitle>{t("instanceAdmin.autoUpdate.title")}</CardTitle>
              <CardDescription>{t("instanceAdmin.autoUpdate.body")}</CardDescription>
            </CardHeader>
            <CardContent>
              <Switch checked={overview.auto_update} disabled={autoBusy} onChange={(v) => void toggleAuto(v)} label={t("instanceAdmin.autoUpdate.label")} />
            </CardContent>
          </Card>
          <AllowList overview={overview} onChange={refreshJournal} />
          <SettingsSection overview={overview} summaries={summaries} onSaved={refreshJournal} />
          <Journal entries={journal} names={names} />
        </>
      )}
    </div>
  );
}
