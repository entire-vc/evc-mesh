import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import type { CatalogState } from "@/hooks/useExtensionCatalog";
import { mergeCatalog, type ExtensionStatusDto } from "@/hooks/useExtensionCatalog";
import { consentTokens } from "@/lib/extension-capabilities";

const state = vi.hoisted(() => ({ value: null as unknown as CatalogState }));
vi.mock("@/hooks/useExtensionCatalog", async (orig) => ({
  ...(await orig<typeof import("@/hooks/useExtensionCatalog")>()),
  useExtensionCatalog: () => state.value,
}));
const api = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), api }));
const overview = vi.hoisted(() => vi.fn());
vi.mock("@/lib/extension-instance", async (orig) => ({
  ...(await orig<typeof import("@/lib/extension-instance")>()),
  getInstanceOverview: overview,
}));

import { setLanguage } from "@/i18n";
import { ExtensionConnectPage } from "@/pages/extension-connect";
import { useWorkspaceStore } from "@/stores/workspace";
import { useMemberStore } from "@/stores/member";

import type { FieldSchema } from "@/components/extensions/schema-form";

const workspaceSchema: FieldSchema = { type: "object", required: ["base_url"], properties: { base_url: { type: "string", title: "Base URL" } } };
const manifest = {
  capabilities: { api: ["tasks:read"], events: ["task.updated"], ui: [{ slot: "task_panel" }], secrets: ["token"] },
  levels: { workspace: true, project: true },
  settings: { workspace: workspaceSchema },
};
const sum = (id: string, over: Record<string, unknown> = {}) => ({
  id, name: id, version: "1.0.0", kind: "integration", runtime: "builtin", publisher: "Entire VC",
  description: "", category: "development", source: "builtin", trust_level: "builtin",
  blocked: false, blocked_reason: "", manifest, ...over,
});
const ws = (id: string, status: ExtensionStatusDto = { enabled: false, source: "none" }) =>
  ({ id, name: id, version: "1.0.0", kind: "integration", category: "development", connection: null, status });

function mount(entries: CatalogState["entries"]) {
  state.value = { state: "ready", catalogUnavailable: false, reload: vi.fn(), refresh: vi.fn(), entries };
  return render(
    <MemoryRouter initialEntries={["/w/acme/extensions/gitlab/connect"]}>
      <Routes><Route path="w/:wsSlug/extensions/:extId/connect" element={<ExtensionConnectPage />} /></Routes>
    </MemoryRouter>,
  );
}

const next = () => screen.getByRole("button", { name: "Next" });

beforeEach(async () => {
  api.mockReset();
  overview.mockReset();
  overview.mockRejectedValue(Object.assign(new Error("forbidden"), { status: 403 }));
  await setLanguage("en");
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws-1" } } as never);
  useMemberStore.setState({ myRole: "owner" } as never);
});

describe("consentTokens", () => {
  it("uses the kind:value form the server compares", () => {
    expect(consentTokens(manifest.capabilities)).toEqual(["api:tasks:read", "events:task.updated", "secrets:token", "ui:task_panel"]);
  });
});

describe("connect wizard wording", () => {
  const gl = {
    id: "gitlab",
    capabilities: {
      api: ["tasks:read"], webhooks_in: ["gitlab"], secrets: ["token", "webhook_secret"],
      network: { allowed_domains: ["{settings.base_url}"] },
    },
    levels: { workspace: true },
    settings: { workspace: { type: "object", properties: {
      base_url: { type: "string", title: "Адрес GitLab" }, token: { type: "string", title: "Токен", "x-secret": true },
    } } },
  };
  const raw = ["{settings.base_url}", "webhook_secret", "Gitlab webhook"];

  it.each([
    ["en", "GitLab address", "Webhook secret", "Токен", "The address you enter in the settings"],
    ["ru", "Адрес GitLab", "Секрет вебхука", "Access token", "Адрес, который вы укажете в настройках"],
  ])("%s: no raw ids on step 1, field labels follow the locale on step 2", async (lng, addr, secret, foreign, domain) => {
    await setLanguage(lng as "en" | "ru");
    mount(mergeCatalog([sum("gitlab", { manifest: gl })], [ws("gitlab")]));
    const text = document.body.textContent ?? "";
    for (const r of raw) expect(text).not.toContain(r);
    const titles = Array.from(document.querySelectorAll("[title]")).map((e) => e.getAttribute("title") ?? "").join("|");
    for (const r of raw) expect(titles).not.toContain(r); // tooltips must not expose raw ids either
    expect(text).toContain(secret); // positive control: the humanised name is what is rendered
    expect(text).toContain(domain);
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: lng === "en" ? "Next" : /Далее|Продолжить/ }));
    expect(screen.getByLabelText(new RegExp(addr))).toBeTruthy();
    expect(document.body.textContent).not.toContain(foreign);
  });

  it("Back is the outline (secondary) button, Next stays primary", async () => {
    mount(mergeCatalog([sum("gitlab")], [ws("gitlab")]));
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(next());
    expect(screen.getByRole("button", { name: "Back" }).className).toContain("border-border");
    expect(next().className).toContain("bg-primary");
  });
});

describe("connect wizard", () => {
  it("red control: Next stays disabled until the consent box is ticked", () => {
    mount(mergeCatalog([sum("gitlab")], [ws("gitlab")]));
    expect(next()).toBeDisabled();
    fireEvent.click(screen.getByRole("checkbox"));
    expect(next()).toBeEnabled();
  });

  it("shows the red unchecked banner for custom extensions only", () => {
    mount(mergeCatalog([sum("gitlab", { source: "repo", trust_level: "custom" })], [ws("gitlab")]));
    expect(screen.getByRole("alert").textContent).toMatch(/Not checked by the catalog/);
  });

  it("walks the four steps: connect sends settings + consent tokens, then the check, then done", async () => {
    api.mockImplementation(async (path: string) => (path.endsWith("/test") ? { ok: true, message: "RAW upstream text" } : {}));
    mount(mergeCatalog([sum("gitlab")], [ws("gitlab")]));
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(next());
    fireEvent.change(screen.getByLabelText(/Base URL/), { target: { value: "https://gitlab.example" } });
    fireEvent.click(next());
    await screen.findByRole("button", { name: "Check connection" });
    expect(api).toHaveBeenCalledWith("/api/v1/workspaces/ws-1/extensions/gitlab/connect", {
      method: "POST",
      body: {
        settings: { base_url: "https://gitlab.example" },
        consent: { capabilities: ["api:tasks:read", "events:task.updated", "secrets:token", "ui:task_panel"] },
      },
    });
    fireEvent.click(screen.getByRole("button", { name: "Check connection" }));
    await screen.findByText("Connection works.");
    expect(screen.queryByText(/RAW upstream/)).toBeNull();
    fireEvent.click(next());
    expect(screen.getByText("What appeared")).toBeTruthy();
    expect(screen.getByText("Bind it to projects")).toBeTruthy();
  });

  it("resolves a $ref workspace schema into fields", () => {
    const m = { ...manifest, settings: { workspace: { $ref: "#/schemas/connection" } }, schemas: { connection: workspaceSchema } };
    mount(mergeCatalog([sum("gitlab", { manifest: m })], [ws("gitlab")]));
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(next());
    expect(screen.getByLabelText(/Base URL/)).toBeTruthy();
    expect(screen.queryByText("This extension needs no settings.")).toBeNull();
  });

  it("a failed health check shows our own text, never the server's", async () => {
    api.mockImplementation(async (path: string) => (path.endsWith("/test") ? { ok: false, message: "dial tcp: connection refused" } : {}));
    mount(mergeCatalog([sum("gitlab", { manifest: { ...manifest, settings: undefined } })], [ws("gitlab")]));
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(next());
    fireEvent.click(next());
    fireEvent.click(await screen.findByRole("button", { name: "Check connection" }));
    await screen.findByText(/The check failed/);
    expect(screen.queryByText(/dial tcp/)).toBeNull();
    expect(screen.getByRole("button", { name: "Check again" })).toBeTruthy();
  });

  it("a 400 on consent returns to step 1 with the box marked", async () => {
    api.mockRejectedValue(Object.assign(new Error("Validation failed"), { code: "validation_failed", status: 400, details: { consent: "consent must cover [x]" } }));
    mount(mergeCatalog([sum("gitlab", { manifest: { ...manifest, settings: undefined } })], [ws("gitlab")]));
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(next());
    fireEvent.click(next());
    await waitFor(() => expect(screen.getByText("Confirm the permissions to continue.")).toBeTruthy());
    expect(screen.getByRole("checkbox")).not.toBeChecked();
    expect(next()).toBeDisabled();
  });

  it("a 404 without a consent field shows the generic connect error instead of crashing on `in`", async () => {
    api.mockRejectedValue(Object.assign(new Error("Extension not found"), { code: "not_found", status: 404, details: "extension not found" }));
    mount(mergeCatalog([sum("gitlab", { manifest: { ...manifest, settings: undefined } })], [ws("gitlab")]));
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(next());
    fireEvent.click(next());
    await waitFor(() => expect(screen.getByText("Couldn't connect the extension.")).toBeTruthy());
    expect(screen.queryByText(/Cannot use 'in' operator/)).toBeNull();
  });

  it("instance-configured extension: read-only note and no connect call", async () => {
    api.mockResolvedValue({ ok: true, message: "" });
    mount(mergeCatalog([sum("gitlab")], [ws("gitlab", { enabled: true, source: "instance" })]));
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(next());
    expect(screen.getByText("Configured by the instance administrator")).toBeTruthy();
    expect(screen.getByLabelText(/Base URL/)).toBeDisabled();
    expect(screen.getAllByRole("button", { name: "Next" })).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Next" }));
    await screen.findByRole("button", { name: "Check connection" });
    expect(api).not.toHaveBeenCalled();
  });

  it("instance admin sees the instance-settings link on the settings step", async () => {
    overview.mockResolvedValue({});
    mount(mergeCatalog([sum("gitlab")], [ws("gitlab", { enabled: true, source: "instance" })]));
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(next());
    expect(await screen.findByRole("button", { name: "Change instance settings" })).toBeTruthy();
  });

  it("non-admin (instance API forbids) gets no instance-settings link", async () => {
    mount(mergeCatalog([sum("gitlab")], [ws("gitlab", { enabled: true, source: "instance" })]));
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(next());
    await waitFor(() => expect(overview).toHaveBeenCalled());
    expect(screen.queryByRole("button", { name: "Change instance settings" })).toBeNull();
  });

  it("instance-configured extension without workspace fields still never calls connect", async () => {
    api.mockResolvedValue({ ok: true, message: "" });
    mount(mergeCatalog([sum("gitlab", { manifest: { ...manifest, settings: undefined } })], [ws("gitlab", { enabled: true, source: "instance" })]));
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(next());
    expect(screen.getAllByRole("button", { name: "Next" })).toHaveLength(1);
    fireEvent.click(next());
    await screen.findByRole("button", { name: "Check connection" });
    expect(api).not.toHaveBeenCalled();
  });

  it("a consent 400 keeps what was typed on the settings step", async () => {
    api.mockRejectedValue(Object.assign(new Error("Validation failed"), { code: "validation_failed", status: 400, details: { consent: "consent must cover [x]" } }));
    mount(mergeCatalog([sum("gitlab")], [ws("gitlab")]));
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(next());
    fireEvent.change(screen.getByLabelText(/Base URL/), { target: { value: "https://gitlab.example" } });
    fireEvent.click(next());
    await screen.findByText("Confirm the permissions to continue.");
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(next());
    expect((screen.getByLabelText(/Base URL/) as HTMLInputElement).value).toBe("https://gitlab.example");
  });
  it("a consent 400 keeps a typed secret and the retry sends it", async () => {
    const secretSchema: FieldSchema = {
      type: "object", required: ["base_url", "token"],
      properties: { base_url: { type: "string", title: "Base URL" }, token: { type: "string", title: "Token", "x-secret": true } },
    };
    const calls: unknown[] = [];
    api.mockImplementation(async (_path: string, opts: { body: unknown }) => {
      calls.push(opts.body);
      if (calls.length === 1) {
        throw Object.assign(new Error("Validation failed"), { code: "validation_failed", status: 400, details: { consent: "consent must cover [x]" } });
      }
      return {};
    });
    mount(mergeCatalog([sum("gitlab", { manifest: { ...manifest, settings: { workspace: secretSchema } } })], [ws("gitlab")]));
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(next());
    fireEvent.change(screen.getByLabelText(/Base URL/), { target: { value: "https://gitlab.example" } });
    fireEvent.change(screen.getByLabelText(/Token/), { target: { value: "glpat-test" } });
    fireEvent.click(next());
    await screen.findByText("Confirm the permissions to continue.");
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(next());
    fireEvent.click(next());
    await screen.findByRole("button", { name: "Check connection" });
    expect((calls[1] as { settings: Record<string, string> }).settings).toEqual({ base_url: "https://gitlab.example", token: "glpat-test" });
  });
});

describe("theme from the catalog", () => {
  const themeSchema: FieldSchema = {
    type: "object",
    properties: { density: { type: "string", title: "Density", enum: ["compact", "comfortable"], default: "comfortable" } },
  };
  const themeInfo = {
    id: "mesh-default", name: "Mesh", version: "1.0.0", publisher: "Entire VC", description: "", source: "builtin",
    trust_level: "builtin", modes: ["light"], has_css: true, has_views: false,
    settings: { workspace: themeSchema }, tokens: {}, contrast_warnings: [],
  };
  const calls: { path: string; method: string; body?: unknown }[] = [];

  function mountTheme(themeId = "mesh-default") {
    state.value = {
      state: "ready", catalogUnavailable: false, reload: vi.fn(), refresh: vi.fn(),
      entries: mergeCatalog([sum(themeId, { kind: "theme", manifest: { capabilities: {}, levels: { workspace: true } } })], [], [], "graphite"),
    };
    return render(
      <MemoryRouter initialEntries={[`/w/acme/extensions/${themeId}/connect`]}>
        <Routes><Route path="w/:wsSlug/extensions/:extId/connect" element={<ExtensionConnectPage />} /></Routes>
      </MemoryRouter>,
    );
  }

  beforeEach(() => {
    calls.length = 0;
    api.mockImplementation(async (path: string, opts?: { method?: string; body?: unknown }) => {
      const method = opts?.method ?? "GET";
      calls.push({ path, method, body: opts?.body });
      if (path === "/api/v1/themes") return { default: "mesh-default", themes: [themeInfo], builtin_available: [] };
      if (path === "/api/v1/workspaces/ws-1/theme" && method === "GET") return { theme_id: "graphite", settings: {}, version: 3, contrast_warnings: [] };
      if (path === "/api/v1/workspaces/ws-1/theme" && method === "PUT") return { ...(opts?.body as object), version: 4, contrast_warnings: [] };
      if (path.startsWith("/api/v1/theme")) return { theme_id: "graphite", tokens_css: "" };
      throw Object.assign(new Error("Not found"), { status: 404 });
    });
  });

  it("applies through PUT /workspaces/:ws/theme and never calls the extension connect route", async () => {
    mountTheme();
    expect(screen.queryByRole("list", { name: "Connection steps" })).toBeNull();
    const apply = await screen.findByRole("button", { name: "Apply" });
    await waitFor(() => expect((apply as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(apply);
    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    const put = calls.find((c) => c.method === "PUT");
    expect(put?.path).toBe("/api/v1/workspaces/ws-1/theme");
    expect((put?.body as { theme_id: string }).theme_id).toBe("mesh-default");
    expect(calls.some((c) => c.path.includes("/extensions/"))).toBe(false);
  });

  it("names the missing theme instead of a bare Not found", async () => {
    api.mockImplementation(async (path: string, opts?: { method?: string }) => {
      const method = opts?.method ?? "GET";
      if (path === "/api/v1/themes") return { default: "mesh-default", themes: [themeInfo], builtin_available: [] };
      if (path === "/api/v1/workspaces/ws-1/theme" && method === "GET") return { theme_id: null, settings: {}, version: 0, contrast_warnings: [] };
      if (path === "/api/v1/workspaces/ws-1/theme") throw Object.assign(new Error("Not found"), { status: 404, errorCode: "not_found" });
      return { theme_id: "mesh-default", tokens_css: "" };
    });
    mountTheme();
    const apply = await screen.findByRole("button", { name: "Apply" });
    await waitFor(() => expect((apply as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(apply);
    expect(await screen.findByText("Theme mesh-default was not found on this instance.")).toBeTruthy();
  });
});
