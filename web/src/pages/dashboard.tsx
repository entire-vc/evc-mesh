import { DashboardContainer } from "@/core/dashboard";

// The page is only a container: the layout and every widget's view come from
// the active theme's dashboard.* slots (mesh-default unless a theme says otherwise).
export function DashboardPage() {
  return <DashboardContainer />;
}
