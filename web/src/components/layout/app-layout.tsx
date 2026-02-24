import { useCallback, useEffect, useState } from "react";
import { Navigate, Outlet, useParams } from "react-router";
import { useAuthStore } from "@/stores/auth";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";
import { useWebSocketStore } from "@/stores/websocket";
import { Sidebar } from "./sidebar";
import { Header } from "./header";
import { Skeleton } from "@/components/ui/skeleton";

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

  // Redirect to first workspace if no ws in URL
  if (initialized && !wsSlug && workspaces.length > 0) {
    return <Navigate to={`/w/${workspaces[0]!.slug}`} replace />;
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
