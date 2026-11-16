import { useParams } from "react-router";
import { DashboardEntry, DashboardPage as DashboardView } from "@/core/dashboard/page/dashboard-page";

// One lazy page for w/:ws, w/:ws/dashboard (→ the home dashboard) and w/:ws/d/:docId (the dashboard itself).
export function DashboardPage() {
  return useParams().docId ? <DashboardView /> : <DashboardEntry />;
}
