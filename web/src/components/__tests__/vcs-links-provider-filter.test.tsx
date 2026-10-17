import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";

// mock: external boundary — the HTTP API the component fetches links from.
const mockedApi = vi.fn();
vi.mock("@/lib/api", () => ({
  api: (...args: unknown[]) => mockedApi(...(args as [string])),
  getAccessToken: vi.fn(() => null),
}));

import { VCSLinks } from "@/components/vcs-links";
import type { VCSLink } from "@/types";

function link(provider: VCSLink["provider"], title: string): VCSLink {
  return {
    id: crypto.randomUUID(), task_id: "t1", provider, link_type: "pr", external_id: "1",
    url: `https://example.com/${title}`, title, status: "merged", metadata: {}, created_at: "2026-10-01T00:00:00Z",
  };
}
const LINKS = [link("github", "gh-pr"), link("gitlab", "gl-mr")];

describe("VCSLinks provider filter (extension panels own their provider's links)", () => {
  beforeEach(() => {
    mockedApi.mockReset();
    mockedApi.mockResolvedValue({ vcs_links: LINKS });
  });

  it("without a filter shows every provider", async () => {
    render(<VCSLinks taskId="t1" />);
    expect(await screen.findByText("gh-pr")).toBeInTheDocument();
    expect(screen.getByText("gl-mr")).toBeInTheDocument();
  });

  it("exclude hides the owned provider and drops it from the add form", async () => {
    render(<VCSLinks taskId="t1" exclude={["gitlab"]} />);
    expect(await screen.findByText("gh-pr")).toBeInTheDocument();
    expect(screen.queryByText("gl-mr")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /link pr/i }));
    expect(screen.queryByRole("option", { name: "GitLab" })).toBeNull();
    expect(screen.getByRole("option", { name: "GitHub" })).toBeInTheDocument();
  });

  it("only shows just that provider and posts new links with it", async () => {
    render(<VCSLinks taskId="t1" only="gitlab" />);
    expect(await screen.findByText("gl-mr")).toBeInTheDocument();
    expect(screen.queryByText("gh-pr")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /link pr/i }));
    expect(screen.queryByRole("option", { name: "GitHub" })).toBeNull();
    expect((screen.getAllByRole("combobox")[1] as HTMLSelectElement).value).toBe("gitlab");
  });
});
