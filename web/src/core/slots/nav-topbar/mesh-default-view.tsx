import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { ChevronRight, Download, Menu } from "lucide-react";
import { cn } from "@/lib/cn";
import { Button } from "@/components/ui/button";
import { EntityIcon } from "@/components/ui/entity-icon";
import { navIcon } from "@/core/icons/nav-icons";
import type { NavTopbarView, TopbarCrumb } from "./types";

// Per-kind sizing of a crumb, as the header has always drawn it.
const LINK_CLASS: Record<TopbarCrumb["kind"], string> = {
  workspace: "min-w-0 shrink truncate max-w-[80px] sm:max-w-[160px] hover:text-foreground transition-colors",
  project: "min-w-0 shrink truncate max-w-[100px] sm:max-w-none hover:text-foreground transition-colors",
  page: "",
};

function Crumb({ crumb, last }: { crumb: TopbarCrumb; last: boolean }) {
  const aria = last ? ({ "aria-current": "page" } as const) : {};
  if (crumb.href) {
    return (
      <Link to={crumb.href} className={cn(LINK_CLASS[crumb.kind], crumb.entityIcon && "inline-flex items-center gap-1.5", crumb.strong && "font-medium text-foreground")} {...aria}>
        {crumb.entityIcon && <EntityIcon value={crumb.entityIcon} className="h-3.5 w-3.5 shrink-0" />}
        {crumb.entityIcon ? <span className="min-w-0 truncate">{crumb.label}</span> : crumb.label}
      </Link>
    );
  }
  const Icon = crumb.icon ? navIcon(crumb.icon) : null;
  return (
    <span className={cn("flex min-w-0 items-center gap-1.5", crumb.strong && "font-medium text-foreground")} {...aria}>
      {/* An entity's own icon (a dashboard's emoji) wins; the page's lucide icon is the fallback. */}
      {crumb.entityIcon ? (
        <EntityIcon value={crumb.entityIcon} className="h-3.5 w-3.5 shrink-0" />
      ) : (
        Icon && <Icon className="h-3.5 w-3.5 shrink-0" />
      )}
      {crumb.entityIcon ? <span className="min-w-0 truncate">{crumb.label}</span> : crumb.label}
    </span>
  );
}

export const MeshDefaultNavTopbar: NavTopbarView = ({ data, actions, themeSettings }) => {
  const { t } = useTranslation("shell");
  const { NotificationBell, UserMenu, ViewTabs, TeamTabs, Search } = data.components;
  const crumbs = data.breadcrumbs;
  return (
    <header className="mesh-mobile-header flex h-14 items-center gap-2 sm:gap-4 border-b border-border bg-background px-2 sm:px-4">
      <Button
        variant="ghost"
        size="icon"
        onClick={() => actions.toggleSidebar()}
        aria-label={t("header.toggleSidebar")}
        className="shrink-0"
      >
        <Menu className="h-4 w-4" />
      </Button>

      {/* Breadcrumbs */}
      {themeSettings.show_breadcrumbs !== false && (
        <nav aria-label={t("header.breadcrumbs")} className="flex min-w-0 items-center gap-1 text-sm text-muted-foreground">
          {crumbs.map((crumb, i) => (
            // One box per crumb with its chevron, laid out as if it were not there (display: contents):
            // a crumb that comes or goes is one DOM change, not two (perf: board.open.dom_mutations).
            <span key={`${crumb.kind}:${crumb.label}`} className="contents">
              {i > 0 && <ChevronRight className="h-3 w-3 shrink-0" />}
              <Crumb crumb={crumb} last={i === crumbs.length - 1} />
            </span>
          ))}
        </nav>
      )}

      <ViewTabs />
      <TeamTabs />

      <div className="flex-1" />

      <Search />

      <NotificationBell />

      {/* PWA install — desktop only, non-intrusive */}
      {data.installable && actions.install && (
        <Button
          variant="ghost"
          size="icon"
          onClick={() => actions.install?.()}
          title={t("header.installApp")}
          aria-label={t("header.installApp")}
          className="hidden md:inline-flex"
        >
          <Download className="h-4 w-4" />
        </Button>
      )}

      <UserMenu />
    </header>
  );
};
