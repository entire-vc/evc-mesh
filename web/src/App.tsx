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

export function App() {
  return (
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
  );
}
