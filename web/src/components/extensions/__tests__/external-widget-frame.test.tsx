/**
 * dashboard.widget, protocol v2: the contract between Mesh and an external widget
 * (docs/extensions/ui-iframe.md, "Widgets"), from the host's side. The widget's side is
 * the reference extension (examples/external-extension/refext, TestWidgetPageContract).
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
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

import { ApiRequestError } from "@/lib/api";
import { useWorkspaceStore } from "@/stores/workspace";
import { resetWidgetLoadQueueForTests, widgetLoadQueueState, MAX_CONCURRENT_WIDGET_LOADS } from "@/lib/widget-load-queue";
import { ExternalWidgetFrame, WIDGET_APP_READY_TIMEOUT_MS } from "../external-widget-frame";
import { REVOKED_DELIVERY_GRACE_MS } from "@/lib/extension-frame-protocol";

const ORIGIN = "https://hello.example.com";
const CTX = { token: "jwt.value.here", expires_at: "2099-01-01T00:00:00Z", frame_url: `${ORIGIN}/mesh/ui/widget`, origin: ORIGIN, title: "Reference widget" };

// IntersectionObserver stub: the test decides which hosts are in view.
const observers: { cb: IntersectionObserverCallback; el: Element }[] = [];
class FakeIO {
  constructor(private cb: IntersectionObserverCallback) {}
  observe(el: Element) {
    observers.push({ cb: this.cb, el });
  }
  disconnect() {}
  unobserve() {}
  takeRecords() {
    return [];
  }
}
function setInView(el: Element, isIntersecting: boolean) {
  const o = observers.filter((x) => x.el === el);
  act(() => o.forEach((x) => x.cb([{ isIntersecting, target: el } as IntersectionObserverEntry], {} as IntersectionObserver)));
}
const hosts = () => screen.queryAllByTestId("external-widget");
const frames = () => Array.from(document.querySelectorAll("iframe"));

function widget(over: Partial<React.ComponentProps<typeof ExternalWidgetFrame>> = {}) {
  return (
    <MemoryRouter>
      <ExternalWidgetFrame extensionId="hello" name="Hello" widgetType="ref" dashboardId="d-1" instanceId="i-1" settings={{ title: "A" }} {...over} />
    </MemoryRouter>
  );
}

function post(iframe: HTMLIFrameElement, data: unknown, origin = ORIGIN, source: Window | null = iframe.contentWindow) {
  act(() => {
    window.dispatchEvent(new MessageEvent("message", { origin, data, source }));
  });
}

/** Spy on what the host posts into the frame. */
function spyOn(iframe: HTMLIFrameElement) {
  const win = iframe.contentWindow as Window;
  const spy = vi.spyOn(win, "postMessage").mockImplementation(() => {});
  return () => spy.mock.calls.map((c) => c[0] as Record<string, unknown>);
}

beforeEach(() => {
  apiMock.mockReset();
  apiMock.mockResolvedValue(CTX);
  toastMock.error.mockReset();
  navigateMock.mockReset();
  observers.length = 0;
  resetWidgetLoadQueueForTests();
  document.documentElement.classList.remove("dark");
  document.documentElement.style.removeProperty("--background");
  vi.stubGlobal("IntersectionObserver", FakeIO);
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws-1", slug: "acme" } as never });
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

async function mountVisible(props: Partial<React.ComponentProps<typeof ExternalWidgetFrame>> = {}) {
  const r = render(widget(props));
  setInView(hosts()[0]!, true);
  const iframe = (await screen.findByTitle("Reference widget")) as HTMLIFrameElement;
  return { ...r, iframe };
}

describe("ExternalWidgetFrame (dashboard.widget)", () => {
  it("asks for a widget token naming the dashboard and the instance, and sandboxes the frame", async () => {
    const { iframe } = await mountVisible({ projectId: "p-1" });
    expect(apiMock).toHaveBeenCalledWith("/api/v1/ext/hello/ui-context", {
      method: "POST",
      body: { workspace_id: "ws-1", slot: "dashboard.widget", dashboard_id: "d-1", instance_id: "i-1", widget_id: "ref", project_id: "p-1" },
    });
    const sandbox = (iframe.getAttribute("sandbox") ?? "").split(/\s+/).filter(Boolean).sort();
    expect(sandbox).toEqual(
      ["allow-forms", "allow-popups", "allow-popups-to-escape-sandbox", "allow-same-origin", "allow-scripts"],
    );
    expect(sandbox.some((t) => t.includes("top-navigation"))).toBe(false);
    expect(sandbox).not.toContain("allow-modals");
    expect(iframe.getAttribute("allow")).toBe("clipboard-write");
    expect(iframe.getAttribute("allow")).not.toContain("clipboard-read");
    expect(iframe.getAttribute("src")).toBe(`${ORIGIN}/mesh/ui/widget?token=jwt.value.here`);
  });

  it("sends the theme, the instance settings and the slot in mesh.context", async () => {
    document.documentElement.classList.add("dark");
    document.documentElement.style.setProperty("--background", "222 14% 9%");
    const { iframe } = await mountVisible({ settings: { title: "A", limit: 5 }, size: { w: 3, h: 2 } });
    const sent = spyOn(iframe);
    act(() => void iframe.dispatchEvent(new Event("load")));
    const ctx = sent().find((m) => m.type === "mesh.context") as Record<string, any>;
    expect(ctx.version).toBe(2);
    expect(ctx.slot).toBe("dashboard.widget");
    expect(ctx.widget).toEqual({ type: "ref", instance_id: "i-1", dashboard_id: "d-1", settings: { title: "A", limit: 5 }, size: { w: 3, h: 2 } });
    expect(ctx.theme.mode).toBe("dark");
    expect(ctx.theme.tokens["--background"]).toBe("222 14% 9%");
    expect(ctx.token).toBe("jwt.value.here");
  });

  it("sends mesh.theme when the colour mode is switched, and mesh.widget.settings when the settings change", async () => {
    const { iframe, rerender } = await mountVisible();
    act(() => void iframe.dispatchEvent(new Event("load")));
    post(iframe, { type: "mesh.app-ready" });
    const sent = spyOn(iframe);
    await act(async () => {
      document.documentElement.classList.add("dark");
      await Promise.resolve();
    });
    await waitFor(() => expect(sent().some((m) => m.type === "mesh.theme" && m.mode === "dark")).toBe(true));
    rerender(widget({ settings: { title: "B" } }));
    await waitFor(() => expect(sent().find((m) => m.type === "mesh.widget.settings")?.settings).toEqual({ title: "B" }));
    expect(frames()).toHaveLength(1); // by message, never by reload
  });

  it("sends mesh.widget.refresh on a bump and mesh.widget.visibility when the widget leaves the view", async () => {
    const { iframe, rerender } = await mountVisible();
    act(() => void iframe.dispatchEvent(new Event("load")));
    post(iframe, { type: "mesh.app-ready" });
    const sent = spyOn(iframe);
    rerender(widget({ refreshNonce: 1 }));
    await waitFor(() => expect(sent().some((m) => m.type === "mesh.widget.refresh")).toBe(true));
    setInView(hosts()[0]!, false);
    await waitFor(() => expect(sent().filter((m) => m.type === "mesh.widget.visibility").slice(-1)[0]?.visible).toBe(false));
    expect(frames()).toHaveLength(1);
  });

  it("closes the widget with a stub on mesh.revoked (access gone at the next renewal)", async () => {
    const { iframe, rerender } = await mountVisible();
    act(() => void iframe.dispatchEvent(new Event("load")));
    // postMessage is delivered later, in the frame's own task: model that, and note whether the frame was still in the page then.
    const delivered: { type: unknown; inPage: boolean }[] = [];
    vi.spyOn(iframe.contentWindow as Window, "postMessage").mockImplementation((m: unknown) => {
      // Race: the frame's own message lands right after mesh.revoked, before React commits the revoked state.
      if ((m as { type?: unknown }).type === "mesh.revoked") {
        window.dispatchEvent(new MessageEvent("message", { origin: ORIGIN, data: { type: "mesh.open", kind: "task", id: "t-race" }, source: iframe.contentWindow }));
      }
      setTimeout(() => delivered.push({ type: (m as { type?: unknown }).type, inPage: iframe.isConnected }), 50);
    });
    apiMock.mockRejectedValueOnce(new ApiRequestError("forbidden", "FORBIDDEN", 403));
    post(iframe, { type: "mesh.ready" });
    await screen.findByTestId("external-widget-revoked");
    // The same frame (never remounted) stays, hidden and out of the tab order, until the message reaches it.
    expect(frames()).toEqual([iframe]);
    expect(iframe.className).toBe("hidden");
    expect(iframe.getAttribute("aria-hidden")).toBe("true");
    await waitFor(() => expect(delivered.find((d) => d.type === "mesh.revoked")).toEqual({ type: "mesh.revoked", inPage: true }));
    // A hidden revoked frame is no longer listened to.
    post(iframe, { type: "mesh.open", kind: "task", id: "t-1" });
    expect(navigateMock).not.toHaveBeenCalled();
    // ...and told nothing more: mesh.revoked is the last message, whatever changes on the host meanwhile.
    rerender(widget({ settings: { title: "changed after revoke" }, refreshNonce: 7 }));
    act(() => document.documentElement.classList.add("dark"));
    await new Promise((r) => setTimeout(r, 200));
    const types = delivered.map((d) => d.type);
    expect(types.slice(types.indexOf("mesh.revoked") + 1)).toEqual([]);
    expect(navigateMock).not.toHaveBeenCalled();
    await waitFor(() => expect(frames()).toHaveLength(0), { timeout: REVOKED_DELIVERY_GRACE_MS + 1_000 });
    expect(screen.getByTestId("external-widget-revoked")).toBeTruthy();
  });

  it("the frame really receives mesh.revoked before it is removed (409 at the regular renewal)", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { iframe } = await mountVisible();
    act(() => void iframe.dispatchEvent(new Event("load")));
    // postMessage only queues a task in the frame: model that, and drop it if the frame is gone by then.
    const received: string[] = [];
    vi.spyOn(iframe.contentWindow as Window, "postMessage").mockImplementation((msg: unknown) => {
      setTimeout(() => {
        if (iframe.isConnected) received.push((msg as { type: string }).type);
      }, 0);
    });
    apiMock.mockRejectedValueOnce(new ApiRequestError("conflict", "CONFLICT", 409));
    post(iframe, { type: "mesh.ready" });
    await screen.findByTestId("external-widget-revoked");
    // The stub is shown at once; the revoked frame stays hidden and out of reach until the messages land.
    expect(frames()).toEqual([iframe]);
    expect(iframe.className).toContain("hidden");
    expect(iframe.getAttribute("aria-hidden")).toBe("true");
    await act(async () => {
      vi.advanceTimersByTime(10);
    });
    expect(received).toEqual(["mesh.context.error", "mesh.revoked"]);
    // Nothing else goes to a revoked frame, and it is removed after the grace period.
    await act(async () => {
      vi.advanceTimersByTime(REVOKED_DELIVERY_GRACE_MS);
    });
    expect(frames()).toHaveLength(0);
    expect(received).toEqual(["mesh.context.error", "mesh.revoked"]);
    expect(screen.getByTestId("external-widget-revoked")).toBeInTheDocument();
  });

  it("a revocation after a renewed token keeps the frame on its first address (no reload that would lose mesh.revoked)", async () => {
    const { iframe } = await mountVisible();
    act(() => void iframe.dispatchEvent(new Event("load")));
    const firstSrc = iframe.getAttribute("src");
    vi.spyOn(iframe.contentWindow as Window, "postMessage").mockImplementation(() => {});
    // The live path: the first renewal succeeds with a new token, the next one is refused.
    apiMock.mockResolvedValueOnce({ ...CTX, token: "renewed.token.value" });
    post(iframe, { type: "mesh.ready" });
    await waitFor(() => expect(apiMock).toHaveBeenCalledTimes(2));
    await act(async () => {});
    apiMock.mockRejectedValueOnce(new ApiRequestError("conflict", "CONFLICT", 409));
    post(iframe, { type: "mesh.ready" });
    await screen.findByTestId("external-widget-revoked");
    expect(frames()).toEqual([iframe]);
    expect(iframe.getAttribute("src")).toBe(firstSrc);
  });

  it("mesh.open: opens the person's own task, refuses a foreign one with a toast", async () => {
    const { iframe } = await mountVisible();
    act(() => void iframe.dispatchEvent(new Event("load")));
    apiMock.mockImplementation((path: string) => {
      if (path === "/api/v1/tasks/own-task") return Promise.resolve({ id: "own-task" });
      if (path === "/api/v1/tasks/foreign-task") return Promise.reject(new ApiRequestError("not found", "NOT_FOUND", 404));
      return Promise.resolve(CTX);
    });
    post(iframe, { type: "mesh.open", kind: "task", id: "own-task" });
    await waitFor(() => expect(navigateMock).toHaveBeenCalledWith("/t/own-task"));
    navigateMock.mockClear();
    post(iframe, { type: "mesh.open", kind: "task", id: "foreign-task" });
    await waitFor(() => expect(toastMock.error).toHaveBeenCalledTimes(1));
    expect(navigateMock).not.toHaveBeenCalled();
    // The access check is Mesh's own call, not the widget's word: a shape that is no id never reaches it.
    apiMock.mockClear();
    post(iframe, { type: "mesh.open", kind: "task", id: "../../admin" });
    post(iframe, { type: "mesh.open", kind: "url", id: "own-task" });
    expect(apiMock).not.toHaveBeenCalled();
  });

  it("mesh.open of a document and of a project checks access first", async () => {
    const { iframe } = await mountVisible();
    act(() => void iframe.dispatchEvent(new Event("load")));
    apiMock.mockImplementation((path: string) => (path.startsWith("/api/v1/projects/") ? Promise.resolve({ slug: "alpha" }) : path.startsWith("/api/v1/documents/") ? Promise.resolve({}) : Promise.resolve(CTX)));
    post(iframe, { type: "mesh.open", kind: "doc", id: "doc-1" });
    await waitFor(() => expect(navigateMock).toHaveBeenCalledWith("/d/doc-1"));
    post(iframe, { type: "mesh.open", kind: "project", id: "proj-1" });
    await waitFor(() => expect(navigateMock).toHaveBeenCalledWith("/w/acme/p/alpha"));
  });

  it("ignores mesh.resize in this slot and asks the host to configure on mesh.widget.configure", async () => {
    const onConfigure = vi.fn();
    const { iframe } = await mountVisible({ onConfigure });
    act(() => void iframe.dispatchEvent(new Event("load")));
    const host = hosts()[0]!;
    const before = host.getAttribute("style");
    post(iframe, { type: "mesh.resize", height: 900 });
    expect(host.getAttribute("style")).toBe(before);
    expect(host.style.minHeight).not.toBe("900px");
    post(iframe, { type: "mesh.widget.configure" });
    expect(onConfigure).toHaveBeenCalledTimes(1);
  });

  it("drops messages from another origin or another window", async () => {
    const onConfigure = vi.fn();
    const { iframe } = await mountVisible({ onConfigure });
    act(() => void iframe.dispatchEvent(new Event("load")));
    post(iframe, { type: "mesh.widget.configure" }, "https://evil.example");
    post(iframe, { type: "mesh.widget.configure" }, ORIGIN, window);
    expect(onConfigure).not.toHaveBeenCalled();
  });

  it("shows an error with Retry when mesh.app-ready does not come in 15 s, and Retry starts it again", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    render(widget());
    setInView(hosts()[0]!, true);
    await screen.findByTitle("Reference widget");
    expect(screen.queryByTestId("external-widget-error")).toBeNull();
    await act(async () => {
      vi.advanceTimersByTime(WIDGET_APP_READY_TIMEOUT_MS - 3000);
    });
    expect(screen.queryByTestId("external-widget-error")).toBeNull();
    await act(async () => {
      vi.advanceTimersByTime(3500);
    });
    const err = await screen.findByTestId("external-widget-error");
    expect(err.textContent).toMatch(/15/);
    expect(frames()).toHaveLength(0);
    expect(widgetLoadQueueState().running).toBe(0); // the slot is given back
    const calls = apiMock.mock.calls.length;
    act(() => void screen.getByRole("button").click());
    await screen.findByTitle("Reference widget");
    expect(apiMock.mock.calls.length).toBe(calls + 1);
  });

  it("a context that arrives after the 15 s deadline does not bring the frame back", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let answer: (v: unknown) => void = () => {};
    apiMock.mockImplementationOnce(() => new Promise((r) => { answer = r; }));
    render(widget());
    setInView(hosts()[0]!, true);
    await act(async () => {
      vi.advanceTimersByTime(WIDGET_APP_READY_TIMEOUT_MS + 500);
    });
    expect(await screen.findByTestId("external-widget-error")).toBeInTheDocument();
    expect(widgetLoadQueueState().running).toBe(0);
    await act(async () => answer(CTX));
    expect(screen.getByTestId("external-widget-error")).toBeInTheDocument();
    expect(frames()).toHaveLength(0);
    expect(widgetLoadQueueState().running).toBe(0);
  });

  it("does not time out once the widget said mesh.app-ready", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    render(widget());
    setInView(hosts()[0]!, true);
    const iframe = (await screen.findByTitle("Reference widget")) as HTMLIFrameElement;
    act(() => void iframe.dispatchEvent(new Event("load")));
    post(iframe, { type: "mesh.app-ready" });
    await act(async () => {
      vi.advanceTimersByTime(WIDGET_APP_READY_TIMEOUT_MS * 2);
    });
    expect(screen.queryByTestId("external-widget-error")).toBeNull();
    expect(frames()).toHaveLength(1);
  });
});

describe("dashboard with 10 external widgets", () => {
  function dashboard(n: number) {
    return (
      <MemoryRouter>
        {Array.from({ length: n }, (_, i) => (
          <ExternalWidgetFrame key={i} extensionId="hello" name={`W${i}`} widgetType="ref" dashboardId="d-1" instanceId={`i-${i}`} />
        ))}
      </MemoryRouter>
    );
  }

  it("creates no frame for a widget that was never in view and no ui-context request for it", async () => {
    render(dashboard(10));
    expect(hosts()).toHaveLength(10);
    // Only the first four are in the viewport.
    hosts().slice(0, 4).forEach((h) => setInView(h, true));
    await waitFor(() => expect(frames()).toHaveLength(4));
    expect(apiMock).toHaveBeenCalledTimes(4);
    const asked = apiMock.mock.calls.map((c) => (c[1] as { body: { instance_id: string } }).body.instance_id).sort();
    expect(asked).toEqual(["i-0", "i-1", "i-2", "i-3"]);
  });

  it("loads at most 6 at a time and starts the next one as a load finishes", async () => {
    render(dashboard(10));
    hosts().forEach((h) => setInView(h, true));
    await waitFor(() => expect(frames()).toHaveLength(MAX_CONCURRENT_WIDGET_LOADS));
    expect(widgetLoadQueueState()).toEqual({ running: 6, waiting: 4 });
    // The 7th..10th are queued, not loading: no request, no frame.
    expect(apiMock).toHaveBeenCalledTimes(6);
    for (let done = 0; done < 4; done++) {
      const ready = frames()[done]!;
      // The frame's message listener is attached in an effect after the iframe is in the DOM; on a slow
      // runner a single early post lands before it and is lost, so the post repeats until the queue moves.
      await waitFor(() => {
        post(ready, { type: "mesh.app-ready" }, ORIGIN, ready.contentWindow);
        expect(frames().length).toBe(MAX_CONCURRENT_WIDGET_LOADS + done + 1);
      });
      expect(widgetLoadQueueState().running).toBeLessThanOrEqual(MAX_CONCURRENT_WIDGET_LOADS);
    }
    expect(frames()).toHaveLength(10);
    expect(widgetLoadQueueState()).toEqual({ running: 6, waiting: 0 }); // 4 settled; 6 still starting
  });

  it("gives its slot back when a widget is removed", async () => {
    const r = render(dashboard(7));
    hosts().forEach((h) => setInView(h, true));
    await waitFor(() => expect(frames()).toHaveLength(6));
    r.rerender(dashboard(6));
    await waitFor(() => expect(widgetLoadQueueState()).toEqual({ running: 6, waiting: 0 }));
  });
});
