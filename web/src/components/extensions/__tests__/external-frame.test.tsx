/**
 * Iframe screens of external extensions: the frame gets a context token
 * from the server, is sandboxed, and obeys messages only from its own origin.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

const apiMock = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ApiRequestError: (await orig<typeof import("@/lib/api")>()).ApiRequestError,
  api: (...a: unknown[]) => apiMock(...a),
}));
const toastMock = vi.hoisted(() => ({ message: vi.fn(), success: vi.fn(), error: vi.fn(), warning: vi.fn() }));
vi.mock("@/components/ui/toast", () => ({ toast: toastMock, Toaster: () => null }));
const navigateMock = vi.fn();
vi.mock("react-router", async (orig) => ({ ...(await orig<typeof import("react-router")>()), useNavigate: () => navigateMock }));

import { resetSlotRegistryForTests } from "@/lib/extension-slots";
import { useWorkspaceStore } from "@/stores/workspace";
import { ApiRequestError } from "@/lib/api";
import { ExternalFrame, FRAME_LOAD_TIMEOUT_MS, REFRESH_RETRY_MS } from "../external-frame";
import { REVOKED_DELIVERY_GRACE_MS } from "@/lib/extension-frame-protocol";

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
  document.documentElement.classList.remove("dark");
  document.documentElement.style.removeProperty("--background");
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

  it("sends the Mesh theme in mesh.context of a screen slot, keeping protocol v1", async () => {
    document.documentElement.classList.add("dark");
    document.documentElement.style.setProperty("--background", "222 14% 9%");
    renderFrame();
    const iframe = (await screen.findByTitle("Hello", {}, { timeout: 8000 })) as HTMLIFrameElement;
    const spy = vi.spyOn(iframe.contentWindow as Window, "postMessage").mockImplementation(() => {});
    fireEvent.load(iframe);
    const ctx = spy.mock.calls.map((c) => c[0] as Record<string, any>).find((m) => m.type === "mesh.context");
    expect(ctx).toBeDefined();
    expect(ctx!.version).toBe(1);
    expect(ctx!.slot).toBeUndefined();
    expect(ctx!.theme.mode).toBe("dark");
    expect(ctx!.theme.tokens["--background"]).toBe("222 14% 9%");
    expect(iframe.style.colorScheme).toBe("dark");
  });

  it("sends mesh.theme to a loaded screen when the colour mode is switched, once per change", async () => {
    renderFrame();
    const iframe = (await screen.findByTitle("Hello", {}, { timeout: 8000 })) as HTMLIFrameElement;
    const spy = vi.spyOn(iframe.contentWindow as Window, "postMessage").mockImplementation(() => {});
    const sent = () => spy.mock.calls.map((c) => c as [Record<string, any>, string]);
    fireEvent.load(iframe);
    // Right after mesh.context the same theme is not repeated.
    await new Promise((r) => setTimeout(r, 50));
    expect(sent().some(([m]) => m.type === "mesh.theme")).toBe(false);
    expect(sent().find(([m]) => m.type === "mesh.context")?.[0].theme.mode).toBe("light");
    await act(async () => {
      document.documentElement.classList.add("dark");
      await Promise.resolve();
    });
    await waitFor(() => expect(sent().some(([m]) => m.type === "mesh.theme" && m.mode === "dark")).toBe(true));
    const themes = sent().filter(([m]) => m.type === "mesh.theme");
    expect(themes).toHaveLength(1);
    expect(themes[0]![1]).toBe(ORIGIN);
    expect(themes[0]![0]).toMatchObject({ type: "mesh.theme", version: 2, mode: "dark" });
    expect(iframe.style.colorScheme).toBe("dark");
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

  it("opens at the section, follows mesh.state, and tells the frame when the address moves", async () => {
    const onSection = vi.fn();
    const view = (section: string) => (
      <MemoryRouter>
        <ExternalFrame extensionId="hello" name="Hello" slot="sidebar.item" layout="screen" section={section} onSection={onSection} />
      </MemoryRouter>
    );
    const { rerender } = render(view("/memory#recent"));
    const iframe = (await screen.findByTitle("Hello", {}, { timeout: 8000 })) as HTMLIFrameElement;
    const src = `${ORIGIN}/mesh/ui/hello/memory?token=jwt.value.here#recent`;
    expect(iframe.getAttribute("src")).toBe(src);
    fireEvent.load(iframe);
    await untilListening(iframe);

    post(iframe, ORIGIN, { type: "mesh.state", path: "/models" });
    expect(onSection).toHaveBeenCalledWith("/models", false);
    // The page echoes it into the address: nothing goes back to the frame, nothing reloads.
    const spy = vi.spyOn(iframe.contentWindow as Window, "postMessage");
    rerender(view("/models"));
    expect(spy).not.toHaveBeenCalled();
    post(iframe, ORIGIN, { type: "mesh.state", path: "/models" });
    expect(onSection).toHaveBeenCalledTimes(1);

    // Back to the earlier section: the frame is told by message, the src stays.
    rerender(view("/memory#recent"));
    await waitFor(() => expect(spy).toHaveBeenCalled());
    const [payload, target] = spy.mock.calls[0] as [Record<string, unknown>, string];
    expect(target).toBe(ORIGIN);
    expect(payload).toMatchObject({ type: "mesh.context", path: "/memory#recent" });
    expect(iframe.getAttribute("src")).toBe(src);

    // Red control: a section that climbs out, or comes from elsewhere, is dropped.
    post(iframe, ORIGIN, { type: "mesh.state", path: "/../admin" });
    post(iframe, "https://evil.example", { type: "mesh.state", path: "/x" });
    expect(onSection).toHaveBeenCalledTimes(1);
  });

  it("asks a project.view token for its project", async () => {
    render(
      <MemoryRouter>
        <ExternalFrame extensionId="hello" name="Hello" slot="project.view" projectId="proj-1" layout="screen" />
      </MemoryRouter>,
    );
    await screen.findByTitle("Hello", {}, { timeout: 8000 });
    expect(apiMock).toHaveBeenCalledWith("/api/v1/ext/hello/ui-context", {
      method: "POST",
      body: { workspace_id: "ws-1", slot: "project.view", project_id: "proj-1" },
    });
  });

  it("tells the frame a refused refresh and revokes it when access is gone", async () => {
    renderFrame();
    const iframe = (await screen.findByTitle("Hello", {}, { timeout: 8000 })) as HTMLIFrameElement;
    fireEvent.load(iframe);
    // postMessage is delivered later, in the frame's own task: model that, and note whether the frame was still in the page then.
    const delivered: { type: unknown; inPage: boolean }[] = [];
    const spy = vi.spyOn(iframe.contentWindow as Window, "postMessage").mockImplementation((m: unknown) => {
      // Race: the frame's own message lands right after mesh.revoked, before React commits the revoked state.
      if ((m as { type?: unknown }).type === "mesh.revoked") {
        window.dispatchEvent(new MessageEvent("message", { origin: ORIGIN, data: { type: "mesh.navigate", path: "/w/acme/race" }, source: iframe.contentWindow }));
      }
      setTimeout(() => delivered.push({ type: (m as { type?: unknown }).type, inPage: iframe.isConnected }), 50);
    });
    apiMock.mockRejectedValue(new ApiRequestError("not enabled", "CONFLICT", 409));
    await waitFor(
      () => {
        if (!screen.queryByTestId("external-frame-revoked")) post(iframe, ORIGIN, { type: "mesh.ready" });
        expect(screen.getByTestId("external-frame-revoked")).toBeTruthy();
      },
      { timeout: 8000 },
    );
    const sent = spy.mock.calls.map((c) => c as [Record<string, unknown>, string]);
    expect(sent.every(([, target]) => target === ORIGIN)).toBe(true);
    expect(sent.map(([m]) => m)).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ type: "mesh.context.error", status: 409, reason: "conflict" }),
        expect.objectContaining({ type: "mesh.revoked", reason: "conflict" }),
      ]),
    );
    // The same frame stays, hidden, until the message reaches it; then it leaves the page.
    expect(screen.getByTitle("Hello")).toBe(iframe);
    expect(iframe.className).toBe("hidden");
    await waitFor(() => expect(delivered.find((d) => d.type === "mesh.revoked")).toEqual({ type: "mesh.revoked", inPage: true }));
    // mesh.revoked is the last word: a theme switch while the frame drains is not told to it.
    act(() => document.documentElement.classList.add("dark"));
    await new Promise((r) => setTimeout(r, 200));
    const types = spy.mock.calls.map((c) => (c[0] as { type: unknown }).type);
    expect(types.slice(types.indexOf("mesh.revoked") + 1)).toEqual([]);
    await waitFor(() => expect(screen.queryByTitle("Hello")).toBeNull(), { timeout: REVOKED_DELIVERY_GRACE_MS + 1_000 });
    expect(screen.getByTestId("external-frame-revoked")).toBeTruthy();
    expect(navigateMock).not.toHaveBeenCalled();
  });

  it("red control: a transient refusal (503) is told to the frame but does not revoke it", async () => {
    renderFrame();
    const iframe = (await screen.findByTitle("Hello", {}, { timeout: 8000 })) as HTMLIFrameElement;
    fireEvent.load(iframe);
    const spy = vi.spyOn(iframe.contentWindow as Window, "postMessage");
    apiMock.mockRejectedValue(new ApiRequestError("down", "SERVICE_UNAVAILABLE", 503));
    await waitFor(
      () => {
        post(iframe, ORIGIN, { type: "mesh.ready" });
        expect(spy.mock.calls.some(([m]) => (m as { type: string }).type === "mesh.context.error")).toBe(true);
      },
      { timeout: 8000 },
    );
    expect(spy.mock.calls.some(([m]) => (m as { type: string }).type === "mesh.revoked")).toBe(false);
    expect(screen.queryByTestId("external-frame-revoked")).toBeNull();
    expect(screen.getByTitle("Hello")).toBeTruthy();
  });

  it("renews the token on its own before it expires", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const exp = () => new Date(Date.now() + 60_000).toISOString();
    apiMock.mockImplementation(() => Promise.resolve({ ...CTX, expires_at: exp() }));
    renderFrame();
    const iframe = (await screen.findByTitle("Hello", {}, { timeout: 8000 })) as HTMLIFrameElement;
    fireEvent.load(iframe);
    const spy = vi.spyOn(iframe.contentWindow as Window, "postMessage");
    const calls = apiMock.mock.calls.length;
    apiMock.mockImplementation(() => Promise.resolve({ ...CTX, token: "renewed.token", expires_at: exp() }));
    // Not yet: well inside the lifetime nothing is renewed.
    await act(async () => {
      vi.advanceTimersByTime(30_000);
      await Promise.resolve();
    });
    expect(apiMock.mock.calls.length).toBe(calls);
    for (let i = 0; i < 50 && !spy.mock.calls.some(([m]) => (m as { token?: string }).token === "renewed.token"); i++) {
      await act(async () => {
        vi.advanceTimersByTime(1_000);
        await Promise.resolve();
      });
    }
    const renewed = spy.mock.calls.find(([m]) => (m as { token?: string }).token === "renewed.token");
    expect(renewed?.[1]).toBe(ORIGIN);
    // Renewed before the 60 s token ran out.
    expect(apiMock.mock.calls.length).toBe(calls + 1);
  });

  it("retries a failed renewal after REFRESH_RETRY_MS and hands over the new token", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const exp = () => new Date(Date.now() + 60_000).toISOString();
    apiMock.mockImplementation(() => Promise.resolve({ ...CTX, expires_at: exp() }));
    renderFrame();
    const iframe = (await screen.findByTitle("Hello", {}, { timeout: 8000 })) as HTMLIFrameElement;
    fireEvent.load(iframe);
    const spy = vi.spyOn(iframe.contentWindow as Window, "postMessage");
    const sent = (type: string) => spy.mock.calls.some(([m]) => (m as { type: string }).type === type);
    const tick = (ms: number) =>
      act(async () => {
        vi.advanceTimersByTime(ms);
        await Promise.resolve();
      });
    apiMock.mockImplementation(() => Promise.reject(new ApiRequestError("down", "SERVICE_UNAVAILABLE", 503)));
    // The proactive renewal fails once: the frame is told, nothing is revoked.
    for (let i = 0; i < 60 && !sent("mesh.context.error"); i++) await tick(1_000);
    expect(sent("mesh.context.error")).toBe(true);
    const afterFailure = apiMock.mock.calls.length;
    apiMock.mockImplementation(() => Promise.resolve({ ...CTX, token: "retried.token", expires_at: exp() }));
    // Not before the retry delay...
    await tick(REFRESH_RETRY_MS - 2_000);
    expect(apiMock.mock.calls.length).toBe(afterFailure);
    // ...but once it has passed, the renewal runs again and the new token reaches the frame.
    for (let i = 0; i < 5 && !spy.mock.calls.some(([m]) => (m as { token?: string }).token === "retried.token"); i++) await tick(1_000);
    expect(apiMock.mock.calls.length).toBe(afterFailure + 1);
    expect(spy.mock.calls.find(([m]) => (m as { token?: string }).token === "retried.token")?.[1]).toBe(ORIGIN);
    expect(sent("mesh.revoked")).toBe(false);
  });

  it("shows the screen's own error on mesh.app-error and marks mesh.app-ready", async () => {
    renderFrame();
    const iframe = (await screen.findByTitle("Hello", {}, { timeout: 8000 })) as HTMLIFrameElement;
    fireEvent.load(iframe);
    await untilListening(iframe);
    post(iframe, ORIGIN, { type: "mesh.app-ready" });
    expect(screen.getByTestId("external-frame").getAttribute("data-app-ready")).toBe("true");
    post(iframe, "https://evil.example", { type: "mesh.app-error", message: "pwned" });
    expect(screen.queryByTestId("external-frame-app-error")).toBeNull();
    post(iframe, ORIGIN, { type: "mesh.app-error", message: "<b>Backend down</b>" });
    expect(screen.getByTestId("external-frame-app-error")).toBeTruthy();
    expect(screen.getByTestId("external-frame-app-error-message").textContent).toBe("<b>Backend down</b>");
    fireEvent.click(screen.getByRole("button"));
    expect(await screen.findByTitle("Hello", {}, { timeout: 8000 })).toBeTruthy();
  });
});
