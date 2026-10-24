import { Fragment } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { ChevronRight, Download, Menu } from "lucide-react";
import { cn } from "@/lib/cn";
import { Button } from "@/components/ui/button";
import { navIcon } from "../nav-icons";
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
      <Link to={crumb.href} className={cn(LINK_CLASS[crumb.kind], crumb.strong && "font-medium text-foreground")} {...aria}>
        {crumb.label}
      </Link>
    );
  }
  const Icon = crumb.icon ? navIcon(crumb.icon) : null;
  return (
    <span className={cn("flex min-w-0 items-center gap-1.5", crumb.strong && "font-medium text-foreground")} {...aria}>
      {Icon && <Icon className="h-3.5 w-3.5" />}
      {crumb.label}
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
            <Fragment key={`${crumb.kind}:${crumb.label}`}>
              {i > 0 && <ChevronRight className="h-3 w-3 shrink-0" />}
              <Crumb crumb={crumb} last={i === crumbs.length - 1} />
            </Fragment>
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
