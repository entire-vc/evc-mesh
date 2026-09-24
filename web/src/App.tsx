import { Component, lazy, Suspense, type ReactNode, type ErrorInfo } from "react";
import {
  createBrowserRouter,
  createRoutesFromElements,
  Navigate,
  Route,
  RouterProvider,
} from "react-router";
import { AppLayout } from "@/components/layout/app-layout";
import { LoginPage } from "@/pages/login";
import { RegisterPage } from "@/pages/register";
import { PageSkeleton } from "@/components/page-skeleton";

// Everything below is route-split: none of it should ship in the JS that
// /login or /register load before first paint (perf·Б1, fleet-epic
// #3e9f036e — see web/perf/budget.json for the gated ceiling). Named
// exports need the `.then(m => ({ default: m.X }))` wrapper because
// React.lazy only resolves a default export.
const AcceptInvitePage = lazy(() =>
  import("@/pages/accept-invite").then((m) => ({ default: m.AcceptInvitePage })),
);
const DashboardPage = lazy(() =>
  import("@/pages/dashboard").then((m) => ({ default: m.DashboardPage })),
);
const OrgChartPage = lazy(() =>
  import("@/pages/org-chart").then((m) => ({ default: m.OrgChartPage })),
);
const TeamMemberPage = lazy(() =>
  import("@/pages/team-member").then((m) => ({ default: m.TeamMemberPage })),
);
const MemoryBrowserPage = lazy(() =>
  import("@/pages/memory-browser").then((m) => ({ default: m.MemoryBrowserPage })),
);
const SessionDashboardPage = lazy(() =>
  import("@/pages/session-dashboard").then((m) => ({
    default: m.SessionDashboardPage,
  })),
);
const SparkPage = lazy(() =>
  import("@/pages/spark").then((m) => ({ default: m.SparkPage })),
);
const EventFeedPage = lazy(() =>
  import("@/pages/event-feed").then((m) => ({ default: m.EventFeedPage })),
);
const AnalyticsPage = lazy(() =>
  import("@/pages/analytics").then((m) => ({ default: m.AnalyticsPage })),
);
const IntegrationsPage = lazy(() =>
  import("@/pages/integrations").then((m) => ({ default: m.IntegrationsPage })),
);
const InitiativesPage = lazy(() =>
  import("@/pages/initiatives").then((m) => ({ default: m.InitiativesPage })),
);
const TriagePage = lazy(() =>
  import("@/pages/triage").then((m) => ({ default: m.TriagePage })),
);
const ActivityPage = lazy(() =>
  import("@/pages/activity-page").then((m) => ({ default: m.ActivityPage })),
);
// BoardPage is the one route perf/*.spec.ts and the post-login prefetch
// (main.tsx) both name directly — keep this import path if renaming the
// chunk-producing module.
const BoardPage = lazy(() =>
  import("@/pages/board").then((m) => ({ default: m.BoardPage })),
);
const ListViewPage = lazy(() =>
  import("@/pages/list-view").then((m) => ({ default: m.ListViewPage })),
);
const TimelinePage = lazy(() =>
  import("@/pages/timeline").then((m) => ({ default: m.TimelinePage })),
);
const CalendarPage = lazy(() =>
  import("@/pages/calendar").then((m) => ({ default: m.CalendarPage })),
);
const DocsPage = lazy(() =>
  import("@/pages/docs").then((m) => ({ default: m.DocsPage })),
);
const ProjectUpdatesPage = lazy(() =>
  import("@/pages/project-updates").then((m) => ({
    default: m.ProjectUpdatesPage,
  })),
);
const TaskCreatePage = lazy(() =>
  import("@/pages/task-create").then((m) => ({ default: m.TaskCreatePage })),
);
const TaskDetailPage = lazy(() =>
  import("@/pages/task-detail").then((m) => ({ default: m.TaskDetailPage })),
);
const WorkspaceSettingsPage = lazy(() =>
  import("@/pages/workspace-settings").then((m) => ({
    default: m.WorkspaceSettingsPage,
  })),
);
const NotificationSettingsPage = lazy(
  () => import("@/pages/notification-settings"),
);
const ProjectSettingsPage = lazy(() =>
  import("@/pages/project-settings").then((m) => ({
    default: m.ProjectSettingsPage,
  })),
);
const TaskDeepLinkResolver = lazy(() =>
  import("@/pages/task-deep-link").then((m) => ({
    default: m.TaskDeepLinkResolver,
  })),
);
const DocumentDeepLinkResolver = lazy(() =>
  import("@/pages/document-deep-link").then((m) => ({
    default: m.DocumentDeepLinkResolver,
  })),
);

/** Wraps a lazy page element in the shared Suspense boundary. AppLayout's
 * <Outlet/> sits inside a flex-1 region whose height comes from the flex
 * layout, not from its content, so swapping the skeleton for the real page
 * never changes that region's height — no CLS from the swap itself. */
function LazyPage({ children }: { children: ReactNode }) {
  return <Suspense fallback={<PageSkeleton />}>{children}</Suspense>;
}

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

// Data router: required so useBlocker (react-router) can intercept in-app
// navigation away from a dirty draft (task #7893ab16) — useBlocker throws
// outside a data router context. Built once at module scope, not inside
// App(), so remounts don't tear down router state/history.
const router = createBrowserRouter(
  createRoutesFromElements(
    <>
      <Route path="/login" element={<LoginPage />} />
      <Route path="/register" element={<RegisterPage />} />
      <Route
        path="/accept-invite/:token"
        element={
          <LazyPage>
            <AcceptInvitePage />
          </LazyPage>
        }
      />
      <Route element={<AppLayout />}>
        {/* Index route is handled by AppLayout redirects — no element needed */}
        <Route index element={null} />
        <Route path="w/:wsSlug" element={<Navigate to="dashboard" replace />} />
        <Route
          path="w/:wsSlug/dashboard"
          element={
            <LazyPage>
              <DashboardPage />
            </LazyPage>
          }
        />
        <Route
          path="w/:wsSlug/org-chart"
          element={
            <LazyPage>
              <OrgChartPage />
            </LazyPage>
          }
        />
        <Route
          path="w/:wsSlug/org-chart/grid"
          element={
            <LazyPage>
              <OrgChartPage />
            </LazyPage>
          }
        />
        <Route
          path="w/:wsSlug/team/:kind/:memberSlug"
          element={
            <LazyPage>
              <TeamMemberPage />
            </LazyPage>
          }
        />
        <Route
          path="w/:wsSlug/memories"
          element={
            <LazyPage>
              <MemoryBrowserPage />
            </LazyPage>
          }
        />
        <Route
          path="w/:wsSlug/sessions"
          element={
            <LazyPage>
              <SessionDashboardPage />
            </LazyPage>
          }
        />
        <Route
          path="w/:wsSlug/spark"
          element={
            <LazyPage>
              <SparkPage />
            </LazyPage>
          }
        />
        <Route
          path="w/:wsSlug/events"
          element={
            <LazyPage>
              <EventFeedPage />
            </LazyPage>
          }
        />
        <Route
          path="w/:wsSlug/analytics"
          element={
            <LazyPage>
              <AnalyticsPage />
            </LazyPage>
          }
        />
        <Route
          path="w/:wsSlug/integrations"
          element={
            <LazyPage>
              <IntegrationsPage />
            </LazyPage>
          }
        />
        <Route
          path="w/:wsSlug/initiatives"
          element={
            <LazyPage>
              <InitiativesPage />
            </LazyPage>
          }
        />
        <Route
          path="w/:wsSlug/triage"
          element={
            <LazyPage>
              <TriagePage />
            </LazyPage>
          }
        />
        <Route
          path="w/:wsSlug/activity"
          element={
            <LazyPage>
              <ActivityPage />
            </LazyPage>
          }
        />
        <Route
          path="w/:wsSlug/p/:projectSlug"
          element={
            <LazyPage>
              <BoardPage />
            </LazyPage>
          }
        />
        <Route
          path="w/:wsSlug/p/:projectSlug/list"
          element={
            <LazyPage>
              <ListViewPage />
            </LazyPage>
          }
        />
        <Route
          path="w/:wsSlug/p/:projectSlug/timeline"
          element={
            <LazyPage>
              <TimelinePage />
            </LazyPage>
          }
        />
        <Route
          path="w/:wsSlug/p/:projectSlug/calendar"
          element={
            <LazyPage>
              <CalendarPage />
            </LazyPage>
          }
        />
        <Route
          path="w/:wsSlug/p/:projectSlug/docs"
          element={
            <LazyPage>
              <DocsPage />
            </LazyPage>
          }
        />
        {/* A selected document is its own URL so it can be linked and
            reloaded. It must be registered here: the catch-all below
            redirects anything unrouted to "/" without a word. */}
        <Route
          path="w/:wsSlug/p/:projectSlug/docs/:docId"
          element={
            <LazyPage>
              <DocsPage />
            </LazyPage>
          }
        />
        <Route
          path="w/:wsSlug/p/:projectSlug/updates"
          element={
            <LazyPage>
              <ProjectUpdatesPage />
            </LazyPage>
          }
        />
        <Route
          path="w/:wsSlug/p/:projectSlug/new"
          element={
            <LazyPage>
              <TaskCreatePage />
            </LazyPage>
          }
        />
        <Route
          path="w/:wsSlug/p/:projectSlug/t/:taskId"
          element={
            <LazyPage>
              <TaskDetailPage />
            </LazyPage>
          }
        />
        <Route
          path="w/:wsSlug/settings"
          element={
            <LazyPage>
              <WorkspaceSettingsPage />
            </LazyPage>
          }
        />
        <Route
          path="w/:wsSlug/notifications"
          element={
            <LazyPage>
              <NotificationSettingsPage />
            </LazyPage>
          }
        />
        <Route
          path="w/:wsSlug/p/:projectSlug/settings"
          element={
            <LazyPage>
              <ProjectSettingsPage />
            </LazyPage>
          }
        />
        <Route
          path="t/:taskId"
          element={
            <LazyPage>
              <TaskDeepLinkResolver />
            </LazyPage>
          }
        />
        <Route
          path="tasks/:taskId"
          element={
            <LazyPage>
              <TaskDeepLinkResolver />
            </LazyPage>
          }
        />
        <Route
          path="d/:docId"
          element={
            <LazyPage>
              <DocumentDeepLinkResolver />
            </LazyPage>
          }
        />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </>,
  ),
);

export function App() {
  return (
    <ErrorBoundary>
      <RouterProvider router={router} />
    </ErrorBoundary>
  );
}
