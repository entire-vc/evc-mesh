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
import { extensionState } from "@/lib/extensionState";
import { ExtensionStateBadge, ExtensionStateNote } from "@/components/extensions/extension-state";
import { cn } from "@/lib/cn";

const TRUST_VARIANT = { builtin: "secondary", official: "default", verified: "success", community: "secondary", custom: "warning" } as const;

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
  const theme = entry.kind === "theme";
  const Icon = entry.kind === "plugin" ? Puzzle : theme ? Palette : Plug;
  return (
    // a theme carries its own tinted palette icon so it reads as a theme at a glance
    <span
      data-kind={entry.kind}
      className={cn(
        "flex h-10 w-10 shrink-0 items-center justify-center rounded-lg",
        theme ? "bg-primary/10 text-primary" : "bg-muted text-muted-foreground",
        className,
      )}
    >
      <Icon className="h-5 w-5" aria-hidden />
    </span>
  );
}

/** Themes are listed in a section of their own after everything else (П8); `id` names it for tests and links. */
export function KindSections<T extends Pick<CatalogEntry, "kind">>({
  entries, children, className,
}: {
  entries: T[]; children: (items: T[]) => React.ReactNode; className?: string;
}) {
  const { t } = useTranslation("integrations");
  const themes = entries.filter((e) => e.kind === "theme");
  const rest = entries.filter((e) => e.kind !== "theme");
  if (themes.length === 0) return <>{children(rest)}</>;
  return (
    <div className={cn("space-y-6", className)}>
      {rest.length > 0 && (
        <section aria-labelledby="ext-section-extensions" className="space-y-3">
          <h2 id="ext-section-extensions" className="flex items-center gap-2 text-sm font-semibold">
            <Puzzle className="h-4 w-4 text-muted-foreground" aria-hidden />
            {t("catalog.sections.extensions")}
            <span className="font-normal text-muted-foreground">{rest.length}</span>
          </h2>
          {children(rest)}
        </section>
      )}
      <section aria-labelledby="ext-section-themes" data-testid="section-themes" className="space-y-3">
        <div>
          <h2 id="ext-section-themes" className="flex items-center gap-2 text-sm font-semibold">
            <Palette className="h-4 w-4 text-primary" aria-hidden />
            {t("catalog.sections.themes")}
            <span className="font-normal text-muted-foreground">{themes.length}</span>
          </h2>
          <p className="mt-0.5 text-xs text-muted-foreground">{t("catalog.sections.themesHint")}</p>
        </div>
        {children(themes)}
      </section>
    </div>
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

function Card({ entry, wsSlug }: { entry: CatalogEntry; wsSlug: string }) {
  const state = extensionState(entry);
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
          <ExtensionStateBadge state={state} />
        </div>
        <ExtensionStateNote state={state} className="-mt-1 line-clamp-2" />
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

  // Built-in parts of Mesh (Runner) stay in the catalog state but are not listed.
  const listed = catalog.entries.filter((e) => !e.hidden);
  const shown = applyFilters(listed, filters);
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
                {t(listed.length === 0 ? "extensionsPage.catalog.emptyTitle" : "catalog.noMatchTitle")}
              </p>
              <p className="max-w-prose text-sm text-muted-foreground">
                {t(listed.length === 0 ? "extensionsPage.catalog.emptyBody" : "catalog.noMatchBody")}
              </p>
              {active > 0 && (
                <Button variant="outline" size="sm"
                  onClick={() => update({ kinds: [], categories: [], trusts: [], q: "" })}>
                  {t("catalog.reset")}
                </Button>
              )}
            </div>
          ) : (
            <KindSections entries={shown}>
              {(items) => (
                <ul className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
                  {items.map((e) => <Card key={e.id} entry={e} wsSlug={wsSlug} />)}
                </ul>
              )}
            </KindSections>
          )}
        </section>
      </div>
    </div>
  );
}
