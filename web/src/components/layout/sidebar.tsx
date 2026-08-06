import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useLocation, useNavigate, useParams } from "react-router";
import {
  Activity,
  BarChart2,
  Bell,
  Bot,
  Brain,
  ChevronDown,
  FolderKanban,
  Inbox,
  LayoutDashboard,
  MonitorDot,
  Plug,
  Plus,
  Settings,
  Sparkles,
  Target,
} from "lucide-react";
import { cn } from "@/lib/cn";
import { MeshIcon } from "@/components/mesh-icon";
import { api } from "@/lib/api";
import { useWorkspaceStore } from "@/stores/workspace";
import { useCapabilitiesStore } from "@/stores/capabilities";
import { useProjectStore } from "@/stores/project";
import { useAuthStore } from "@/stores/auth";
import { useWebSocketStore } from "@/stores/websocket";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Separator } from "@/components/ui/separator";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { CreateProjectDialog } from "@/components/create-project-dialog";

interface SidebarProps {
  collapsed: boolean;
}

export function Sidebar({ collapsed }: SidebarProps) {
  const { wsSlug, projectSlug } = useParams();
  const location = useLocation();
  const navigate = useNavigate();
  const { workspaces, currentWorkspace, createWorkspace } = useWorkspaceStore();
  const { projects } = useProjectStore();
  const { user } = useAuthStore();
  const lastEvent = useWebSocketStore((s) => s.lastEvent);
  const wsSubscribe = useWebSocketStore((s) => s.subscribe);
  const wsUnsubscribe = useWebSocketStore((s) => s.unsubscribe);
  const [showCreateProject, setShowCreateProject] = useState(false);
  const [createWsOpen, setCreateWsOpen] = useState(false);
  const [wsName, setWsName] = useState("");
  const [wsSlugDraft, setWsSlugDraft] = useState("");
  const [wsCreating, setWsCreating] = useState(false);
  const [wsError, setWsError] = useState<string | null>(null);
  const [unseenCount, setUnseenCount] = useState(0);
  const unseenFetchedRef = useRef(false);
  const sparkEnabled = useCapabilitiesStore((s) => s.sparkEnabled);
  const fetchCapabilities = useCapabilitiesStore((s) => s.fetch);

  useEffect(() => {
    fetchCapabilities();
  }, [fetchCapabilities]);

  const handleWsNameChange = useCallback((value: string) => {
    setWsName(value);
    setWsSlugDraft(value.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, ""));
  }, []);

  const handleCreateWs = useCallback(async () => {
    if (!wsName.trim() || !wsSlugDraft.trim()) return;
    setWsError(null);
    setWsCreating(true);
    try {
      const ws = await createWorkspace({ name: wsName.trim(), slug: wsSlugDraft.trim() });
      setCreateWsOpen(false);
      setWsName("");
      setWsSlugDraft("");
      navigate(`/w/${ws.slug}`);
    } catch (err) {
      setWsError(err instanceof Error ? err.message : "Failed to create workspace");
    } finally {
      setWsCreating(false);
    }
  }, [wsName, wsSlugDraft, createWorkspace, navigate]);

  const isDashboardRoute = location.pathname.endsWith("/dashboard");
  const isOrgChartRoute = location.pathname.endsWith("/org-chart");
  const isSparkRoute = location.pathname.endsWith("/spark");
  const isEventsRoute = location.pathname.endsWith("/events");
  const isAnalyticsRoute = location.pathname.endsWith("/analytics");
  const isIntegrationsRoute = location.pathname.endsWith("/integrations");
  const isInitiativesRoute = location.pathname.endsWith("/initiatives");
  const isTriageRoute = location.pathname.endsWith("/triage");
  const isMemoriesRoute = location.pathname.endsWith("/memories");
  const isSessionsRoute = location.pathname.endsWith("/sessions");
  const isActivityRoute = location.pathname.includes("/activity");
  const isNotificationsRoute = location.pathname.endsWith("/notifications");

  // Fetch unseen mention count once on mount, then refresh on cache invalidation.
  useEffect(() => {
    if (unseenFetchedRef.current) return;
    unseenFetchedRef.current = true;
    api<{ count: number }>("/api/v1/me/mentions/unseen_count")
      .then(({ count }) => {
        localStorage.setItem("mesh_unseen_ts", String(Date.now()));
        localStorage.setItem("mesh_unseen_count", String(count));
        setUnseenCount(count);
      })
      .catch(() => {});
  }, []);

  // Subscribe to personal WS channel for mention.created events.
  useEffect(() => {
    if (!user?.id) return;
    const channel = `ws:user:${user.id}`;
    wsSubscribe(channel);
    return () => wsUnsubscribe(channel);
  }, [user?.id, wsSubscribe, wsUnsubscribe]);

  // Increment badge on incoming mention.created event.
  useEffect(() => {
    if (lastEvent?.type === "mention.created") {
      setUnseenCount((n) => n + 1);
    }
  }, [lastEvent]);

  // Clear badge when ActivityPage marks all visible mentions as shown.
  useEffect(() => {
    const handler = (e: Event) => {
      setUnseenCount((e as CustomEvent<{ newCount: number }>).detail.newCount);
    };
    window.addEventListener("mesh:mentions:shown", handler);
    return () => window.removeEventListener("mesh:mentions:shown", handler);
  }, []);

  if (collapsed) {
    return (
      <aside className="flex h-full w-12 flex-col items-center border-r border-sidebar-border bg-sidebar">
        <div className="flex h-14 w-full items-center justify-center border-b border-sidebar-border">
          <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-sidebar-primary text-primary-foreground">
            <MeshIcon size={18} />
          </div>
        </div>
        <nav className="flex flex-col items-center gap-2 py-3">
          {/* Dashboard */}
          <Link
            to={wsSlug ? `/w/${wsSlug}/dashboard` : "/"}
            className={cn(
              "flex h-8 w-8 items-center justify-center rounded-lg text-sidebar-foreground hover:bg-sidebar-accent",
              isDashboardRoute && "bg-sidebar-accent text-sidebar-primary",
            )}
          >
            <LayoutDashboard className="h-4 w-4" />
          </Link>
          {/* Projects */}
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
          <div className="my-1 w-6 border-t border-sidebar-border" />
          {/* Initiatives */}
          <Link
            to={wsSlug ? `/w/${wsSlug}/initiatives` : "/"}
            className={cn(
              "flex h-8 w-8 items-center justify-center rounded-lg text-sidebar-foreground hover:bg-sidebar-accent",
              isInitiativesRoute && "bg-sidebar-accent text-sidebar-primary",
            )}
          >
            <Target className="h-4 w-4" />
          </Link>
          {/* Triage */}
          <Link
            to={wsSlug ? `/w/${wsSlug}/triage` : "/"}
            className={cn(
              "flex h-8 w-8 items-center justify-center rounded-lg text-sidebar-foreground hover:bg-sidebar-accent",
              isTriageRoute && "bg-sidebar-accent text-sidebar-primary",
            )}
          >
            <Inbox className="h-4 w-4" />
          </Link>
          {/* Activity */}
          <Link
            to={wsSlug ? `/w/${wsSlug}/activity` : "/"}
            className={cn(
              "relative flex h-8 w-8 items-center justify-center rounded-lg text-sidebar-foreground hover:bg-sidebar-accent",
              isActivityRoute && "bg-sidebar-accent text-sidebar-primary",
            )}
          >
            <Bell className="h-4 w-4" />
            {unseenCount > 0 && (
              <span className="absolute -right-0.5 -top-0.5 flex h-4 w-4 items-center justify-center rounded-full bg-red-500 text-[10px] font-bold text-white">
                {unseenCount > 9 ? "9+" : unseenCount}
              </span>
            )}
          </Link>
          <div className="my-1 w-6 border-t border-sidebar-border" />
          {/* Team (Org Chart) */}
          <Link
            to={wsSlug ? `/w/${wsSlug}/org-chart` : "/"}
            className={cn(
              "flex h-8 w-8 items-center justify-center rounded-lg text-sidebar-foreground hover:bg-sidebar-accent",
              isOrgChartRoute && "bg-sidebar-accent text-sidebar-primary",
            )}
          >
            <Bot className="h-4 w-4" />
          </Link>
          {/* Memory Browser */}
          <Link
            to={wsSlug ? `/w/${wsSlug}/memories` : "/"}
            className={cn(
              "flex h-8 w-8 items-center justify-center rounded-lg text-sidebar-foreground hover:bg-sidebar-accent",
              isMemoriesRoute && "bg-sidebar-accent text-sidebar-primary",
            )}
          >
            <Brain className="h-4 w-4" />
          </Link>
          {/* Sessions Dashboard */}
          <Link
            to={wsSlug ? `/w/${wsSlug}/sessions` : "/"}
            className={cn(
              "flex h-8 w-8 items-center justify-center rounded-lg text-sidebar-foreground hover:bg-sidebar-accent",
              isSessionsRoute && "bg-sidebar-accent text-sidebar-primary",
            )}
          >
            <MonitorDot className="h-4 w-4" />
          </Link>
          {/* Spark Catalog — hidden when the server has MESH_SPARK_ENABLED=false */}
          {sparkEnabled && (
            <Link
              to={wsSlug ? `/w/${wsSlug}/spark` : "/"}
              className={cn(
                "flex h-8 w-8 items-center justify-center rounded-lg text-sidebar-foreground hover:bg-sidebar-accent",
                isSparkRoute && "bg-sidebar-accent text-sidebar-primary",
              )}
            >
              <Sparkles className="h-4 w-4" />
            </Link>
          )}
          {/* Analytics */}
          <Link
            to={wsSlug ? `/w/${wsSlug}/analytics` : "/"}
            className={cn(
              "flex h-8 w-8 items-center justify-center rounded-lg text-sidebar-foreground hover:bg-sidebar-accent",
              isAnalyticsRoute && "bg-sidebar-accent text-sidebar-primary",
            )}
          >
            <BarChart2 className="h-4 w-4" />
          </Link>
          {/* Events */}
          <Link
            to={wsSlug ? `/w/${wsSlug}/events` : "/"}
            className={cn(
              "flex h-8 w-8 items-center justify-center rounded-lg text-sidebar-foreground hover:bg-sidebar-accent",
              isEventsRoute && "bg-sidebar-accent text-sidebar-primary",
            )}
          >
            <Activity className="h-4 w-4" />
          </Link>
          {/* Integrations */}
          <Link
            to={wsSlug ? `/w/${wsSlug}/integrations` : "/"}
            className={cn(
              "flex h-8 w-8 items-center justify-center rounded-lg text-sidebar-foreground hover:bg-sidebar-accent",
              isIntegrationsRoute && "bg-sidebar-accent text-sidebar-primary",
            )}
          >
            <Plug className="h-4 w-4" />
          </Link>
        </nav>
      </aside>
    );
  }

  return (
    <aside className="flex h-full w-60 flex-col border-r border-sidebar-border bg-sidebar">
      {/* Workspace switcher */}
      <div className="flex h-14 items-center border-b border-sidebar-border px-3">
        <DropdownMenu>
          <DropdownMenuTrigger className="flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-sm font-semibold text-sidebar-foreground hover:bg-sidebar-accent">
            <div className="flex h-6 w-6 shrink-0 items-center justify-center overflow-hidden rounded bg-sidebar-primary text-primary-foreground">
              {currentWorkspace?.icon_url ? (
                <img
                  src={currentWorkspace.icon_url}
                  alt={currentWorkspace.name}
                  className="h-full w-full object-cover"
                />
              ) : (
                <MeshIcon size={14} />
              )}
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
                    "gap-2",
                    ws.slug === wsSlug && "bg-accent text-accent-foreground",
                  )}
                >
                  <div className="flex h-5 w-5 shrink-0 items-center justify-center overflow-hidden rounded bg-sidebar-primary text-primary-foreground text-[10px] font-bold">
                    {ws.icon_url ? (
                      <img src={ws.icon_url} alt={ws.name} className="h-full w-full object-cover" />
                    ) : (
                      ws.name.charAt(0).toUpperCase()
                    )}
                  </div>
                  {ws.name}
                </DropdownMenuItem>
              </Link>
            ))}
            <DropdownMenuSeparator />
            <DropdownMenuItem
              onClick={() => setCreateWsOpen(true)}
              className="gap-2 text-muted-foreground"
            >
              <Plus className="h-3.5 w-3.5" />
              New Workspace
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      {/* Navigation */}
      <nav className="flex-1 overflow-y-auto p-3">
        {/* Dashboard */}
        <Link
          to={wsSlug ? `/w/${wsSlug}/dashboard` : "/"}
          className={cn(
            "flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm text-sidebar-foreground hover:bg-sidebar-accent",
            isDashboardRoute && "bg-sidebar-accent font-medium",
          )}
        >
          <LayoutDashboard className="h-4 w-4" />
          Dashboard
        </Link>

        {/* Projects */}
        <div className="mt-3">
          <div className="flex items-center justify-between px-2 py-1">
            <span className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              Projects
            </span>
            <Button
              variant="ghost"
              size="icon"
              className="h-5 w-5"
              onClick={() => setShowCreateProject(true)}
            >
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
                {project.icon ? (
                  <span className="flex h-4 w-4 shrink-0 items-center justify-center text-sm leading-none">
                    {project.icon}
                  </span>
                ) : (
                  <FolderKanban className="h-4 w-4 shrink-0" />
                )}
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

        <div className="my-2 border-t border-sidebar-border" />

        {/* Initiatives */}
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
        {/* Triage Inbox */}
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
        {/* Activity */}
        <Link
          to={wsSlug ? `/w/${wsSlug}/activity` : "/"}
          className={cn(
            "flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm text-sidebar-foreground hover:bg-sidebar-accent",
            isActivityRoute && "bg-sidebar-accent font-medium",
          )}
        >
          <Bell className="h-4 w-4" />
          Activity
          {unseenCount > 0 && (
            <span className="ml-auto flex h-5 min-w-5 items-center justify-center rounded-full bg-red-500 px-1 text-[10px] font-bold text-white">
              {unseenCount > 99 ? "99+" : unseenCount}
            </span>
          )}
        </Link>

        <div className="my-2 border-t border-sidebar-border" />

        {/* Team (Org Chart) */}
        <Link
          to={wsSlug ? `/w/${wsSlug}/org-chart` : "/"}
          className={cn(
            "flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm text-sidebar-foreground hover:bg-sidebar-accent",
            isOrgChartRoute && "bg-sidebar-accent font-medium",
          )}
        >
          <Bot className="h-4 w-4" />
          Team
        </Link>
        {/* Memory Browser */}
        <Link
          to={wsSlug ? `/w/${wsSlug}/memories` : "/"}
          className={cn(
            "flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm text-sidebar-foreground hover:bg-sidebar-accent",
            isMemoriesRoute && "bg-sidebar-accent font-medium",
          )}
        >
          <Brain className="h-4 w-4" />
          Memory
        </Link>
        {/* Sessions Dashboard */}
        <Link
          to={wsSlug ? `/w/${wsSlug}/sessions` : "/"}
          className={cn(
            "flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm text-sidebar-foreground hover:bg-sidebar-accent",
            isSessionsRoute && "bg-sidebar-accent font-medium",
          )}
        >
          <MonitorDot className="h-4 w-4" />
          Sessions
        </Link>
        {/* Spark Catalog — hidden when the server has MESH_SPARK_ENABLED=false */}
        {sparkEnabled && (
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
        )}
        {/* Analytics */}
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
        {/* Events */}
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
        {/* Integrations */}
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
        {wsSlug && (
          <Link
            to={`/w/${wsSlug}/notifications`}
            className={cn(
              "flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm text-sidebar-foreground hover:bg-sidebar-accent",
              isNotificationsRoute && "bg-sidebar-accent font-medium",
            )}
          >
            <Bell className="h-4 w-4" />
            Notification Settings
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

      <CreateProjectDialog
        open={showCreateProject}
        onOpenChange={setShowCreateProject}
      />

      {/* Create workspace dialog */}
      <Dialog open={createWsOpen} onOpenChange={(open) => { setCreateWsOpen(open); if (!open) { setWsName(""); setWsSlugDraft(""); setWsError(null); } }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Create Workspace</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 pt-2">
            <div className="space-y-1.5">
              <label className="text-sm font-medium">Name</label>
              <Input
                value={wsName}
                onChange={(e) => handleWsNameChange(e.target.value)}
                placeholder="My Team"
                autoFocus
                onKeyDown={(e) => { if (e.key === "Enter" && wsName.trim()) void handleCreateWs(); }}
              />
            </div>
            <div className="space-y-1.5">
              <label className="text-sm font-medium">Slug</label>
              <Input
                value={wsSlugDraft}
                onChange={(e) => setWsSlugDraft(e.target.value)}
                placeholder="my-team"
              />
              <p className="text-xs text-muted-foreground">Used in URLs: /w/{wsSlugDraft || "slug"}</p>
            </div>
            {wsError && (
              <p className="text-sm text-destructive">{wsError}</p>
            )}
            <div className="flex justify-end gap-2">
              <Button variant="outline" size="sm" onClick={() => setCreateWsOpen(false)}>Cancel</Button>
              <Button size="sm" onClick={() => void handleCreateWs()} disabled={!wsName.trim() || !wsSlugDraft.trim() || wsCreating}>
                {wsCreating ? "Creating..." : "Create"}
              </Button>
            </div>
          </div>
        </DialogContent>
      </Dialog>
    </aside>
  );
}
