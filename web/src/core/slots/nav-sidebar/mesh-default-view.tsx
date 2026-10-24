import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { AlertTriangle, ArchiveRestore, ChevronDown, ChevronRight, FolderKanban, Plus } from "lucide-react";
import { cn } from "@/lib/cn";
import {
  PROJECT_RAIL_ICON_LABEL,
  WORKSPACE_LOGO_ICON_FILL,
  RAIL_COLLAPSED_ASIDE,
  RAIL_COLLAPSED_HEADER,
  RAIL_COLLAPSED_NAV,
  projectRailIconParts,
  workspaceLogoContainerParts,
} from "@/components/layout/rail-icon-classes";
import type { WorkspaceLogoVariant } from "@/components/layout/rail-icon-classes";
import { MeshIcon } from "@/components/mesh-icon";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Separator } from "@/components/ui/separator";
import { navIcon } from "../nav-icons";
import type { NavItem, NavProject, NavSidebarView } from "./types";

// The sizing/tint recipes this view renders live in `rail-icon-classes.ts`
// — an import-free module so the contrast guard
// (`scripts/assert-rail-icon-contrast.mjs`) can measure the SAME definitions
// rather than restating them and drifting out of sync. The reasoning behind
// each rule is documented there. See an internal ticket.

type ImageStatus = "pending" | "loaded" | "error";

interface WorkspaceLogoProps {
  iconUrl: string | null | undefined;
  name: string | undefined;
  variant: WorkspaceLogoVariant;
}

/**
 * The workspace mark: the uploaded image once it has loaded (bare, so a logo
 * with transparent regions never shows the tint behind it), otherwise the
 * generic mark in the tinted box — while the image is loading, when it
 * failed, and when there is none (an internal ticket, an internal ticket).
 */
function WorkspaceLogo({ iconUrl, name, variant }: WorkspaceLogoProps) {
  const { t } = useTranslation("shell");
  const [status, setStatus] = useState<ImageStatus>("pending");

  // Re-arm on URL change (workspace switch, or a fresh upload replacing the
  // old icon) so a stale loaded/error flag from the previous image can never
  // survive onto this one.
  useEffect(() => {
    setStatus("pending");
  }, [iconUrl]);

  const isLoaded = Boolean(iconUrl) && status === "loaded";

  return (
    <div
      data-testid="workspace-logo"
      data-variant={variant}
      data-status={iconUrl ? status : "none"}
      className={cn(workspaceLogoContainerParts({ variant, isLoaded, className: undefined }))}
    >
      {iconUrl && (
        <img
          data-testid="workspace-logo-img"
          src={iconUrl}
          alt={name || t("sidebar.workspaceLogoAlt")}
          className={cn("h-full w-full object-cover", !isLoaded && "hidden")}
          onLoad={() => setStatus("loaded")}
          onError={() => setStatus("error")}
        />
      )}
      {!isLoaded && <MeshIcon className={WORKSPACE_LOGO_ICON_FILL} />}
    </div>
  );
}

const RAIL_LINK = "flex h-8 w-8 items-center justify-center rounded-lg text-sidebar-foreground hover:bg-sidebar-accent";
const RAIL_LINK_ACTIVE = "bg-sidebar-accent text-sidebar-primary";
const ROW_LINK = "flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm text-sidebar-foreground hover:bg-sidebar-accent";
const ROW_LINK_ACTIVE = "bg-sidebar-accent font-medium";

// Whole class strings per tone, so the attention badge stays byte-for-byte what it was.
const ROW_BADGE: Record<"info" | "attention", string> = {
  attention: "ml-auto flex h-5 min-w-5 items-center justify-center rounded-full bg-tone-danger-500 px-1 text-[10px] font-bold text-white",
  info: "ml-auto flex h-5 min-w-5 items-center justify-center rounded-full bg-sidebar-primary px-1 text-[10px] font-bold text-primary-foreground",
};
const RAIL_BADGE: Record<"info" | "attention", string> = {
  attention: "absolute -right-0.5 -top-0.5 flex h-4 w-4 items-center justify-center rounded-full bg-tone-danger-500 text-[10px] font-bold text-white",
  info: "absolute -right-0.5 -top-0.5 flex h-4 w-4 items-center justify-center rounded-full bg-sidebar-primary text-[10px] font-bold text-primary-foreground",
};

function RailItem({ item }: { item: NavItem }) {
  const { t } = useTranslation("shell");
  const Icon = navIcon(item.icon);
  const count = item.badge?.count ?? 0;
  return (
    <Link
      to={item.href}
      aria-label={item.label}
      title={item.label}
      aria-current={item.active ? "page" : undefined}
      data-slot-origin={item.origin}
      className={cn(item.badge != null && "relative", RAIL_LINK, item.active && RAIL_LINK_ACTIVE)}
    >
      <Icon className="h-4 w-4" />
      {item.badge && count > 0 && (
        <span
          aria-label={t("sidebar.unread", { count })}
          className={RAIL_BADGE[item.badge.tone]}
        >
          {count > 9 ? "9+" : count}
        </span>
      )}
    </Link>
  );
}

function RowItem({ item }: { item: NavItem }) {
  const { t } = useTranslation("shell");
  const Icon = navIcon(item.icon);
  const count = item.badge?.count ?? 0;
  return (
    <Link
      to={item.href}
      aria-current={item.active ? "page" : undefined}
      data-slot-origin={item.origin}
      className={cn(ROW_LINK, item.active && ROW_LINK_ACTIVE)}
    >
      <Icon className="h-4 w-4" />
      {item.origin === "extension" ? <span className="truncate">{item.label}</span> : item.label}
      {item.badge && count > 0 && (
        <span
          aria-label={t("sidebar.unread", { count })}
          className={ROW_BADGE[item.badge.tone]}
        >
          {count > 99 ? "99+" : count}
        </span>
      )}
    </Link>
  );
}

function ProjectRailIcon({ project }: { project: NavProject }) {
  return (
    <Link
      to={project.href}
      data-testid="project-icon"
      aria-label={project.name}
      aria-current={project.active ? "page" : undefined}
      /* an internal ticket: a project's own muted chip, deliberately NOT
         `WorkspaceLogo` — that component's "no image" branch is the
         workspace's brand-teal tint, and reusing it here is what
         made every project tile in a 13-project workspace look like
         the workspace mark above them. See `rail-icon-classes.ts`. */
      className={cn(projectRailIconParts(project.active))}
    >
      <span className={PROJECT_RAIL_ICON_LABEL}>{project.icon || project.name.charAt(0).toUpperCase()}</span>
    </Link>
  );
}

export const MeshDefaultNavSidebar: NavSidebarView = ({ data, actions }) => {
  const { t } = useTranslation("shell");
  const [showArchived, setShowArchived] = useState(false);
  const { workspace, items, projects } = data;
  const top = items.filter((i) => i.group === "top");
  const inbox = items.filter((i) => i.group === "inbox");
  const tools = items.filter((i) => i.group === "tools");
  const footer = items.filter((i) => i.group === "footer");

  const toggle = (
    <button
      type="button"
      onClick={() => actions.toggleCollapsed()}
      className="sr-only rounded bg-sidebar-accent px-2 py-1 text-xs text-sidebar-foreground focus:not-sr-only focus:absolute focus:left-1 focus:top-1 focus:z-10"
    >
      {t("sidebar.toggle")}
    </button>
  );

  if (data.collapsed) {
    return (
      <aside className={RAIL_COLLAPSED_ASIDE}>
        {toggle}
        <div className={RAIL_COLLAPSED_HEADER}>
          <WorkspaceLogo iconUrl={workspace.logoUrl} name={workspace.name} variant="collapsed" />
        </div>
        <nav className={RAIL_COLLAPSED_NAV}>
          {top.map((item) => (
            <RailItem key={item.id} item={item} />
          ))}
          {projects.state === "loading" ? (
            <div
              data-testid="sidebar-projects-loading"
              aria-busy="true"
              aria-label={t("sidebar.projects.loading")}
              className="space-y-2"
            >
              <Skeleton className="h-8 w-8 rounded-lg bg-sidebar-border" />
              <Skeleton className="h-8 w-8 rounded-lg bg-sidebar-border" />
            </div>
          ) : projects.state === "error" ? (
            <div
              role="alert"
              aria-label={projects.error.message}
              title={projects.error.message}
              className="flex h-8 w-8 items-center justify-center rounded-lg text-destructive"
            >
              <AlertTriangle className="h-4 w-4" aria-hidden="true" />
            </div>
          ) : (
            projects.value.map((project) => <ProjectRailIcon key={project.id} project={project} />)
          )}
          <div className="my-1 w-6 border-t border-sidebar-border" />
          {inbox.filter((i) => i.inRail).map((item) => (
            <RailItem key={item.id} item={item} />
          ))}
          <div className="my-1 w-6 border-t border-sidebar-border" />
          {tools.filter((i) => i.inRail).map((item) => (
            <RailItem key={item.id} item={item} />
          ))}
        </nav>
      </aside>
    );
  }

  return (
    <aside className="flex h-full w-60 flex-col border-r border-sidebar-border bg-sidebar">
      {toggle}
      {/* Workspace switcher */}
      <div className="flex h-14 items-center border-b border-sidebar-border px-3">
        <DropdownMenu>
          <DropdownMenuTrigger className="flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-sm font-semibold text-sidebar-foreground hover:bg-sidebar-accent">
            <WorkspaceLogo iconUrl={workspace.logoUrl} name={workspace.name} variant="expanded" />
            <span className="flex-1 truncate text-left">
              {workspace.name || t("sidebar.workspaceSwitcher.select")}
            </span>
            <ChevronDown className="h-4 w-4 shrink-0 opacity-50" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="w-56">
            <DropdownMenuLabel>{t("sidebar.workspaceSwitcher.title")}</DropdownMenuLabel>
            <DropdownMenuSeparator />
            {data.workspaces.map((ws) => (
              <Link
                key={ws.id}
                to={ws.href}
                onClick={(e) => {
                  // A plain click goes through the container so it can react; a modified one
                  // (new tab, new window) stays the browser's.
                  if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
                  e.preventDefault();
                  actions.switchWorkspace(ws.slug);
                }}
              >
                <DropdownMenuItem
                  aria-current={ws.current ? "true" : undefined}
                  className={cn("gap-2", ws.current && "bg-accent text-accent-foreground")}
                >
                  <div className="flex h-5 w-5 shrink-0 items-center justify-center overflow-hidden rounded bg-sidebar-primary text-primary-foreground text-[10px] font-bold">
                    {ws.logoUrl ? (
                      <img src={ws.logoUrl} alt={ws.name} className="h-full w-full object-cover" />
                    ) : (
                      ws.name.charAt(0).toUpperCase()
                    )}
                  </div>
                  {ws.name}
                </DropdownMenuItem>
              </Link>
            ))}
            {actions.createWorkspace && (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuItem onClick={() => actions.createWorkspace?.()} className="gap-2 text-muted-foreground">
                  <Plus className="h-3.5 w-3.5" />
                  {t("sidebar.workspaceSwitcher.newWorkspace")}
                </DropdownMenuItem>
              </>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      {/* Navigation */}
      <nav className="flex-1 overflow-y-auto p-3">
        {top.map((item) => (
          <RowItem key={item.id} item={item} />
        ))}

        {/* Projects */}
        <div className="mt-3">
          <div className="flex items-center justify-between px-2 py-1">
            <span className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              {t("sidebar.projects.title")}
            </span>
            {actions.createProject && (
              <Button
                variant="ghost"
                size="icon"
                className="h-5 w-5"
                aria-label={t("sidebar.projects.create")}
                onClick={() => actions.createProject?.()}
              >
                <Plus className="h-3 w-3" />
              </Button>
            )}
          </div>
          <div className="mt-1 space-y-0.5">
            {projects.state === "loading" ? (
              <div
                data-testid="sidebar-projects-loading"
                aria-busy="true"
                aria-label={t("sidebar.projects.loading")}
                className="space-y-2 px-2 py-2"
              >
                <Skeleton className="h-6 w-full bg-sidebar-border" />
                <Skeleton className="h-6 w-2/3 bg-sidebar-border" />
              </div>
            ) : projects.state === "error" ? (
              <div className="px-2 py-4 text-center">
                <p role="alert" className="text-xs text-destructive">
                  {projects.error.message}
                </p>
              </div>
            ) : (
              projects.value.map((project) => (
                <Link
                  key={project.id}
                  to={project.href}
                  aria-current={project.active ? "page" : undefined}
                  className={cn(ROW_LINK, project.active && "bg-sidebar-accent font-medium text-sidebar-primary")}
                >
                  {project.icon ? (
                    <span className="flex h-4 w-4 shrink-0 items-center justify-center text-sm leading-none">
                      {project.icon}
                    </span>
                  ) : (
                    <FolderKanban className="h-4 w-4 shrink-0" />
                  )}
                  <span className="flex-1 truncate">{project.name}</span>
                </Link>
              ))
            )}
            {projects.state === "ready" && data.workspaceResolved && projects.value.length === 0 && (
              <p className="px-2 py-4 text-center text-xs text-muted-foreground">{t("sidebar.projects.empty")}</p>
            )}
          </div>
          {data.workspaceResolved && data.archivedProjects.length > 0 && (
            <div className="mt-1" data-testid="archived-projects">
              <button
                type="button"
                onClick={() => setShowArchived((v) => !v)}
                aria-expanded={showArchived}
                className="flex w-full items-center gap-1 rounded-lg px-2 py-1 text-xs text-muted-foreground hover:bg-sidebar-accent"
              >
                {showArchived ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
                {t("sidebar.projects.archived", { count: data.archivedProjects.length })}
              </button>
              {showArchived && (
                <div className="mt-0.5 space-y-0.5">
                  {data.archivedProjects.map((project) => (
                    <div
                      key={project.id}
                      className="group flex items-center gap-1 rounded-lg pr-1 hover:bg-sidebar-accent"
                    >
                      <Link
                        to={project.href}
                        aria-current={project.active ? "page" : undefined}
                        className={cn(
                          "flex min-w-0 flex-1 items-center gap-2 px-2 py-1.5 text-sm text-muted-foreground",
                          project.active && "font-medium text-sidebar-primary",
                        )}
                      >
                        <FolderKanban className="h-4 w-4 shrink-0" />
                        <span className="flex-1 truncate">{project.name}</span>
                      </Link>
                      {actions.unarchiveProject && (
                        <Button
                          variant="ghost"
                          size="icon"
                          className="h-6 w-6 shrink-0"
                          title={t("sidebar.projects.unarchive")}
                          aria-label={t("sidebar.projects.unarchiveProject", { name: project.name })}
                          onClick={() => void actions.unarchiveProject?.(project.id)}
                        >
                          <ArchiveRestore className="h-3.5 w-3.5" />
                        </Button>
                      )}
                    </div>
                  ))}
                  {data.archivedError && <p className="px-2 py-1 text-xs text-destructive">{data.archivedError}</p>}
                </div>
              )}
            </div>
          )}
        </div>

        <div className="my-2 border-t border-sidebar-border" />
        {inbox.map((item) => (
          <RowItem key={item.id} item={item} />
        ))}

        <div className="my-2 border-t border-sidebar-border" />
        {tools.map((item) => (
          <RowItem key={item.id} item={item} />
        ))}
      </nav>

      {/* Footer */}
      <Separator />
      <div className="p-3 space-y-0.5">
        {footer.map((item) => (
          <RowItem key={item.id} item={item} />
        ))}
      </div>
    </aside>
  );
};
