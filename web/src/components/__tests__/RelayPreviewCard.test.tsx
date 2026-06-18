import { afterEach, beforeEach, describe, it, expect, vi } from "vitest";
import { render, screen, act } from "@testing-library/react";
import { RelayPreviewCard } from "@/components/RelayPreviewCard";
import { MarkdownWithRelay } from "@/components/MarkdownWithRelay";

// Mock useNavigate — MarkdownRenderer uses react-router
vi.mock("react-router", () => ({
  useNavigate: () => vi.fn(),
}));

// Mock import.meta.env
vi.stubEnv("VITE_RELAY_PUBLISH_BASE_URL", "");

// Mock api() so projId-path tests can control API responses without a real server.
// Existing tests render without projId so api() is never invoked — mock doesn't affect them.
vi.mock("@/lib/api", () => ({
  api: vi.fn(),
}));
import { api } from "@/lib/api";

describe("RelayPreviewCard", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("renders doc name in card header", () => {
    render(
      <RelayPreviewCard relayUrl="relay://relay.entire.host/shares/demo/docs/Architecture.md" />,
    );
    expect(screen.getByText("Architecture")).toBeInTheDocument();
  });

  it("uses explicit label when provided", () => {
    render(
      <RelayPreviewCard
        relayUrl="relay://relay.entire.host/shares/demo/docs/Architecture.md"
        label="My Custom Label"
      />,
    );
    expect(screen.getByText("My Custom Label")).toBeInTheDocument();
  });

  it("renders Open link pointing to https:// resolved URL", () => {
    render(
      <RelayPreviewCard relayUrl="relay://relay.entire.host/shares/demo/docs/Notes.md" />,
    );
    const links = screen.getAllByRole("link");
    const openLink = links.find((l) => l.getAttribute("href") === "https://relay.entire.host/shares/demo/docs/Notes.md");
    expect(openLink).toBeDefined();
    expect(openLink).toHaveAttribute("target", "_blank");
  });

  it("renders iframe with resolved src", () => {
    render(
      <RelayPreviewCard relayUrl="relay://relay.entire.host/shares/5ba2a6c4/docs/spec.md" />,
    );
    const iframe = document.querySelector("iframe");
    expect(iframe).not.toBeNull();
    expect(iframe?.src).toBe("https://relay.entire.host/shares/5ba2a6c4/docs/spec.md");
  });

  it("shows fallback after load timeout (simulates X-Frame-Options block)", async () => {
    render(
      <RelayPreviewCard relayUrl="relay://relay.entire.host/shares/demo/docs/Blocked.md" />,
    );
    // Iframe is visible initially (loading state)
    expect(document.querySelector("iframe")).not.toBeNull();

    // Advance time past the 6s load timeout — state should flip to 'failed'
    await act(async () => {
      vi.advanceTimersByTime(7000);
    });

    // Fallback text link should appear, iframe should be gone
    expect(document.querySelector("iframe")).toBeNull();
    const fallbackLinks = screen.getAllByRole("link");
    const fallback = fallbackLinks.find((l) =>
      l.textContent?.includes("Open in Team Relay"),
    );
    expect(fallback).toBeDefined();
  });
});

describe("RelayPreviewCard — authenticated projId path (D5-b embed-token)", () => {
  const RELAY_URL = "relay://relay.entire.host/shares/5ba2a6c4/docs/Spec.md";
  const HTTPS_URL = "https://relay.entire.host/shares/5ba2a6c4/docs/Spec.md";
  const EMBED_URL = "https://relay.entire.host/shares/5ba2a6c4/docs/Spec.md?embed_token=tok123abc";

  beforeEach(() => {
    vi.useFakeTimers();
    vi.mocked(api).mockReset();
  });
  afterEach(() => vi.useRealTimers());

  it("shows Loading… initially while API call is in flight", () => {
    // API never resolves — simulates slow network
    vi.mocked(api).mockReturnValue(new Promise(() => {}));
    render(<RelayPreviewCard relayUrl={RELAY_URL} projId="proj-uuid" />);
    expect(screen.getByText("Loading…")).toBeInTheDocument();
    expect(document.querySelector("iframe")).toBeNull();
  });

  it("renders iframe with embed_token URL when API returns available+iframe_src", async () => {
    vi.mocked(api).mockResolvedValue({ available: true, iframe_src: EMBED_URL });
    render(<RelayPreviewCard relayUrl={RELAY_URL} projId="proj-uuid" />);
    // Flush promise resolution + React state updates
    await act(async () => {});
    const iframe = document.querySelector("iframe");
    expect(iframe).not.toBeNull();
    expect(iframe?.getAttribute("src")).toBe(EMBED_URL);
    // Link-card fallback must NOT be visible yet
    expect(screen.queryByText("Open in Team Relay")).toBeNull();
  });

  it("falls back to plain https:// when API returns available=false", async () => {
    vi.mocked(api).mockResolvedValue({ available: false });
    render(<RelayPreviewCard relayUrl={RELAY_URL} projId="proj-uuid" />);
    await act(async () => {});
    const iframe = document.querySelector("iframe");
    expect(iframe).not.toBeNull();
    expect(iframe?.getAttribute("src")).toBe(HTTPS_URL);
  });

  it("falls back to plain https:// when API returns no iframe_src", async () => {
    vi.mocked(api).mockResolvedValue({ available: true });
    render(<RelayPreviewCard relayUrl={RELAY_URL} projId="proj-uuid" />);
    await act(async () => {});
    const iframe = document.querySelector("iframe");
    expect(iframe).not.toBeNull();
    expect(iframe?.getAttribute("src")).toBe(HTTPS_URL);
  });

  it("falls back to plain https:// when API call rejects (network error)", async () => {
    vi.mocked(api).mockRejectedValue(new Error("network error"));
    render(<RelayPreviewCard relayUrl={RELAY_URL} projId="proj-uuid" />);
    await act(async () => {});
    const iframe = document.querySelector("iframe");
    expect(iframe).not.toBeNull();
    expect(iframe?.getAttribute("src")).toBe(HTTPS_URL);
  });

  it("shows link-card fallback when embed iframe fails to load within 6s", async () => {
    vi.mocked(api).mockResolvedValue({ available: true, iframe_src: EMBED_URL });
    render(<RelayPreviewCard relayUrl={RELAY_URL} projId="proj-uuid" />);
    await act(async () => {});
    // Iframe should be rendering
    expect(document.querySelector("iframe")).not.toBeNull();

    // Advance time past the 6s load timeout
    await act(async () => {
      vi.advanceTimersByTime(7000);
    });

    // Iframe gone, link-card fallback visible
    expect(document.querySelector("iframe")).toBeNull();
    const links = screen.getAllByRole("link");
    const fallback = links.find((l) => l.textContent?.includes("Open in Team Relay"));
    expect(fallback).toBeDefined();
  });
});

describe("MarkdownWithRelay — relay URL detection", () => {
  it("renders plain text unchanged (no relay URLs)", () => {
    const { container } = render(<MarkdownWithRelay content="Hello **world**" />);
    expect(container.querySelector("strong")).not.toBeNull();
  });

  it("renders a bare relay:// URL as a preview card", () => {
    render(
      <MarkdownWithRelay content={"Here is a doc:\nrelay://relay.host/shares/abc/docs/Spec.md"} />,
    );
    expect(screen.getByText("Spec")).toBeInTheDocument();
  });

  it("renders a markdown-link relay:// with the label", () => {
    render(
      <MarkdownWithRelay content="[Architecture Doc](relay://relay.host/shares/abc/docs/Arch.md)" />,
    );
    expect(screen.getByText("Architecture Doc")).toBeInTheDocument();
  });

  it("renders surrounding text alongside the preview card", () => {
    const { container } = render(
      <MarkdownWithRelay content={"Above text\nrelay://relay.host/shares/abc/docs/Doc.md\nBelow text"} />,
    );
    expect(container.textContent).toContain("Above text");
    expect(container.textContent).toContain("Below text");
    expect(screen.getByText("Doc")).toBeInTheDocument();
  });

  it("handles invalid relay:// gracefully (no crash)", () => {
    expect(() =>
      render(<MarkdownWithRelay content="relay://" />),
    ).not.toThrow();
  });

  it("handles plain text with no relay:// without rendering a card", () => {
    const { container } = render(<MarkdownWithRelay content="No relay here" />);
    expect(container.querySelector("iframe")).toBeNull();
  });
});
