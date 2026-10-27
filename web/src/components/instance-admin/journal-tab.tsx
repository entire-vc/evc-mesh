import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { formatDateTime } from "@/i18n/format";
import {
  getInstanceJournal,
  type InstanceJournalEntry,
  type InstanceJournalFacets,
  type InstanceJournalFilter,
} from "@/lib/extension-instance";

const LIMIT = 200;

type T = (k: string, o?: Record<string, unknown>) => string;

export function actionLabel(t: T, action: string) {
  const key = `instanceAdmin.journal.actions.${action}`;
  const label = t(key);
  return label === key ? action : label;
}

export function journalLine(t: T, e: InstanceJournalEntry, names: Map<string, string>) {
  const ext = names.get(e.extension_id) ?? e.extension_id;
  const d = e.details ?? {};
  switch (e.action) {
    case "settings_changed": {
      const list = Array.isArray(d.keys) ? (d.keys as string[]) : [];
      const parts: string[] = [];
      if (typeof d.enabled === "boolean") parts.push(t(`instanceAdmin.journal.enabled_changed_${d.enabled ? "on" : "off"}`, { ext }));
      if (list.length > 0) parts.push(t("instanceAdmin.journal.settings_changed", { ext, keys: list.join(", ") }));
      return parts.join(". ");
    }
    case "allow_changed":
      return t(`instanceAdmin.journal.allow_changed_${d.allowed ? "on" : "off"}`, { ext });
    case "auto_update_changed":
      return t(`instanceAdmin.journal.auto_update_changed_${d.auto_update ? "on" : "off"}`);
    case "installed":
      return t("instanceAdmin.journal.installed", { ext, version: d.version ?? "" });
    case "updated":
      return t("instanceAdmin.journal.updated", { ext, from: d.from ?? "", to: d.to ?? "" });
    case "rolled_back":
      return t("instanceAdmin.journal.rolled_back", { ext, from: d.from ?? "", to: d.to ?? "" });
    case "update_pending_consent":
      return t("instanceAdmin.journal.update_pending_consent", { ext });
    case "theme_selected":
      return t(d.level === "workspace" ? "instanceAdmin.journal.theme_selected_workspace" : "instanceAdmin.journal.theme_selected_instance", { ext });
    case "theme_deleted":
      return t("instanceAdmin.journal.theme_deleted", { ext });
    default:
      return t("instanceAdmin.journal.other", { action: e.action, ext });
  }
}

/** The day after a YYYY-MM-DD: the "to" date in the form is inclusive, the API bound is not. */
function nextDay(day: string) {
  const d = new Date(`${day}T00:00:00Z`);
  if (Number.isNaN(d.getTime())) return "";
  d.setUTCDate(d.getUTCDate() + 1);
  return d.toISOString().slice(0, 10);
}

type Load = "loading" | "ready" | "error";

export function JournalTab({ names }: { names: Map<string, string> }) {
  const { t } = useTranslation("integrations");
  const [filter, setFilter] = useState<InstanceJournalFilter>({});
  const [to, setTo] = useState("");
  const [entries, setEntries] = useState<InstanceJournalEntry[]>([]);
  const [facets, setFacets] = useState<InstanceJournalFacets | null>(null);
  const [state, setState] = useState<Load>("loading");
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let live = true;
    setState("loading");
    getInstanceJournal({ ...filter, until: to ? nextDay(to) : undefined }, { limit: LIMIT, facets: facets === null })
      .then((page) => {
        if (!live) return;
        setEntries(page.entries);
        if (page.facets) setFacets(page.facets);
        setState("ready");
      })
      .catch(() => live && setState("error"));
    return () => {
      live = false;
    };
    // facets are fetched once with the first page; they do not drive reloads
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filter, to, attempt]);

  const set = (k: keyof InstanceJournalFilter) => (v: string) => setFilter((f) => ({ ...f, [k]: v || undefined }));
  const filtered = Object.values(filter).some(Boolean) || to !== "";
  const reset = () => {
    setFilter({});
    setTo("");
  };

  // Facets list every value ever written; the current names come from the overview.
  const extensions = (facets?.extensions ?? []).map((id) => ({ id, name: names.get(id) ?? id })).sort((a, b) => a.name.localeCompare(b.name));

  return (
    <div className="space-y-4" data-testid="journal">
      <p className="max-w-prose text-sm text-muted-foreground">{t("instanceAdmin.journal.body")}</p>
      <div className="grid grid-cols-2 gap-3 lg:flex lg:flex-wrap lg:items-end" role="group" aria-label={t("instanceAdmin.journal.filters")}>
        <label className="space-y-1 text-xs text-muted-foreground lg:w-52">
          <span>{t("instanceAdmin.journal.filterExtension")}</span>
          <Select value={filter.extension ?? ""} onChange={(e) => set("extension")(e.target.value)}>
            <option value="">{t("instanceAdmin.journal.any")}</option>
            {extensions.map((x) => (
              <option key={x.id} value={x.id}>
                {x.name}
              </option>
            ))}
          </Select>
        </label>
        <label className="space-y-1 text-xs text-muted-foreground lg:w-52">
          <span>{t("instanceAdmin.journal.filterAction")}</span>
          <Select value={filter.action ?? ""} onChange={(e) => set("action")(e.target.value)}>
            <option value="">{t("instanceAdmin.journal.any")}</option>
            {(facets?.actions ?? []).map((a) => (
              <option key={a} value={a}>
                {actionLabel(t, a)}
              </option>
            ))}
          </Select>
        </label>
        <label className="col-span-2 space-y-1 text-xs text-muted-foreground sm:col-span-1 lg:w-48">
          <span>{t("instanceAdmin.journal.filterActor")}</span>
          <Select value={filter.actor ?? ""} onChange={(e) => set("actor")(e.target.value)}>
            <option value="">{t("instanceAdmin.journal.any")}</option>
            {(facets?.actors ?? []).map((a) => (
              <option key={a.id} value={a.id}>
                {a.name || t("instanceAdmin.journal.unknownActor")}
              </option>
            ))}
          </Select>
        </label>
        <label className="space-y-1 text-xs text-muted-foreground lg:w-40">
          <span>{t("instanceAdmin.journal.filterFrom")}</span>
          <Input type="date" value={filter.since ?? ""} max={to || undefined} onChange={(e) => set("since")(e.target.value)} />
        </label>
        <label className="space-y-1 text-xs text-muted-foreground lg:w-40">
          <span>{t("instanceAdmin.journal.filterTo")}</span>
          <Input type="date" value={to} min={filter.since || undefined} onChange={(e) => setTo(e.target.value)} />
        </label>
        {filtered && (
          <Button variant="ghost" size="sm" onClick={reset} className="justify-self-start">
            {t("instanceAdmin.filters.reset")}
          </Button>
        )}
      </div>

      <div className="rounded-xl border border-border" aria-busy={state === "loading"}>
        {state === "loading" && (
          <div role="status" aria-label={t("instanceAdmin.journal.loading")} className="space-y-2 p-4">
            <Skeleton className="h-5 w-full" />
            <Skeleton className="h-5 w-5/6" />
            <Skeleton className="h-5 w-2/3" />
          </div>
        )}
        {state === "error" && (
          <div role="alert" className="flex flex-col items-center gap-2 px-6 py-10 text-center">
            <p className="text-sm font-medium">{t("instanceAdmin.journal.loadFailed")}</p>
            <Button variant="outline" size="sm" onClick={() => setAttempt((n) => n + 1)}>
              {t("instanceAdmin.retry")}
            </Button>
          </div>
        )}
        {state === "ready" && entries.length === 0 && (
          <div className="flex flex-col items-center gap-2 px-6 py-10 text-center">
            <p className="text-sm text-muted-foreground">{t(filtered ? "instanceAdmin.journal.noMatch" : "instanceAdmin.journal.empty")}</p>
            {filtered && (
              <Button variant="outline" size="sm" onClick={reset}>
                {t("instanceAdmin.filters.reset")}
              </Button>
            )}
          </div>
        )}
        {state === "ready" && entries.length > 0 && (
          <ol className="divide-y divide-border">
            {entries.map((e) => (
              <li key={e.id} className="flex flex-col gap-1 px-4 py-2.5 text-sm sm:flex-row sm:items-baseline sm:justify-between sm:gap-4">
                <span className="min-w-0 break-words">
                  <span className="font-medium">{e.actor_name || t("instanceAdmin.journal.unknownActor")}</span>
                  {" · "}
                  {journalLine(t, e, names)}
                </span>
                <time dateTime={e.created_at} className="shrink-0 text-xs text-muted-foreground">
                  {formatDateTime(e.created_at)}
                </time>
              </li>
            ))}
          </ol>
        )}
      </div>
      {state === "ready" && entries.length >= LIMIT && <p className="text-xs text-muted-foreground">{t("instanceAdmin.journal.capped", { count: LIMIT })}</p>}
    </div>
  );
}
