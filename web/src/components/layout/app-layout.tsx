import { type FormEvent, useCallback, useEffect, useState } from "react";
import { Navigate, Outlet, useNavigate, useParams } from "react-router";
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
  const { fetchProjects, setCurrentProjectBySlug, currentProject } =
    useProjectStore();
  const wsConnect = useWebSocketStore((s) => s.connect);
  const wsDisconnect = useWebSocketStore((s) => s.disconnect);

  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const [initialized, setInitialized] = useState(false);

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

  // Resolve project slug
  useEffect(() => {
    if (projectSlug) {
      setCurrentProjectBySlug(projectSlug);
    } else if (!projectSlug && currentProject) {
      // Clear current project when navigating away from a project route
      useProjectStore.setState({ currentProject: null });
    }
  }, [projectSlug, setCurrentProjectBySlug, currentProject]);

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
    return <Navigate to="/login" replace />;
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

  // Redirect to first workspace if no ws in URL
  if (!wsSlug && workspaces.length > 0) {
    return <Navigate to={`/w/${workspaces[0]!.slug}`} replace />;
  }

  // No workspaces — show create workspace screen
  if (workspaces.length === 0) {
    return <NoWorkspacesScreen />;
  }

  return (
    <div className="flex h-screen bg-background">
      <Sidebar collapsed={sidebarCollapsed} />
      <div className="flex flex-1 flex-col overflow-hidden">
        <Header onToggleSidebar={toggleSidebar} />
        <main className="flex-1 overflow-y-auto p-6">
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
