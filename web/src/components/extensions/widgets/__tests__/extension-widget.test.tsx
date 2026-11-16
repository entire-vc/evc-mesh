/**
 * Dashboard widgets of extensions: an instance draws while its extension is
 * enabled, consented and still offers the widget; otherwise it keeps its place
 * as a stub with "Remove widget", and draws again once the extension is back.
 */
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

const apiMock = vi.fn();
vi.mock("@/lib/api", () => ({ api: (...a: unknown[]) => apiMock(...a) }));

import {
  invalidateExtensionWidgets,
  parseWidgetType,
  resetExtensionWidgetsForTests,
  resolveExtensionWidget,
  type ManifestWidget,
} from "@/lib/extension-widgets";
import { invalidateSlotRegistry } from "@/lib/extension-slots";
import { useWorkspaceStore } from "@/stores/workspace";
import { ExtensionWidget } from "../extension-widget";

const TYPE = "gitlab.failed-pipelines";
const WIDGET: ManifestWidget = {
  id: "failed-pipelines",
  title: "Failed pipelines",
  scope: "project",
  render: { builtin: "GitlabFailedPipelinesWidget" },
};

function gitlab(opts: { enabled: boolean; source?: string; consented?: string[]; widgets?: ManifestWidget[]; slot?: boolean }) {
  return {
    id: "gitlab",
    name: "GitLab",
    manifest: {
      capabilities: { ui: opts.slot === false ? ["task.panel"] : ["task.panel", "dashboard.widget"] },
      widgets: opts.widgets ?? [WIDGET],
    },
    status: { enabled: opts.enabled, source: opts.source ?? "instance", consented_capabilities: opts.consented },
  };
}

const PIPELINE = {
  link_id: "l1",
  url: "https://git.example/g/p/-/merge_requests/7",
  title: "Fix login",
  external_id: "7",
  pipeline_updated_at: "2026-11-13T10:00:00Z",
  task_id: "t1",
  task_title: "Login is broken",
  project_id: "p1",
  project_name: "Mesh",
};

// The widget is a lazy chunk (`BUILTIN_WIDGETS`). On a loaded CI runner its
// first transform-and-import alone outlasted findBy's 10 s, so the first test
// to draw it sat on `extension-widget-pending`. Import it up front: the lazy
// import then resolves from the module cache, and the tests time what they
// mean to time — the widget's own states, not the module loader. The hook gets
// its own generous timeout for the same reason.
beforeAll(async () => {
  await import("../gitlab-failed-pipelines");
}, 60_000);

let extensions: unknown[] = [];
let pipelines: unknown[] = [];

beforeEach(() => {
  apiMock.mockReset();
  apiMock.mockImplementation((path: string) => {
    if (path.endsWith("/extensions")) return Promise.resolve({ extensions });
    if (path.includes("/widgets/gitlab/failed-pipelines")) return Promise.resolve({ since: "", items: pipelines });
    return Promise.resolve({});
  });
  resetExtensionWidgetsForTests();
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws-1" } as never });
  extensions = [];
  pipelines = [];
});

function show(onRemove?: () => void, settings?: Record<string, unknown>) {
  return render(
    <MemoryRouter>
      <ExtensionWidget type={TYPE} settings={settings} onRemove={onRemove} />
    </MemoryRouter>,
  );
}

describe("parseWidgetType", () => {
  it("splits ext.widget and leaves core ids alone", () => {
    expect(parseWidgetType("gitlab.failed-pipelines")).toEqual({ extensionId: "gitlab", widgetId: "failed-pipelines" });
    expect(parseWidgetType("my-tasks")).toBeNull();
    expect(parseWidgetType(".x")).toBeNull();
    expect(parseWidgetType("x.")).toBeNull();
  });
});

describe("resolveExtensionWidget", () => {
  it("follows enabled, slot, widget and workspace consent", () => {
    expect(resolveExtensionWidget(TYPE, null).state).toBe("loading");
    expect(resolveExtensionWidget(TYPE, []).state).toBe("removed");
    expect(resolveExtensionWidget(TYPE, [gitlab({ enabled: true })]).state).toBe("ready");
    expect(resolveExtensionWidget(TYPE, [gitlab({ enabled: false })]).state).toBe("disabled");
    expect(resolveExtensionWidget(TYPE, [gitlab({ enabled: true, slot: false })]).state).toBe("removed");
    expect(resolveExtensionWidget(TYPE, [gitlab({ enabled: true, widgets: [] })]).state).toBe("removed");
    // A workspace connection consented before the slot existed waits for re-consent.
    expect(resolveExtensionWidget(TYPE, [gitlab({ enabled: true, source: "workspace", consented: ["ui:task.panel"] })]).state).toBe("consent");
    expect(
      resolveExtensionWidget(TYPE, [gitlab({ enabled: true, source: "workspace", consented: ["ui:task.panel", "ui:dashboard.widget"] })]).state,
    ).toBe("ready");
  });
});

describe("ExtensionWidget", () => {
  it("draws the GitLab widget while the extension is enabled", async () => {
    extensions = [gitlab({ enabled: true })];
    pipelines = [PIPELINE];
    show(undefined, { project_id: "p1" });
    expect(await screen.findByTestId("gitlab-failed-pipelines")).toBeInTheDocument();
    expect(screen.getByText("Login is broken").closest("a")).toHaveAttribute("href", "/t/t1");
    expect(screen.getByText(/Fix login/).closest("a")).toHaveAttribute("href", PIPELINE.url);
    expect(apiMock).toHaveBeenCalledWith("/api/v1/workspaces/ws-1/widgets/gitlab/failed-pipelines?project_id=p1");
    expect(screen.queryByTestId("extension-widget-stub")).toBeNull();
  });

  it("shows the empty state when nothing failed", async () => {
    extensions = [gitlab({ enabled: true })];
    show();
    expect(await screen.findByTestId("gitlab-failed-pipelines-empty")).toBeInTheDocument();
  });

  it("disabled → stub with Remove, re-enabled → draws again", async () => {
    extensions = [gitlab({ enabled: false })];
    const onRemove = vi.fn();
    show(onRemove);
    const stub = await screen.findByTestId("extension-widget-stub");
    expect(stub).toHaveTextContent("GitLab");
    expect(screen.queryByTestId("gitlab-failed-pipelines-empty")).toBeNull();
    fireEvent.click(screen.getByTestId("extension-widget-remove"));
    expect(onRemove).toHaveBeenCalledTimes(1);

    // The extensions UI invalidates the slot registry after enable; the widget follows it.
    extensions = [gitlab({ enabled: true })];
    await act(async () => invalidateSlotRegistry("ws-1"));
    expect(await screen.findByTestId("gitlab-failed-pipelines-empty")).toBeInTheDocument();
    expect(screen.queryByTestId("extension-widget-stub")).toBeNull();
  });

  it("an invalidation during an in-flight request refetches instead of caching the older answer", async () => {
    let release: (v: unknown) => void = () => {};
    let first = true;
    apiMock.mockImplementation((path: string) => {
      if (path.endsWith("/extensions")) {
        if (first) {
          first = false;
          // The answer computed before the administrator re-enabled the extension.
          return new Promise((r) => { release = r; });
        }
        return Promise.resolve({ extensions: [gitlab({ enabled: true })] });
      }
      if (path.includes("/widgets/gitlab/failed-pipelines")) return Promise.resolve({ since: "", items: [] });
      return Promise.resolve({});
    });
    show(() => {});
    expect(await screen.findByTestId("extension-widget-pending")).toBeInTheDocument();
    await act(async () => invalidateExtensionWidgets("ws-1"));
    await act(async () => release({ extensions: [gitlab({ enabled: false })] }));
    expect(await screen.findByTestId("gitlab-failed-pipelines-empty")).toBeInTheDocument();
    expect(apiMock.mock.calls.filter(([p]) => String(p).endsWith("/extensions"))).toHaveLength(2);
  });

  it("an extension that is gone leaves a stub; without onRemove there is no button", async () => {
    extensions = [];
    show();
    expect(await screen.findByTestId("extension-widget-stub")).toBeInTheDocument();
    expect(screen.queryByTestId("extension-widget-remove")).toBeNull();
  });

  it("waiting for consent is a stub, not the widget", async () => {
    extensions = [gitlab({ enabled: true, source: "workspace", consented: ["ui:task.panel"] })];
    show(() => {});
    expect(await screen.findByTestId("extension-widget-stub")).toBeInTheDocument();
    expect(apiMock.mock.calls.some(([p]) => String(p).includes("/widgets/gitlab/"))).toBe(false);
  });

  it("an external widget with render.frame draws the iframe host and asks ui-context for its widget id", async () => {
    const frameWidget: ManifestWidget = { id: "status", title: "Status", scope: "workspace", render: { frame: { path: "/mesh/ui/widget" } } };
    extensions = [{
      id: "reference-ext",
      name: "Reference",
      manifest: { capabilities: { ui: ["dashboard.widget"] }, widgets: [frameWidget] },
      status: { enabled: true, source: "workspace", consented_capabilities: ["ui:dashboard.widget"] },
    }];
    const ctx = { token: "t", expires_at: new Date(Date.now() + 60_000).toISOString(), frame_url: "https://ref.example/mesh/ui/widget", origin: "https://ref.example", title: "Status" };
    apiMock.mockImplementation((path: string) => {
      if (path.endsWith("/extensions")) return Promise.resolve({ extensions });
      if (path.endsWith("/ui-context")) return Promise.resolve(ctx);
      return Promise.resolve({});
    });
    const { unmount } = render(
      <MemoryRouter>
        <ExtensionWidget type="reference-ext.status" dashboardId="d-1" instanceId="i-1" settings={{ label: "A" }} />
      </MemoryRouter>,
    );
    expect(await screen.findByTestId("external-widget")).toBeInTheDocument();
    expect(screen.queryByTestId("extension-widget-stub")).toBeNull();
    await vi.waitFor(() =>
      expect(apiMock).toHaveBeenCalledWith("/api/v1/ext/reference-ext/ui-context", {
        method: "POST",
        body: { workspace_id: "ws-1", slot: "dashboard.widget", dashboard_id: "d-1", instance_id: "i-1", widget_id: "status" },
      }),
    );
    unmount();

    // Without a saved instance there is nothing to sign: the stub, and no token request.
    apiMock.mockClear();
    render(
      <MemoryRouter>
        <ExtensionWidget type="reference-ext.status" />
      </MemoryRouter>,
    );
    expect(await screen.findByTestId("extension-widget-stub")).toBeInTheDocument();
    expect(apiMock.mock.calls.some(([p]) => String(p).includes("/ui-context"))).toBe(false);
  });
});
