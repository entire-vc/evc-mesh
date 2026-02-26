import { Link, useLocation, useParams } from "react-router";
import {
  Activity,
  BarChart2,
  Bot,
  ChevronDown,
  FolderKanban,
  Inbox,
  LayoutDashboard,
  Plug,
  Plus,
  Settings,
  Sparkles,
  Target,
} from "lucide-react";
import { cn } from "@/lib/cn";
import { MeshIcon } from "@/components/mesh-icon";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Separator } from "@/components/ui/separator";

interface SidebarProps {
  collapsed: boolean;
}

export function Sidebar({ collapsed }: SidebarProps) {
  const { wsSlug, projectSlug } = useParams();
  const location = useLocation();
  const { workspaces, currentWorkspace } = useWorkspaceStore();
  const { projects } = useProjectStore();

  const isAgentsRoute = location.pathname.endsWith("/agents");
  const isSparkRoute = location.pathname.endsWith("/spark");
  const isEventsRoute = location.pathname.endsWith("/events");
  const isAnalyticsRoute = location.pathname.endsWith("/analytics");
  const isIntegrationsRoute = location.pathname.endsWith("/integrations");
  const isInitiativesRoute = location.pathname.endsWith("/initiatives");
  const isTriageRoute = location.pathname.endsWith("/triage");

  if (collapsed) {
    return (
      <aside className="flex h-full w-12 flex-col items-center border-r border-sidebar-border bg-sidebar py-4">
        <div className="mb-4 flex h-8 w-8 items-center justify-center rounded-lg bg-sidebar-primary text-primary-foreground">
          <MeshIcon size={18} />
        </div>
        <Separator className="mb-4 w-8" />
        <nav className="flex flex-col items-center gap-2">
          <Link
            to={wsSlug ? `/w/${wsSlug}` : "/"}
            className="flex h-8 w-8 items-center justify-center rounded-lg text-sidebar-foreground hover:bg-sidebar-accent"
          >
            <LayoutDashboard className="h-4 w-4" />
          </Link>
          <Link
            to={wsSlug ? `/w/${wsSlug}/agents` : "/"}
            className={cn(
              "flex h-8 w-8 items-center justify-center rounded-lg text-sidebar-foreground hover:bg-sidebar-accent",
              isAgentsRoute && "bg-sidebar-accent text-sidebar-primary",
            )}
          >
            <Bot className="h-4 w-4" />
          </Link>
          <Link
            to={wsSlug ? `/w/${wsSlug}/spark` : "/"}
            className={cn(
              "flex h-8 w-8 items-center justify-center rounded-lg text-sidebar-foreground hover:bg-sidebar-accent",
              isSparkRoute && "bg-sidebar-accent text-sidebar-primary",
            )}
          >
            <Sparkles className="h-4 w-4" />
          </Link>
          <Link
            to={wsSlug ? `/w/${wsSlug}/events` : "/"}
            className={cn(
              "flex h-8 w-8 items-center justify-center rounded-lg text-sidebar-foreground hover:bg-sidebar-accent",
              isEventsRoute && "bg-sidebar-accent text-sidebar-primary",
            )}
          >
            <Activity className="h-4 w-4" />
          </Link>
          <Link
            to={wsSlug ? `/w/${wsSlug}/analytics` : "/"}
            className={cn(
              "flex h-8 w-8 items-center justify-center rounded-lg text-sidebar-foreground hover:bg-sidebar-accent",
              isAnalyticsRoute && "bg-sidebar-accent text-sidebar-primary",
            )}
          >
            <BarChart2 className="h-4 w-4" />
          </Link>
          <Link
            to={wsSlug ? `/w/${wsSlug}/integrations` : "/"}
            className={cn(
              "flex h-8 w-8 items-center justify-center rounded-lg text-sidebar-foreground hover:bg-sidebar-accent",
              isIntegrationsRoute && "bg-sidebar-accent text-sidebar-primary",
            )}
          >
            <Plug className="h-4 w-4" />
          </Link>
          <Link
            to={wsSlug ? `/w/${wsSlug}/initiatives` : "/"}
            className={cn(
              "flex h-8 w-8 items-center justify-center rounded-lg text-sidebar-foreground hover:bg-sidebar-accent",
              isInitiativesRoute && "bg-sidebar-accent text-sidebar-primary",
            )}
          >
            <Target className="h-4 w-4" />
          </Link>
          <Link
            to={wsSlug ? `/w/${wsSlug}/triage` : "/"}
            className={cn(
              "flex h-8 w-8 items-center justify-center rounded-lg text-sidebar-foreground hover:bg-sidebar-accent",
              isTriageRoute && "bg-sidebar-accent text-sidebar-primary",
            )}
          >
            <Inbox className="h-4 w-4" />
          </Link>
          {projects.map((project) => (
            <Link
              key={project.id}
              to={`/w/${wsSlug}/p/${project.slug}`}
              className={cn(
                "flex h-8 w-8 items-center justify-center rounded-lg text-sidebar-foreground hover:bg-sidebar-accent",
                project.slug === projectSlug &&
                  "bg-sidebar-accent text-sidebar-primary",
              )}
            >
              <span className="text-xs font-medium">
                {project.icon || project.name.charAt(0).toUpperCase()}
              </span>
            </Link>
          ))}
        </nav>
      </aside>
    );
  }

  return (
    <aside className="flex h-full w-60 flex-col border-r border-sidebar-border bg-sidebar">
      {/* Workspace switcher */}
      <div className="p-3">
        <DropdownMenu>
          <DropdownMenuTrigger className="flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-sm font-semibold text-sidebar-foreground hover:bg-sidebar-accent">
            <div className="flex h-6 w-6 items-center justify-center rounded bg-sidebar-primary text-primary-foreground">
              <MeshIcon size={14} />
            </div>
            <span className="flex-1 truncate text-left">
              {currentWorkspace?.name || "Select workspace"}
            </span>
            <ChevronDown className="h-4 w-4 shrink-0 opacity-50" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="w-56">
            <DropdownMenuLabel>Workspaces</DropdownMenuLabel>
            <DropdownMenuSeparator />
            {workspaces.map((ws) => (
              <Link key={ws.id} to={`/w/${ws.slug}`}>
                <DropdownMenuItem
                  className={cn(
                    ws.slug === wsSlug && "bg-accent text-accent-foreground",
                  )}
                >
                  {ws.name}
                </DropdownMenuItem>
              </Link>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      <Separator />

      {/* Navigation */}
      <nav className="flex-1 overflow-y-auto p-3">
        <Link
          to={wsSlug ? `/w/${wsSlug}` : "/"}
          className={cn(
            "flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm text-sidebar-foreground hover:bg-sidebar-accent",
            !projectSlug && !isAgentsRoute && !isSparkRoute && !isEventsRoute && !isAnalyticsRoute && !isIntegrationsRoute && !isInitiativesRoute && !isTriageRoute && "bg-sidebar-accent font-medium",
          )}
        >
          <LayoutDashboard className="h-4 w-4" />
          Dashboard
        </Link>
        <Link
          to={wsSlug ? `/w/${wsSlug}/agents` : "/"}
          className={cn(
            "flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm text-sidebar-foreground hover:bg-sidebar-accent",
            isAgentsRoute && "bg-sidebar-accent font-medium",
          )}
        >
          <Bot className="h-4 w-4" />
          Agents
        </Link>
        <Link
          to={wsSlug ? `/w/${wsSlug}/spark` : "/"}
          className={cn(
            "flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm text-sidebar-foreground hover:bg-sidebar-accent",
            isSparkRoute && "bg-sidebar-accent font-medium",
          )}
        >
          <Sparkles className="h-4 w-4" />
          Spark Catalog
        </Link>
        <Link
          to={wsSlug ? `/w/${wsSlug}/events` : "/"}
          className={cn(
            "flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm text-sidebar-foreground hover:bg-sidebar-accent",
            isEventsRoute && "bg-sidebar-accent font-medium",
          )}
        >
          <Activity className="h-4 w-4" />
          Events
        </Link>
        <Link
          to={wsSlug ? `/w/${wsSlug}/analytics` : "/"}
          className={cn(
            "flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm text-sidebar-foreground hover:bg-sidebar-accent",
            isAnalyticsRoute && "bg-sidebar-accent font-medium",
          )}
        >
          <BarChart2 className="h-4 w-4" />
          Analytics
        </Link>
        <Link
          to={wsSlug ? `/w/${wsSlug}/integrations` : "/"}
          className={cn(
            "flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm text-sidebar-foreground hover:bg-sidebar-accent",
            isIntegrationsRoute && "bg-sidebar-accent font-medium",
          )}
        >
          <Plug className="h-4 w-4" />
          Integrations
        </Link>
        <Link
          to={wsSlug ? `/w/${wsSlug}/initiatives` : "/"}
          className={cn(
            "flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm text-sidebar-foreground hover:bg-sidebar-accent",
            isInitiativesRoute && "bg-sidebar-accent font-medium",
          )}
        >
          <Target className="h-4 w-4" />
          Initiatives
        </Link>
        <Link
          to={wsSlug ? `/w/${wsSlug}/triage` : "/"}
          className={cn(
            "flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm text-sidebar-foreground hover:bg-sidebar-accent",
            isTriageRoute && "bg-sidebar-accent font-medium",
          )}
        >
          <Inbox className="h-4 w-4" />
          Triage Inbox
        </Link>

        <div className="mt-4">
          <div className="flex items-center justify-between px-2 py-1">
            <span className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              Projects
            </span>
            <Button variant="ghost" size="icon" className="h-5 w-5">
              <Plus className="h-3 w-3" />
            </Button>
          </div>
          <div className="mt-1 space-y-0.5">
            {projects.map((project) => (
              <Link
                key={project.id}
                to={`/w/${wsSlug}/p/${project.slug}`}
                className={cn(
                  "flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm text-sidebar-foreground hover:bg-sidebar-accent",
                  project.slug === projectSlug &&
                    "bg-sidebar-accent font-medium text-sidebar-primary",
                )}
              >
                <FolderKanban className="h-4 w-4" />
                <span className="flex-1 truncate">{project.name}</span>
              </Link>
            ))}
            {projects.length === 0 && (
              <p className="px-2 py-4 text-center text-xs text-muted-foreground">
                No projects yet
              </p>
            )}
          </div>
        </div>
      </nav>

      {/* Footer */}
      <Separator />
      <div className="p-3 space-y-0.5">
        {wsSlug && (
          <Link
            to={`/w/${wsSlug}/settings`}
            className={cn(
              "flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm text-sidebar-foreground hover:bg-sidebar-accent",
              location.pathname === `/w/${wsSlug}/settings` &&
                "bg-sidebar-accent font-medium",
            )}
          >
            <Settings className="h-4 w-4" />
            Workspace Settings
          </Link>
        )}
        {wsSlug && projectSlug && (
          <Link
            to={`/w/${wsSlug}/p/${projectSlug}/settings`}
            className={cn(
              "flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm text-sidebar-foreground hover:bg-sidebar-accent",
              location.pathname === `/w/${wsSlug}/p/${projectSlug}/settings` &&
                "bg-sidebar-accent font-medium",
            )}
          >
            <Settings className="h-4 w-4" />
            Project Settings
          </Link>
        )}
      </div>
    </aside>
  );
}
