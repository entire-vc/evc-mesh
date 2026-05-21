import { type FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { Navigate, Outlet, useLocation, useNavigate, useParams } from "react-router";
import { cn } from "@/lib/cn";
import { api } from "@/lib/api";
import { useAuthStore } from "@/stores/auth";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";
import { useWebSocketStore } from "@/stores/websocket";
import { Sidebar } from "./sidebar";
import { Header } from "./header";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";

export function AppLayout() {
  const { isAuthenticated, isLoading: authLoading } = useAuthStore();
  const { wsSlug, projectSlug } = useParams();
  const {
    workspaces,
    currentWorkspace,
    fetchWorkspaces,
    setCurrentWorkspaceBySlug,
  } = useWorkspaceStore();
  const { projects, fetchProjects, setCurrentProjectBySlug, currentProject } =
    useProjectStore();
  const wsConnect = useWebSocketStore((s) => s.connect);
  const wsDisconnect = useWebSocketStore((s) => s.disconnect);

  const [sidebarCollapsed, setSidebarCollapsed] = useState(
    () => window.innerWidth < 768,
  );
  const [initialized, setInitialized] = useState(false);
  const [defaultRedirect, setDefaultRedirect] = useState<string | null>(null);
  const defaultRedirectCheckRef = useRef(false);

  const toggleSidebar = useCallback(() => {
    setSidebarCollapsed((prev) => !prev);
  }, []);

  // Fetch workspaces on mount
  useEffect(() => {
    if (isAuthenticated && workspaces.length === 0) {
      fetchWorkspaces().then(() => setInitialized(true));
    } else if (isAuthenticated) {
      setInitialized(true);
    }
  }, [isAuthenticated, workspaces.length, fetchWorkspaces]);

  // Resolve workspace slug
  useEffect(() => {
    if (wsSlug && workspaces.length > 0) {
      setCurrentWorkspaceBySlug(wsSlug);
    }
  }, [wsSlug, workspaces, setCurrentWorkspaceBySlug]);

  // Fetch projects when workspace changes
  useEffect(() => {
    if (currentWorkspace) {
      fetchProjects(currentWorkspace.id);
    }
  }, [currentWorkspace, fetchProjects]);

  // Resolve project slug (re-runs when projects finish loading)
  useEffect(() => {
    if (projectSlug && projects.length > 0) {
      setCurrentProjectBySlug(projectSlug);
    } else if (!projectSlug && currentProject) {
      // Clear current project when navigating away from a project route
      useProjectStore.setState({ currentProject: null });
    }
  }, [projectSlug, projects, setCurrentProjectBySlug, currentProject]);

  // Initialize WebSocket connection when workspace is available.
  useEffect(() => {
    if (isAuthenticated && currentWorkspace?.slug) {
      wsConnect(currentWorkspace.slug);
    }

    return () => {
      if (!isAuthenticated) {
        wsDisconnect();
      }
    };
  }, [isAuthenticated, currentWorkspace?.slug, wsConnect, wsDisconnect]);

  // Auto-collapse sidebar on mobile viewport resize
  const location = useLocation();
  useEffect(() => {
    const mq = window.matchMedia("(max-width: 767px)");
    const handler = () => { if (mq.matches) setSidebarCollapsed(true); };
    mq.addEventListener("change", handler);
    return () => mq.removeEventListener("change", handler);
  }, []);

  const isDeepLinkRoute =
    location.pathname.startsWith("/t/") ||
    location.pathname.startsWith("/tasks/");

  // Default-landing: when navigating to root with no wsSlug, redirect to /activity
  // if there are unseen mentions, otherwise to dashboard. Uses 10s localStorage cache.
  useEffect(() => {
    if (wsSlug || !initialized || workspaces.length === 0 || isDeepLinkRoute) return;
    if (defaultRedirectCheckRef.current) return;
    defaultRedirectCheckRef.current = true;

    const ws = workspaces[0]!;
    const cachedTs = localStorage.getItem("mesh_unseen_ts");
    const cachedCount = localStorage.getItem("mesh_unseen_count");
    if (cachedTs && cachedCount && Date.now() - Number(cachedTs) < 10_000) {
      const count = Number(cachedCount);
      setDefaultRedirect(count > 0 ? `/w/${ws.slug}/activity` : `/w/${ws.slug}`);
      return;
    }

    api<{ count: number }>("/api/v1/me/mentions/unseen_count")
      .then(({ count }) => {
        localStorage.setItem("mesh_unseen_ts", String(Date.now()));
        localStorage.setItem("mesh_unseen_count", String(count));
        setDefaultRedirect(count > 0 ? `/w/${ws.slug}/activity` : `/w/${ws.slug}`);
      })
      .catch(() => {
        setDefaultRedirect(`/w/${ws.slug}`);
      });
  }, [wsSlug, initialized, workspaces, isDeepLinkRoute]);

  // Close sidebar on route change (mobile)
  useEffect(() => {
    if (window.innerWidth < 768) setSidebarCollapsed(true);
  }, [location.pathname]);

  if (authLoading) {
    return (
      <div className="flex h-screen items-center justify-center bg-background">
        <div className="space-y-4 text-center">
          <Skeleton className="mx-auto h-8 w-8 rounded-full" />
          <Skeleton className="h-4 w-32" />
        </div>
      </div>
    );
  }

  if (!isAuthenticated) {
    // Preserve the requested path so login can bounce back after sign-in
    // (e.g. deep links like /t/:taskId, /tasks/:taskId, or any /w/... path).
    const path = location.pathname + location.search;
    const to =
      path && path !== "/" && !path.startsWith("/login")
        ? `/login?redirect=${encodeURIComponent(path)}`
        : "/login";
    return <Navigate to={to} replace />;
  }

  // Show loading while fetching workspaces
  if (!initialized) {
    return (
      <div className="flex h-screen items-center justify-center bg-background">
        <div className="space-y-4 text-center">
          <Skeleton className="mx-auto h-8 w-8 rounded-full" />
          <Skeleton className="h-4 w-32" />
        </div>
      </div>
    );
  }

  // Redirect to first workspace if no ws in URL — with activity check for default-landing.
  // Skip for deep-link routes so the resolver can navigate to canonical ws+project path.
  if (!wsSlug && workspaces.length > 0 && !isDeepLinkRoute) {
    if (defaultRedirect) {
      return <Navigate to={defaultRedirect} replace />;
    }
    return (
      <div className="flex h-screen items-center justify-center bg-background">
        <div className="space-y-4 text-center">
          <Skeleton className="mx-auto h-8 w-8 rounded-full" />
          <Skeleton className="h-4 w-32" />
        </div>
      </div>
    );
  }

  // No workspaces — show create workspace screen
  if (workspaces.length === 0) {
    return <NoWorkspacesScreen />;
  }

  return (
    <div className="flex h-screen bg-background">
      {/* Mobile backdrop */}
      {!sidebarCollapsed && (
        <div
          className="fixed inset-0 z-30 bg-black/40 md:hidden"
          onClick={toggleSidebar}
          aria-hidden="true"
        />
      )}

      {/* Sidebar — fixed overlay on mobile, inline on desktop */}
      <div
        className={cn(
          "shrink-0 transition-all duration-200",
          // Mobile: fixed overlay
          "fixed inset-y-0 left-0 z-40 md:relative md:z-auto",
          sidebarCollapsed
            ? "-translate-x-full md:translate-x-0 md:w-12"
            : "translate-x-0 w-60",
        )}
      >
        <Sidebar collapsed={sidebarCollapsed} />
      </div>

      <div className="flex flex-1 flex-col overflow-hidden">
        <Header onToggleSidebar={toggleSidebar} />
        <main className="flex-1 overflow-y-auto p-3 md:p-6">
          <Outlet />
        </main>
      </div>
    </div>
  );
}

function NoWorkspacesScreen() {
  const navigate = useNavigate();
  const { createWorkspace } = useWorkspaceStore();
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const handleNameChange = useCallback((value: string) => {
    setName(value);
    // Auto-generate slug from name
    setSlug(
      value
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, "-")
        .replace(/^-|-$/g, ""),
    );
  }, []);

  const handleSubmit = useCallback(
    async (e: FormEvent) => {
      e.preventDefault();
      if (!name.trim() || !slug.trim()) return;
      setError(null);
      setCreating(true);
      try {
        const ws = await createWorkspace({ name: name.trim(), slug: slug.trim() });
        navigate(`/w/${ws.slug}`, { replace: true });
      } catch (err) {
        setError(err instanceof Error ? err.message : "Failed to create workspace");
      } finally {
        setCreating(false);
      }
    },
    [name, slug, createWorkspace, navigate],
  );

  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4">
      <Card className="w-full max-w-md">
        <CardHeader className="text-center">
          <div className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-xl bg-primary text-lg font-bold text-primary-foreground">
            M
          </div>
          <CardTitle className="text-2xl">Welcome to EVC Mesh</CardTitle>
          <CardDescription>
            Create your first workspace to get started.
          </CardDescription>
        </CardHeader>
        <form onSubmit={handleSubmit}>
          <CardContent className="space-y-4">
            {error && (
              <div className="rounded-lg bg-destructive/10 p-3 text-sm text-destructive">
                {error}
              </div>
            )}
            <div className="space-y-2">
              <label htmlFor="ws-name" className="text-sm font-medium">
                Workspace name
              </label>
              <Input
                id="ws-name"
                placeholder="My Team"
                value={name}
                onChange={(e) => handleNameChange(e.target.value)}
                required
                autoFocus
              />
            </div>
            <div className="space-y-2">
              <label htmlFor="ws-slug" className="text-sm font-medium">
                Slug
              </label>
              <Input
                id="ws-slug"
                placeholder="my-team"
                value={slug}
                onChange={(e) => setSlug(e.target.value)}
                required
              />
              <p className="text-xs text-muted-foreground">
                Used in URLs: /w/{slug || "..."}
              </p>
            </div>
          </CardContent>
          <CardFooter>
            <Button type="submit" className="w-full" disabled={creating}>
              {creating ? "Creating..." : "Create workspace"}
            </Button>
          </CardFooter>
        </form>
      </Card>
    </div>
  );
}
