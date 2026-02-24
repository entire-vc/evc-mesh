import { Component, type ErrorInfo, type ReactNode } from "react";
import { BrowserRouter, Navigate, Route, Routes } from "react-router";
import { AppLayout } from "@/components/layout/app-layout";
import { LoginPage } from "@/pages/login";
import { RegisterPage } from "@/pages/register";
import { DashboardPage } from "@/pages/dashboard";
import { BoardPage } from "@/pages/board";
import { ListViewPage } from "@/pages/list-view";
import { TaskDetailPage } from "@/pages/task-detail";
import { ProjectSettingsPage } from "@/pages/project-settings";
import { AgentDashboardPage } from "@/pages/agent-dashboard";
import { EventFeedPage } from "@/pages/event-feed";
import { TimelinePage } from "@/pages/timeline";

class ErrorBoundary extends Component<
  { children: ReactNode },
  { error: Error | null }
> {
  state: { error: Error | null } = { error: null };

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("React ErrorBoundary caught:", error, info.componentStack);
  }

  render() {
    if (this.state.error) {
      return (
        <div style={{ padding: 40, fontFamily: "monospace" }}>
          <h1 style={{ color: "red" }}>Application Error</h1>
          <pre style={{ whiteSpace: "pre-wrap", marginTop: 16 }}>
            {this.state.error.message}
          </pre>
          <pre
            style={{ whiteSpace: "pre-wrap", marginTop: 8, color: "#666" }}
          >
            {this.state.error.stack}
          </pre>
          <button
            onClick={() => {
              this.setState({ error: null });
              window.location.href = "/";
            }}
            style={{ marginTop: 16, padding: "8px 16px", cursor: "pointer" }}
          >
            Reload
          </button>
        </div>
      );
    }
    return this.props.children;
  }
}

export function App() {
  return (
    <ErrorBoundary>
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="/register" element={<RegisterPage />} />
        <Route element={<AppLayout />}>
          {/* Index route is handled by AppLayout redirects — no element needed */}
          <Route index element={null} />
          <Route path="w/:wsSlug" element={<DashboardPage />} />
          <Route
            path="w/:wsSlug/agents"
            element={<AgentDashboardPage />}
          />
          <Route
            path="w/:wsSlug/events"
            element={<EventFeedPage />}
          />
          <Route path="w/:wsSlug/p/:projectSlug" element={<BoardPage />} />
          <Route
            path="w/:wsSlug/p/:projectSlug/list"
            element={<ListViewPage />}
          />
          <Route
            path="w/:wsSlug/p/:projectSlug/timeline"
            element={<TimelinePage />}
          />
          <Route
            path="w/:wsSlug/p/:projectSlug/t/:taskId"
            element={<TaskDetailPage />}
          />
          <Route
            path="w/:wsSlug/p/:projectSlug/settings"
            element={<ProjectSettingsPage />}
          />
        </Route>
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </BrowserRouter>
    </ErrorBoundary>
  );
}
