import { Link, useLocation, useParams } from "react-router";
import {
  Activity,
  Bot,
  ChevronDown,
  FolderKanban,
  LayoutDashboard,
  Plus,
  Settings,
} from "lucide-react";
import { cn } from "@/lib/cn";
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
  const isEventsRoute = location.pathname.endsWith("/events");

  if (collapsed) {
    return (
      <aside className="flex h-full w-12 flex-col items-center border-r border-sidebar-border bg-sidebar py-4">
        <div className="mb-4 flex h-8 w-8 items-center justify-center rounded-lg bg-sidebar-primary text-xs font-bold text-primary-foreground">
          {currentWorkspace?.name.charAt(0).toUpperCase() || "M"}
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
            to={wsSlug ? `/w/${wsSlug}/events` : "/"}
            className={cn(
              "flex h-8 w-8 items-center justify-center rounded-lg text-sidebar-foreground hover:bg-sidebar-accent",
              isEventsRoute && "bg-sidebar-accent text-sidebar-primary",
            )}
          >
            <Activity className="h-4 w-4" />
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
            <div className="flex h-6 w-6 items-center justify-center rounded bg-sidebar-primary text-[10px] font-bold text-primary-foreground">
              {currentWorkspace?.name.charAt(0).toUpperCase() || "M"}
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
            !projectSlug && !isAgentsRoute && !isEventsRoute && "bg-sidebar-accent font-medium",
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
          to={wsSlug ? `/w/${wsSlug}/events` : "/"}
          className={cn(
            "flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm text-sidebar-foreground hover:bg-sidebar-accent",
            isEventsRoute && "bg-sidebar-accent font-medium",
          )}
        >
          <Activity className="h-4 w-4" />
          Events
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
      <div className="p-3">
        <Link
          to={
            wsSlug && projectSlug
              ? `/w/${wsSlug}/p/${projectSlug}/settings`
              : "#"
          }
          className="flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm text-sidebar-foreground hover:bg-sidebar-accent"
        >
          <Settings className="h-4 w-4" />
          Settings
        </Link>
      </div>
    </aside>
  );
}
