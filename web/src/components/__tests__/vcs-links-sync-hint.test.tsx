import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

// mock: external boundary — the HTTP API the component fetches links from.
const mockedApi = vi.fn();
vi.mock("@/lib/api", () => ({
  api: (...args: unknown[]) => mockedApi(...(args as [string])),
  getAccessToken: vi.fn(() => null),
}));

import { VCSLinks } from "@/components/vcs-links";
import type { VCSLink } from "@/types";

const NOW = new Date("2026-10-09T12:00:00Z");

function link(over: Partial<VCSLink>): VCSLink {
  return {
    id: crypto.randomUUID(),
    task_id: "t1",
    provider: "gitlab",
    link_type: "pr",
    external_id: "42",
    url: "https://git.example.com/g/p/-/merge_requests/42",
    title: "Some MR",
    status: "open",
    metadata: {},
    created_at: "2026-10-01T00:00:00Z",
    ...over,
  };
}

async function renderWith(links: VCSLink[]) {
  mockedApi.mockResolvedValueOnce({ vcs_links: links });
  render(<VCSLinks taskId="t1" />);
  await screen.findByText("VCS Links");
}

describe("VCSLinks open-PR freshness hint", () => {
  beforeEach(() => {
    mockedApi.mockReset();
    // mock: system time — makes the relative wording deterministic.
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(NOW);
  });
  afterEach(() => vi.useRealTimers());

  it("shows relative sync time with the absolute timestamp in the title for an open PR", async () => {
    await renderWith([link({ status_synced_at: "2026-10-09T09:00:00Z" })]);
    const hint = screen.getByTestId("pr-sync-hint");
    expect(hint.textContent).toBe("synced about 3 hours ago");
    expect(hint.getAttribute("title")).toMatch(/^Status last confirmed Oct 9, 2026 \d\d:\d\d$/);
  });

  it("says 'not verified' for an open PR never confirmed by a webhook (null or missing)", async () => {
    await renderWith([link({ status_synced_at: null }), link({ id: "legacy" })]);
    const hints = screen.getAllByTestId("pr-sync-hint");
    expect(hints).toHaveLength(2);
    for (const h of hints) expect(h.textContent).toBe("not verified");
  });

  it("shows no hint on terminal merged/closed PRs or on non-PR links", async () => {
    await renderWith([
      link({ status: "merged", status_synced_at: null }),
      link({ status: "closed", status_synced_at: "2026-01-01T00:00:00Z" }),
      link({ link_type: "commit", status: "", external_id: "abc123" }),
    ]);
    expect(screen.getByText("Merged")).toBeTruthy();
    expect(screen.getByText("Closed")).toBeTruthy();
    expect(screen.queryByTestId("pr-sync-hint")).toBeNull();
  });
});
