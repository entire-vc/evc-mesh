// Backend-free harness for the statistics page's controls: the real Controls and UsagePanel inside the
// page's own wrapper (`main` p-3 md:p-6, the page's @container column), state from the URL as on /w/:ws/stats. An
// admin's capabilities, so the «Pool / account» filter is kept. The authed-e2e spec checks the same on the real page.
import { useState } from "react";
import { createRoot } from "react-dom/client";
import { Controls } from "../src/core/agent-usage/controls";
import { useST } from "../src/core/agent-usage/format";
import { stateFromSearch, type StatsState } from "../src/core/agent-usage/state";
import { UsagePanel } from "../src/core/agent-usage/usage-panel";
import { usageFixture } from "../src/core/agent-usage/__tests__/fixtures";
import "@/i18n";
import "../src/index.css";

function Page() {
  const st = useST();
  const [state, setState] = useState<StatsState>(() => stateFromSearch(new URLSearchParams(location.search)));
  const data = usageFixture();
  return (
    <main className="p-3 md:p-6">
      <div className="mx-auto flex w-full max-w-[1600px] flex-col gap-4 pb-10 @container">
        <h1 className="truncate text-lg font-semibold md:text-2xl">Agent statistics</h1>
        <Controls state={state} onChange={setState} caps={data.capabilities} st={st} />
        <UsagePanel data={data} state={state} onChange={setState} mode="page" links={{ openHref: () => undefined, open: () => {}, copySlice: () => {} }} st={st} />
      </div>
    </main>
  );
}

createRoot(document.getElementById("root")!).render(<Page />);
