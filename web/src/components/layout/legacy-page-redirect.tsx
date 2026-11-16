// The six pages the dashboards replaced (PRD dashboards §5.9) keep their addresses only as redirects:
// an old bookmark or a link from a letter opens the «New dashboard» dialog with the page's template,
// on top of the workspace's home dashboard. The dialog lives with the menu (use-dashboards-nav).
import { Navigate, useParams } from "react-router";

/** Query parameter that opens the create dialog with this template preselected. */
export const NEW_DASHBOARD_PARAM = "new-dashboard";

/** Old page → the server template with its composition (internal/extensions/dashboards/templates.go). */
export const LEGACY_PAGE_TEMPLATES = {
  initiatives: "initiatives",
  triage: "triage",
  activity: "activity",
  sessions: "agent-sessions",
  analytics: "analytics",
  events: "events",
} as const;

export type LegacyPage = keyof typeof LEGACY_PAGE_TEMPLATES;

export function newDashboardHref(wsSlug: string, template: string): string {
  return `/w/${wsSlug}/dashboard?${NEW_DASHBOARD_PARAM}=${encodeURIComponent(template)}`;
}

export function LegacyPageRedirect({ page }: { page: LegacyPage }) {
  const { wsSlug } = useParams<{ wsSlug: string }>();
  return <Navigate to={wsSlug ? newDashboardHref(wsSlug, LEGACY_PAGE_TEMPLATES[page]) : "/"} replace />;
}
