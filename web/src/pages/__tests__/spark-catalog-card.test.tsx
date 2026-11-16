import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SparkAgentCard, SparkPage, sparkAgentUrl } from "../spark";
import { setLanguage } from "@/i18n";
import { useWorkspaceStore } from "@/stores/workspace";
import { useSparkStore } from "@/stores/spark";
import { useCapabilitiesStore } from "@/stores/capabilities";
import type { SparkAgentManifest } from "@/types";

const SPARK = "https://spark.example.test";
const workspace = { id: "ws1", slug: "acme", name: "Acme" } as never;

function manifest(over: Partial<SparkAgentManifest> = {}): SparkAgentManifest {
  return {
    id: "a1",
    slug: "code-reviewer",
    name: "Code Reviewer",
    description: "Reviews diffs",
    agent_type: "claude_code",
    version: "1.2.0",
    author: "acme",
    capabilities: {},
    config: {},
    tags: ["backend"],
    downloads: 42,
    rating: 4.5,
    created_at: "2026-11-01T00:00:00Z",
    ...over,
  };
}

const popular = [manifest(), manifest({ id: "a2", slug: "", name: "Docs Writer & Co" })];
const found = [manifest({ id: "a3", slug: "infra-bot", name: "Infra Bot", tags: ["infra"] })];

// Network boundary: the integration gate and the catalog endpoints.
function stubFetch() {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      const u = String(url);
      const body = u.includes("/integrations")
        ? { integrations: [{ provider: "spark", is_active: true }] }
        : u.includes("/spark/agents/popular")
          ? { items: popular, count: popular.length }
          : u.includes("/spark/agents")
            ? { items: found, count: found.length }
            : {};
      return new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
}

let openSpy: ReturnType<typeof vi.fn>;

beforeEach(async () => {
  await setLanguage("en");
  stubFetch();
  openSpy = vi.fn();
  vi.stubGlobal("open", openSpy);
  useWorkspaceStore.setState({ workspaces: [workspace], currentWorkspace: workspace, isLoading: false });
  useCapabilitiesStore.setState({ sparkEnabled: true, sparkUrl: SPARK, fetched: true });
  useSparkStore.setState({ agents: [], popularAgents: [], isLoading: false, error: null });
});
afterEach(() => vi.unstubAllGlobals());

describe("sparkAgentUrl", () => {
  it("links to the agent page by slug", () => {
    expect(sparkAgentUrl(SPARK, manifest())).toBe(`${SPARK}/agents/code-reviewer`);
  });
  it("falls back to a catalog search by the encoded name without a slug", () => {
    expect(sparkAgentUrl(SPARK, manifest({ slug: "", name: "Docs Writer & Co" }))).toBe(
      `${SPARK}/catalog?q=Docs%20Writer%20%26%20Co`,
    );
  });
});

describe("SparkAgentCard — Open is the only action", () => {
  it("renders Open and no Install", () => {
    render(<SparkAgentCard agent={manifest()} onOpen={() => {}} />);
    expect(screen.getByRole("button", { name: /open/i })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /install/i })).not.toBeInTheDocument();
    expect(screen.queryByText(/install/i)).not.toBeInTheDocument();
    expect(screen.getAllByRole("button")).toHaveLength(1);
  });

  it("Open spans the card width", () => {
    render(<SparkAgentCard agent={manifest()} onOpen={() => {}} />);
    expect(screen.getByRole("button", { name: /open/i })).toHaveClass("w-full");
  });

  it("the Open button and a click on the card both call onOpen once", () => {
    const onOpen = vi.fn();
    render(<SparkAgentCard agent={manifest()} onOpen={onOpen} />);
    fireEvent.click(screen.getByRole("button", { name: /open/i }));
    expect(onOpen).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByText("Code Reviewer"));
    expect(onOpen).toHaveBeenCalledTimes(2);
  });
});

const renderPage = () =>
  render(
    <MemoryRouter initialEntries={["/w/acme/spark"]}>
      <Routes>
        <Route path="/w/:wsSlug/spark" element={<SparkPage />} />
      </Routes>
    </MemoryRouter>,
  );

describe("SparkPage — no Install anywhere in the catalog", () => {
  it("popular agents: Open only, opening the slug page or the name search in a new tab", async () => {
    renderPage();
    await screen.findByText("Code Reviewer");
    expect(screen.queryByRole("button", { name: /install/i })).not.toBeInTheDocument();
    const opens = screen.getAllByRole("button", { name: /^open$/i });
    expect(opens).toHaveLength(2);

    fireEvent.click(opens[0] as HTMLElement);
    expect(openSpy).toHaveBeenLastCalledWith(`${SPARK}/agents/code-reviewer`, "_blank", "noopener,noreferrer");
    fireEvent.click(opens[1] as HTMLElement);
    expect(openSpy).toHaveBeenLastCalledWith(
      `${SPARK}/catalog?q=Docs%20Writer%20%26%20Co`,
      "_blank",
      "noopener,noreferrer",
    );
  });

  it("search with a type and a tag filter: results have Open only", async () => {
    renderPage();
    await screen.findByText("Code Reviewer");
    fireEvent.change(screen.getByRole("combobox"), { target: { value: "claude_code" } });
    fireEvent.click(screen.getByRole("button", { name: "infra" }));
    fireEvent.change(screen.getByPlaceholderText(/search agents/i), { target: { value: "bot" } });
    fireEvent.click(screen.getByRole("button", { name: /^search$/i }));

    await screen.findByText("Infra Bot");
    await waitFor(() => expect(screen.queryByText("Code Reviewer")).not.toBeInTheDocument());
    expect(screen.getByText(/search results/i)).toBeInTheDocument();
    const searchUrl = vi
      .mocked(fetch)
      .mock.calls.map(([u]) => String(u))
      .find((u) => u.includes("/spark/agents?"));
    expect(searchUrl).toMatch(/q=bot/);
    expect(searchUrl).toMatch(/agent_type=claude_code/);
    expect(searchUrl).toMatch(/tags=infra/);
    expect(screen.queryByRole("button", { name: /install/i })).not.toBeInTheDocument();
    const opens = screen.getAllByRole("button", { name: /^open$/i });
    expect(opens).toHaveLength(1);
    fireEvent.click(opens[0] as HTMLElement);
    expect(openSpy).toHaveBeenLastCalledWith(`${SPARK}/agents/infra-bot`, "_blank", "noopener,noreferrer");
  });

  it("does not open anything when the deployment has no Spark URL", async () => {
    useCapabilitiesStore.setState({ sparkUrl: "" });
    renderPage();
    fireEvent.click(await screen.findByText("Code Reviewer"));
    expect(openSpy).not.toHaveBeenCalled();
  });
});
