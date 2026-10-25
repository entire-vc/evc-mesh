// Backend-free harness for the project tab strip: the real ViewTabBar in a memory
// router. `?impl=before` renders the strip from the merge-request base (written to
// view-tab-bar.before.tsx by the CI job), `?theme=dark` turns on the dark class.
import { createRoot } from "react-dom/client";
import { createMemoryRouter, RouterProvider, useLocation } from "react-router";
import type { ComponentType } from "react";
import { Puzzle } from "lucide-react";
import { ViewTabBar } from "../src/components/view-tab-bar";
import { BUILTIN_PROJECT_TABS, CHAT_PROJECT_TAB, type ProjectViewTabDescriptor } from "../src/lib/project-view-tabs";
import "@/i18n";
import "../src/index.css";

const params = new URLSearchParams(location.search);
if (params.get("theme") === "dark") document.documentElement.classList.add("dark");

type Strip = ComponentType<{ currentView: "board" | "list" | "calendar" | "docs" | "chat" | null; wsSlug: string; projectSlug: string; projectId?: string; tabs?: readonly ProjectViewTabDescriptor[] }>;
const before = import.meta.glob<{ ViewTabBar: Strip }>("./view-tab-bar.before.tsx", { eager: true });
const Bar: Strip = params.get("impl") === "before"
  ? Object.values(before)[0]?.ViewTabBar ?? (() => <p>no base strip</p>)
  : ViewTabBar;

// `?extra=1` adds a tab as data, the way an extension would; a strip with a fixed
// tab array ignores the prop and never shows it.
const tabs = params.get("extra") === "1"
  ? [...BUILTIN_PROJECT_TABS, { id: "extra", labelKey: "viewTabs.tabs.docs", Icon: Puzzle, path: (ws: string, proj: string) => `/w/${ws}/p/${proj}/extra`, overflowRank: 5 }]
  : params.get("chat") === "1"
    ? [...BUILTIN_PROJECT_TABS, CHAT_PROJECT_TAB]
    : undefined;

function Page() {
  const { pathname } = useLocation();
  const view = (["calendar", "docs", "list", "chat"] as const).find((v) => pathname.endsWith(`/${v}`)) ?? "board";
  return (
    <div className="p-2">
      <div className="mesh-mobile-header flex items-center border-b border-border"><Bar currentView={view} wsSlug="w" projectSlug="p" projectId={params.get("chat") === "1" ? "p" : undefined} tabs={tabs} /></div>
      <output data-testid="path" className="mt-2 block text-xs text-muted-foreground">{pathname}</output>
    </div>
  );
}

createRoot(document.getElementById("root")!).render(
  <RouterProvider router={createMemoryRouter([{ path: "*", element: <Page /> }], { initialEntries: [params.get("chat") === "1" ? "/w/p/chat" : "/w/p"] })} />,
);
