/**
 * Iframe screens of external extensions (W3.3): the frame gets a context token
 * from the server, is sandboxed, and obeys messages only from its own origin.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

const apiMock = vi.fn();
vi.mock("@/lib/api", () => ({ api: (...a: unknown[]) => apiMock(...a) }));
const toastMock = vi.hoisted(() => ({ message: vi.fn(), success: vi.fn(), error: vi.fn(), warning: vi.fn() }));
vi.mock("@/components/ui/toast", () => ({ toast: toastMock, Toaster: () => null }));
const navigateMock = vi.fn();
vi.mock("react-router", async (orig) => ({ ...(await orig<typeof import("react-router")>()), useNavigate: () => navigateMock }));

import { resetSlotRegistryForTests } from "@/lib/extension-slots";
import { useWorkspaceStore } from "@/stores/workspace";
import { ExternalFrame, FRAME_LOAD_TIMEOUT_MS } from "../external-frame";

const ORIGIN = "https://hello.example.com";
const CTX = { token: "jwt.value.here", expires_at: "2026-10-17T12:01:00Z", frame_url: `${ORIGIN}/mesh/ui/hello`, origin: ORIGIN, title: "Hello" };

function renderFrame(layout: "screen" | "panel" = "screen") {
  return render(
    <MemoryRouter>
      <ExternalFrame extensionId="hello" name="Hello" slot="sidebar.item" layout={layout} />
    </MemoryRouter>,
  );
}

function post(iframe: HTMLIFrameElement, origin: string, data: unknown, source: Window | null = iframe.contentWindow) {
  act(() => {
    window.dispatchEvent(new MessageEvent("message", { origin, data, source }));
  });
}

/**
 * The host listener is attached in a passive effect that can land just after the iframe appears.
 * A listener that answers `mesh.ready` (a second ui-context request) is provably live, so messages
 * posted afterwards cannot be lost, and a "nothing happened" assertion cannot pass vacuously.
 */
async function untilListening(iframe: HTMLIFrameElement) {
  const before = apiMock.mock.calls.length;
  await waitFor(
    () => {
      post(iframe, ORIGIN, { type: "mesh.ready" });
      expect(apiMock.mock.calls.length).toBeGreaterThan(before);
    },
    { timeout: 8000 },
  );
}

beforeEach(() => {
  apiMock.mockReset();
  toastMock.message.mockReset();
  toastMock.success.mockReset();
  navigateMock.mockReset();
  resetSlotRegistryForTests();
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws-1", slug: "acme" } as never });
  apiMock.mockResolvedValue(CTX);
});
afterEach(() => vi.useRealTimers());

describe("ExternalFrame", () => {
  it("asks for a context token and renders a sandboxed iframe on the service origin", async () => {
    renderFrame();
    const iframe = (await screen.findByTitle("Hello", {}, { timeout: 8000 })) as HTMLIFrameElement;
    expect(apiMock).toHaveBeenCalledWith("/api/v1/ext/hello/ui-context", {
      method: "POST",
      body: { workspace_id: "ws-1", slot: "sidebar.item" },
    });
    expect(iframe.getAttribute("src")).toBe(`${ORIGIN}/mesh/ui/hello?token=jwt.value.here`);
    expect(iframe.getAttribute("sandbox")).toBe("allow-scripts allow-forms allow-same-origin");
    expect(iframe.getAttribute("referrerpolicy")).toBe("no-referrer");
  });

  it("shows mesh.toast and follows mesh.navigate from the frame's origin", async () => {
    renderFrame();
    const iframe = (await screen.findByTitle("Hello", {}, { timeout: 8000 })) as HTMLIFrameElement;
    await untilListening(iframe);
    post(iframe, ORIGIN, { type: "mesh.toast", level: "success", message: "Привет" });
    expect(toastMock.success).toHaveBeenCalledWith("Привет");
    post(iframe, ORIGIN, { type: "mesh.navigate", path: "/w/acme/dashboard" });
    expect(navigateMock).toHaveBeenCalledWith("/w/acme/dashboard");
  });

  it("red control: a foreign origin cannot toast or navigate", async () => {
    renderFrame();
    const iframe = (await screen.findByTitle("Hello", {}, { timeout: 8000 })) as HTMLIFrameElement;
    await untilListening(iframe);
    post(iframe, "https://evil.example", { type: "mesh.navigate", path: "/w/acme/dashboard" });
    post(iframe, "https://evil.example", { type: "mesh.toast", message: "pwned" });
    post(iframe, ORIGIN, { type: "mesh.navigate", path: "/w/acme/dashboard" }, window);
    post(iframe, ORIGIN, { type: "mesh.navigate", path: "https://evil.example" });
    expect(navigateMock).not.toHaveBeenCalled();
    expect(toastMock.message).not.toHaveBeenCalled();
  });

  it("hands over a fresh token on mesh.ready, addressed to the service origin only", async () => {
    renderFrame();
    const iframe = (await screen.findByTitle("Hello", {}, { timeout: 8000 })) as HTMLIFrameElement;
    const spy = vi.spyOn(iframe.contentWindow as Window, "postMessage");
    apiMock.mockResolvedValue({ ...CTX, token: "fresh.token.value" });
    // The listener is attached in a passive effect that may land just after the iframe appears:
    // re-announce readiness until the host answers (a real screen only announces after it has loaded).
    await waitFor(
      () => {
        post(iframe, ORIGIN, { type: "mesh.ready" });
        expect(spy).toHaveBeenCalled();
      },
      { timeout: 8000 },
    );
    const [payload, target] = spy.mock.calls[0] as [Record<string, unknown>, string];
    expect(target).toBe(ORIGIN);
    expect(payload).toMatchObject({ type: "mesh.context", token: "fresh.token.value", workspace_id: "ws-1", workspace_slug: "acme" });
  });

  it("shows unavailable with retry when the server refuses a token", async () => {
    apiMock.mockRejectedValueOnce(new Error("409"));
    renderFrame();
    expect(await screen.findByTestId("external-frame-unavailable", {}, { timeout: 8000 })).toBeTruthy();
    apiMock.mockResolvedValue(CTX);
    fireEvent.click(screen.getByRole("button"));
    expect(await screen.findByTitle("Hello", {}, { timeout: 8000 })).toBeTruthy();
  });

  it("shows unavailable when the screen never loads", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    renderFrame();
    await screen.findByTitle("Hello", {}, { timeout: 8000 });
    // The timeout is armed in an effect after the frame mounts; keep advancing until it has fired.
    for (let i = 0; i < 50 && !screen.queryByTestId("external-frame-unavailable"); i++) {
      await act(async () => {
        vi.advanceTimersByTime(FRAME_LOAD_TIMEOUT_MS + 1);
        await Promise.resolve();
      });
    }
    expect(screen.getByTestId("external-frame-unavailable")).toBeTruthy();
  });
});
