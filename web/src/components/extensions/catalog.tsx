import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, useParams, useSearchParams } from "react-router";
import { AlertTriangle, PackageSearch, Palette, Plug, Puzzle, Search } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import type { CatalogEntry, CatalogState } from "@/hooks/useExtensionCatalog";
import {
  CATEGORIES, KINDS, SORTS, TRUSTS, applyFilters, filtersFromParams, filtersToParams,
  type CatalogFilters,
} from "@/lib/extension-catalog-filter";
import { useIsMobile } from "@/lib/use-is-mobile";
import { cn } from "@/lib/cn";

const TRUST_VARIANT = { official: "default", verified: "success", community: "secondary", custom: "warning" } as const;

export function KindTrustBadges({ entry }: { entry: Pick<CatalogEntry, "kind" | "trust"> }) {
  const { t } = useTranslation("integrations");
  return (
    <>
      <Badge variant="outline">{t(`catalog.kind.${entry.kind}`)}</Badge>
      <Badge variant={TRUST_VARIANT[entry.trust]}>{t(`catalog.trust.${entry.trust}`)}</Badge>
    </>
  );
}

export function ExtensionIcon({ entry, className }: { entry: Pick<CatalogEntry, "kind">; className?: string }) {
  const Icon = entry.kind === "plugin" ? Puzzle : entry.kind === "theme" ? Palette : Plug;
  return (
    <span className={cn("flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-muted text-muted-foreground", className)}>
      <Icon className="h-5 w-5" aria-hidden />
    </span>
  );
}

function CheckGroup({
  title, name, options, selected, label, onToggle,
}: {
  title: string; name: string; options: readonly string[]; selected: string[];
  label: (o: string) => string; onToggle: (o: string) => void;
}) {
  return (
    <fieldset className="space-y-1.5">
      <legend className="mb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">{title}</legend>
      {options.map((o) => (
        <label key={o} className="flex min-h-8 cursor-pointer items-center gap-2 text-sm">
          <input
            type="checkbox"
            name={name}
            value={o}
            checked={selected.includes(o)}
            onChange={() => onToggle(o)}
            className="h-4 w-4 accent-primary"
          />
          {label(o)}
        </label>
      ))}
    </fieldset>
  );
}

function StatusChip({ entry }: { entry: CatalogEntry }) {
  const { t } = useTranslation("integrations");
  if (!entry.connected) return null;
  return (
    <Badge variant="success">
      {t(entry.status?.source === "instance" ? "catalog.state.connected" : "catalog.state.installed")}
    </Badge>
  );
}

function Card({ entry, wsSlug }: { entry: CatalogEntry; wsSlug: string }) {
  const { t } = useTranslation("integrations");
  const notAllowed = !entry.allowed;
  return (
    <li>
      <Link
        to={`/w/${wsSlug}/extensions/${entry.id}`}
        className="flex h-full flex-col gap-3 rounded-xl border border-border bg-card p-4 transition-colors hover:border-primary/50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary"
      >
        <div className="flex items-start gap-3">
          <ExtensionIcon entry={entry} />
          <div className="min-w-0">
            <p className="truncate text-sm font-semibold">{entry.name}</p>
            <p className="truncate text-xs text-muted-foreground">{entry.publisher}</p>
          </div>
        </div>
        <p className="line-clamp-2 text-sm text-muted-foreground">{entry.description}</p>
        <div className="mt-auto flex flex-wrap items-center gap-1.5">
          <KindTrustBadges entry={entry} />
          <StatusChip entry={entry} />
          {entry.blocked && <Badge variant="destructive">{t("catalog.state.blocked")}</Badge>}
          {notAllowed && <Badge variant="warning">{t("catalog.state.notAllowed")}</Badge>}
        </div>
      </Link>
    </li>
  );
}

export function ExtensionsCatalog({ catalog }: { catalog: CatalogState }) {
  const { t } = useTranslation("integrations");
  const { wsSlug = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const isMobile = useIsMobile();
  // Local state is the source of truth while typing: router updates land in a
  // transition, and a controlled input read straight from the URL lags a click
  // and drops fast keystrokes. The URL mirrors it so a filtered view can be linked.
  const [filters, setFilters] = useState<CatalogFilters>(() => filtersFromParams(params));

  const update = (next: Partial<CatalogFilters>) => {
    const merged = { ...filters, ...next };
    setFilters(merged);
    setParams(filtersToParams(params, merged), { replace: true });
  };
  const toggle = (key: "kinds" | "categories" | "trusts") => (o: string) =>
    update({ [key]: filters[key].includes(o) ? filters[key].filter((x) => x !== o) : [...filters[key], o] });

  if (catalog.state === "loading") {
    return (
      <div role="status" aria-label={t("extensionsPage.catalog.loading")} className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {[0, 1, 2].map((i) => <Skeleton key={i} className="h-36 w-full rounded-xl" />)}
      </div>
    );
  }
  if (catalog.state === "error") {
    return (
      <div role="alert" className="flex flex-col items-center gap-2 rounded-xl border border-dashed border-border px-6 py-16 text-center">
        <AlertTriangle className="h-8 w-8 text-muted-foreground" aria-hidden />
        <p className="text-sm font-medium">{t("extensionsPage.catalog.errorTitle")}</p>
        <p className="max-w-prose text-sm text-muted-foreground">{t("extensionsPage.catalog.errorBody")}</p>
        <Button variant="outline" size="sm" onClick={catalog.reload}>{t("extensionsPage.catalog.retry")}</Button>
      </div>
    );
  }

  const shown = applyFilters(catalog.entries, filters);
  const active = filters.kinds.length + filters.categories.length + filters.trusts.length + (filters.q ? 1 : 0);

  const panel = (
    <div className="space-y-5">
      <CheckGroup title={t("catalog.filters.kind")} name="kind" options={KINDS} selected={filters.kinds}
        label={(o) => t(o === "theme" ? "catalog.filters.kindTheme" : `catalog.kind.${o}`)} onToggle={toggle("kinds")} />
      <CheckGroup title={t("catalog.filters.category")} name="category" options={CATEGORIES} selected={filters.categories}
        label={(o) => t(`catalog.category.${o}`)} onToggle={toggle("categories")} />
      <CheckGroup title={t("catalog.filters.trust")} name="trust" options={TRUSTS} selected={filters.trusts}
        label={(o) => t(`catalog.trust.${o}`)} onToggle={toggle("trusts")} />
    </div>
  );

  return (
    <div className="space-y-4">
      {catalog.catalogUnavailable && (
        <div role="status" className="flex items-center gap-2 rounded-lg border border-warning/40 bg-warning/10 px-3 py-2 text-sm">
          <AlertTriangle className="h-4 w-4 shrink-0" aria-hidden />
          {t("catalog.unavailableBanner")}
        </div>
      )}
      <div className="flex flex-wrap items-center gap-3">
        <div className="relative min-w-0 flex-1 basis-56">
          <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" aria-hidden />
          <Input
            type="search"
            value={filters.q}
            onChange={(e) => update({ q: e.target.value })}
            placeholder={t("catalog.search")}
            aria-label={t("catalog.search")}
            className="pl-9"
          />
        </div>
        <label className="flex items-center gap-2 text-sm text-muted-foreground">
          {t("catalog.sort.label")}
          <select
            value={filters.sort}
            onChange={(e) => update({ sort: e.target.value as CatalogFilters["sort"] })}
            className="h-9 rounded-md border border-input bg-background px-2 text-sm text-foreground"
          >
            {SORTS.map((s) => <option key={s} value={s}>{t(`catalog.sort.${s}`)}</option>)}
          </select>
        </label>
      </div>

      <div className="grid gap-6 lg:grid-cols-[14rem_1fr]">
        <aside aria-label={t("catalog.filters.label")}>
          {isMobile ? (
            <details className="rounded-lg border border-border p-3">
              <summary className="cursor-pointer text-sm font-medium">
                {t("catalog.filters.label")}{active > 0 ? ` (${active})` : ""}
              </summary>
              <div className="mt-3">{panel}</div>
            </details>
          ) : panel}
        </aside>

        <section aria-label={t("catalog.results")}>
          <p className="mb-3 text-sm text-muted-foreground" aria-live="polite" data-testid="catalog-count">
            {t("catalog.count", { count: shown.length })}
          </p>
          {shown.length === 0 ? (
            <div className="flex flex-col items-center gap-2 rounded-xl border border-dashed border-border px-6 py-16 text-center">
              <PackageSearch className="h-8 w-8 text-muted-foreground" aria-hidden />
              <p className="text-sm font-medium">
                {t(catalog.entries.length === 0 ? "extensionsPage.catalog.emptyTitle" : "catalog.noMatchTitle")}
              </p>
              <p className="max-w-prose text-sm text-muted-foreground">
                {t(catalog.entries.length === 0 ? "extensionsPage.catalog.emptyBody" : "catalog.noMatchBody")}
              </p>
              {active > 0 && (
                <Button variant="outline" size="sm"
                  onClick={() => update({ kinds: [], categories: [], trusts: [], q: "" })}>
                  {t("catalog.reset")}
                </Button>
              )}
            </div>
          ) : (
            <ul className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
              {shown.map((e) => <Card key={e.id} entry={e} wsSlug={wsSlug} />)}
            </ul>
          )}
        </section>
      </div>
    </div>
  );
}
