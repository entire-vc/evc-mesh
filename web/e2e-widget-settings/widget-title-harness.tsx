// Backend-free harness for §5.6 «the title replaces the type name»: the real dashboard <Widget> of several core
// widgets in view mode, each with the instance title the editor saves (settings.title), and one Mentions without a
// title as the control. No workspace, so the bodies stay in their loading state; the headers are what is checked.
import { createRoot } from "react-dom/client";
import { MemoryRouter } from "react-router";
import { Widget } from "../src/core/dashboard/slots";
import "@/i18n";
import "../src/index.css";

export const CUSTOM = "QA96 custom mentions";
const cells: { type: string; title?: string }[] = [
  { type: "mentions", title: CUSTOM },
  { type: "mentions" },
  { type: "my-tasks", title: "QA96 my queue" },
  { type: "event-feed", title: "QA96 feed" },
  { type: "cost-kpis", title: "QA96 spend" },
  { type: "recent-comments", title: "QA96 comments" },
];

function Page() {
  return (
    <MemoryRouter initialEntries={["/w/acme/dashboard"]}>
      <div className="grid grid-cols-1 gap-4 px-4 py-4 md:grid-cols-2">
        {cells.map((c, i) => (
          <div key={i} data-cell={`${c.type}${c.title ? "" : "-default"}`} className="flex min-h-0 flex-col">
            <Widget instance={{ id: `i${i}`, type: c.type, x: 0, y: 0, w: 6, h: 3, settings: c.title ? { title: c.title } : {} }} />
          </div>
        ))}
      </div>
    </MemoryRouter>
  );
}

createRoot(document.getElementById("root")!).render(<Page />);
