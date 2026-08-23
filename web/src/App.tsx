import { Component, type ErrorInfo, type ReactNode } from "react";
import { BrowserRouter, Navigate, Route, Routes } from "react-router";
import { AppLayout } from "@/components/layout/app-layout";
import { LoginPage } from "@/pages/login";
import { RegisterPage } from "@/pages/register";
import { BoardPage } from "@/pages/board";
import { ListViewPage } from "@/pages/list-view";
import { TaskDetailPage } from "@/pages/task-detail";
import { TaskCreatePage } from "@/pages/task-create";
import { ProjectSettingsPage } from "@/pages/project-settings";
import { EventFeedPage } from "@/pages/event-feed";
import { TimelinePage } from "@/pages/timeline";
import { SparkPage } from "@/pages/spark";
import { AnalyticsPage } from "@/pages/analytics";
import { IntegrationsPage } from "@/pages/integrations";
import { InitiativesPage } from "@/pages/initiatives";
import { TriagePage } from "@/pages/triage";
import { ProjectUpdatesPage } from "@/pages/project-updates";
import { CalendarPage } from "@/pages/calendar";
import { DocsPage } from "@/pages/docs";
import { WorkspaceSettingsPage } from "@/pages/workspace-settings";
import NotificationSettingsPage from "@/pages/notification-settings";
import { OrgChartPage } from "@/pages/org-chart";
import { TeamMemberPage } from "@/pages/team-member";
import { MemoryBrowserPage } from "@/pages/memory-browser";
import { SessionDashboardPage } from "@/pages/session-dashboard";
import { ActivityPage } from "@/pages/activity-page";
import { DashboardPage } from "@/pages/dashboard";
import { TaskDeepLinkResolver } from "@/pages/task-deep-link";
import { AcceptInvitePage } from "@/pages/accept-invite";

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
        <Route path="/accept-invite/:token" element={<AcceptInvitePage />} />
        <Route element={<AppLayout />}>
          {/* Index route is handled by AppLayout redirects — no element needed */}
          <Route index element={null} />
          <Route path="w/:wsSlug" element={<Navigate to="dashboard" replace />} />
          <Route path="w/:wsSlug/dashboard" element={<DashboardPage />} />
          <Route
            path="w/:wsSlug/org-chart"
            element={<OrgChartPage />}
          />
          <Route
            path="w/:wsSlug/org-chart/grid"
            element={<OrgChartPage />}
          />
          <Route
            path="w/:wsSlug/team/:kind/:memberSlug"
            element={<TeamMemberPage />}
          />
          <Route
            path="w/:wsSlug/memories"
            element={<MemoryBrowserPage />}
          />
          <Route
            path="w/:wsSlug/sessions"
            element={<SessionDashboardPage />}
          />
          <Route
            path="w/:wsSlug/spark"
            element={<SparkPage />}
          />
          <Route
            path="w/:wsSlug/events"
            element={<EventFeedPage />}
          />
          <Route
            path="w/:wsSlug/analytics"
            element={<AnalyticsPage />}
          />
          <Route
            path="w/:wsSlug/integrations"
            element={<IntegrationsPage />}
          />
          <Route
            path="w/:wsSlug/initiatives"
            element={<InitiativesPage />}
          />
          <Route
            path="w/:wsSlug/triage"
            element={<TriagePage />}
          />
          <Route
            path="w/:wsSlug/activity"
            element={<ActivityPage />}
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
            path="w/:wsSlug/p/:projectSlug/calendar"
            element={<CalendarPage />}
          />
          <Route
            path="w/:wsSlug/p/:projectSlug/docs"
            element={<DocsPage />}
          />
          {/* A selected document is its own URL so it can be linked and
              reloaded. It must be registered here: the catch-all below
              redirects anything unrouted to "/" without a word. */}
          <Route
            path="w/:wsSlug/p/:projectSlug/docs/:docId"
            element={<DocsPage />}
          />
          <Route
            path="w/:wsSlug/p/:projectSlug/updates"
            element={<ProjectUpdatesPage />}
          />
          <Route
            path="w/:wsSlug/p/:projectSlug/new"
            element={<TaskCreatePage />}
          />
          <Route
            path="w/:wsSlug/p/:projectSlug/t/:taskId"
            element={<TaskDetailPage />}
          />
          <Route
            path="w/:wsSlug/settings"
            element={<WorkspaceSettingsPage />}
          />
          <Route
            path="w/:wsSlug/notifications"
            element={<NotificationSettingsPage />}
          />
          <Route
            path="w/:wsSlug/p/:projectSlug/settings"
            element={<ProjectSettingsPage />}
          />
          <Route path="t/:taskId" element={<TaskDeepLinkResolver />} />
          <Route path="tasks/:taskId" element={<TaskDeepLinkResolver />} />
        </Route>
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </BrowserRouter>
    </ErrorBoundary>
  );
}
