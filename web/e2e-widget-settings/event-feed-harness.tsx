// Backend-free harness for the Events widget card: the real EventFeedWidgetView inside the dashboard cell's
// scroller (view-grid.tsx WidgetCell makes the card `overflow-auto`). No workspace, so the list stays in its
// loading state; the filter panel is what the spec measures. Page padding 28 px leaves a 337 px card at 393 px,
// the width the prod dashboard gives it (clientWidth 335 inside the 1 px border).
import { createRoot } from "react-dom/client";
import { EventFeedWidgetView } from "../src/core/dashboard/widgets/events/view";
import "@/i18n";
import "../src/index.css";

function Page() {
  return (
    <div className="px-7 py-4">
      <div className="flex min-h-0 flex-1 flex-col [&>[data-mesh-widget]>*]:min-h-0 [&>[data-mesh-widget]>*]:flex-1 [&>[data-mesh-widget]>*]:overflow-auto">
        <div data-mesh-widget="event-feed" className="flex flex-col">
          <EventFeedWidgetView settings={{}} size={{ w: 6, h: 4 }} />
        </div>
      </div>
    </div>
  );
}

createRoot(document.getElementById("root")!).render(<Page />);
