import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import type { CatalogState } from "@/hooks/useExtensionCatalog";
import { mergeCatalog } from "@/hooks/useExtensionCatalog";

const api = vi.hoisted(() => ({
  setEnabled: vi.fn(), connect: vi.fn(), disconnect: vi.fn(), test: vi.fn(), save: vi.fn(),
}));
vi.mock("@/lib/extension-settings", () => ({
  setWorkspaceExtensionEnabled: api.setEnabled,
  connectWorkspaceExtension: api.connect,
  disconnectWorkspaceExtension: api.disconnect,
  testWorkspaceExtension: api.test,
  saveWorkspaceExtensionSettings: api.save,
}));

const themeApi = vi.hoisted(() => ({ put: vi.fn(), apply: vi.fn() }));
vi.mock("@/lib/themes", () => ({ putWorkspaceTheme: themeApi.put }));
vi.mock("@/core/themes/runtime", () => ({ applyWorkspaceTheme: themeApi.apply }));
// the theme panel has its own tests: here only that it opens on the theme, in the Settings window
vi.mock("@/components/themes/theme-settings-panel", async () => {
  const { createPortal } = await import("react-dom");
  return {
    ThemeSettingsPanel: (p: { initialThemeId?: string; level: string; workspaceId?: string; readOnly?: boolean; actionsContainer?: HTMLElement | null }) => (
      <div data-testid="theme-panel" data-theme={p.initialThemeId} data-level={p.level} data-ws={p.workspaceId} data-readonly={String(!!p.readOnly)}>
        {p.actionsContainer ? createPortal(<button type="button">Save theme</button>, p.actionsContainer) : <button type="button">Save theme</button>}
      </div>
    ),
  };
});

const instanceAdmin = vi.hoisted(() => ({ value: false }));
vi.mock("@/lib/extension-admin", () => ({ useIsInstanceAdmin: () => instanceAdmin.value }));

import { setLanguage } from "@/i18n";
import { InstalledExtensions } from "@/components/extensions/installed";
import { useMemberStore } from "@/stores/member";
import { useWorkspaceStore } from "@/stores/workspace";

const sum = (id: string, name: string) => ({
  id, name, version: "1.0.0", kind: "integration", runtime: "builtin", publisher: "Entire VC",
  description: "", category: "development", source: "builtin", trust_level: "builtin",
  blocked: false, blocked_reason: "", manifest: { mesh_api: ">=1 <2", capabilities: {} },
});

let refresh: ReturnType<typeof vi.fn>;
function mount(entries: CatalogState["entries"], state: CatalogState["state"] = "ready", url = "/w/acme/extensions?tab=installed") {
  refresh = vi.fn();
  const catalog = { state, entries, catalogUnavailable: false, reload: vi.fn(), refresh } as unknown as CatalogState;
  return render(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route path="w/:wsSlug/extensions" element={<InstalledExtensions catalog={catalog} />} />
        <Route path="w/:wsSlug/extensions/:extId/connect" element={<div data-testid="theme-screen" />} />
        <Route path="w/:wsSlug/runner" element={<div data-testid="runner-screen" />} />
      </Routes>
    </MemoryRouter>,
  );
}

const instanceGitlab = () =>
  mergeCatalog([sum("gitlab", "GitLab")], [{
    id: "gitlab", name: "GitLab", version: "1.0.0", kind: "integration", category: "development",
    connection: null, status: { enabled: true, source: "instance", health: { ok: true, message: "" } },
  }] as never);

beforeEach(async () => {
  await setLanguage("en");
  Object.values(api).forEach((f) => f.mockReset().mockResolvedValue({ ok: true }));
  Object.values(themeApi).forEach((f) => f.mockReset().mockResolvedValue({}));
  instanceAdmin.value = false;
  useMemberStore.setState({ myRole: "admin" } as never);
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws1", slug: "acme" } } as never);
});

describe("Installed list", () => {
  it("the runner program has no switch or settings here, only the way to the Runner screen", async () => {
    mount(mergeCatalog([{ ...sum("agent-runner", "Agent Runner"), runtime: "external", source: "catalog", trust_level: "official" }], [{
      id: "agent-runner", name: "Agent Runner", version: "0.1.0", kind: "plugin", category: "agents",
      connection: { settings: { runner_url: "https://runner.example.com" } }, status: { enabled: true, source: "workspace" },
    }] as never));
    const row = document.querySelector('[data-ext="agent-runner"]') as HTMLElement;
    expect(within(row).queryByRole("switch")).toBeNull();
    expect(within(row).queryByRole("button", { name: "Settings" })).toBeNull();
    fireEvent.click(within(row).getByRole("button", { name: "Manage in Runner" }));
    expect(await screen.findByTestId("runner-screen")).toBeTruthy();
  });

  it("?settings=agent-runner opens no settings window and goes to the Runner screen", async () => {
    mount(mergeCatalog([{ ...sum("agent-runner", "Agent Runner"), runtime: "external", source: "catalog", trust_level: "official" }], [{
      id: "agent-runner", name: "Agent Runner", version: "0.1.0", kind: "plugin", category: "agents",
      connection: null, status: { enabled: false, source: "workspace" },
    }] as never), "ready", "/w/acme/extensions?tab=installed&settings=agent-runner");
    expect(await screen.findByTestId("runner-screen")).toBeTruthy();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("says where an enabled extension comes from", () => {
    mount(instanceGitlab());
    expect(screen.getByTestId("status-gitlab").textContent).toContain("Enabled in workspace");
    expect(screen.getByTestId("status-gitlab").textContent).toContain("keys from the instance");
  });

  it("turning off an instance-sourced extension writes nothing until the modal is confirmed", async () => {
    mount(instanceGitlab());
    fireEvent.click(screen.getByRole("switch", { name: "GitLab enabled" }));
    expect(await screen.findByText(/Turning it off affects only this workspace/)).toBeTruthy();
    expect(api.setEnabled).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Turn off" }));
    await waitFor(() => expect(api.setEnabled).toHaveBeenCalledWith("ws1", "gitlab", false, true));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
  });

  it("cancelling the modal leaves state untouched", async () => {
    mount(instanceGitlab());
    fireEvent.click(screen.getByRole("switch", { name: "GitLab enabled" }));
    fireEvent.click(await screen.findByRole("button", { name: "Cancel" }));
    expect(api.setEnabled).not.toHaveBeenCalled();
  });

  it("a workspace-sourced extension is switched off without confirm", async () => {
    mount(mergeCatalog([sum("gitlab", "GitLab")], [{
      id: "gitlab", name: "GitLab", version: "1.0.0", kind: "integration", category: "development",
      connection: { settings: {} }, status: { enabled: true, source: "workspace", health: { ok: true, message: "" } },
    }] as never));
    fireEvent.click(screen.getByRole("switch", { name: "GitLab enabled" }));
    await waitFor(() => expect(api.setEnabled).toHaveBeenCalledWith("ws1", "gitlab", false));
  });

  it("an extension the administrator does not allow cannot be switched on or connected", () => {
    mount(mergeCatalog([sum("gitlab", "GitLab"), { ...sum("github", "GitHub"), allowed: false }], [{
      id: "gitlab", name: "GitLab", version: "1.0.0", kind: "integration", category: "development",
      connection: { settings: {} }, status: { enabled: false, source: "workspace", not_allowed: true },
    }, {
      id: "github", name: "GitHub", version: "1.0.0", kind: "integration", category: "development",
      connection: null, status: { enabled: false, source: "none" },
    }] as never));
    expect(screen.getByRole("switch", { name: "GitLab enabled" })).toBeDisabled();
    const github = screen.getByRole("switch", { name: "GitHub enabled" });
    expect(github).toBeDisabled();
    const row = github.closest("li") as HTMLElement;
    expect(within(row).getByRole("button", { name: /connect/i })).toBeDisabled();
  });

  it("Remove keeps the extension's data and only drops the connection", async () => {
    mount(mergeCatalog([sum("gitlab", "GitLab")], [{
      id: "gitlab", name: "GitLab", version: "1.0.0", kind: "integration", category: "development",
      connection: { settings: {} }, status: { enabled: true, source: "workspace", health: { ok: true, message: "" } },
    }] as never));
    fireEvent.click(screen.getByRole("button", { name: "More actions for GitLab" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Remove" }));
    const del = screen.getByRole("radio", { name: /Delete the data too/ }) as HTMLInputElement;
    expect(del.disabled).toBe(true);
    expect((screen.getByRole("radio", { name: /Keep the data/ }) as HTMLInputElement).checked).toBe(true);
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(api.disconnect).toHaveBeenCalledWith("ws1", "gitlab", "keep"));
  });

  it("Remove offers deleting the data where the extension supports it, and sends the choice", async () => {
    mount(mergeCatalog([sum("gitlab", "GitLab")], [{
      id: "gitlab", name: "GitLab", version: "1.0.0", kind: "integration", category: "development", can_purge_data: true,
      connection: { settings: {} }, status: { enabled: true, source: "workspace", health: { ok: true, message: "" } },
    }] as never));
    fireEvent.click(screen.getByRole("button", { name: "More actions for GitLab" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Remove" }));
    fireEvent.click(screen.getByRole("radio", { name: /Delete the data too/ }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(api.disconnect).toHaveBeenCalledWith("ws1", "gitlab", "delete"));
  });

  it("the built-in Runner is not listed; the extension that requires it is", () => {
    mount(mergeCatalog([sum("runner", "Runner"), sum("agent-chat", "Chat with Agent")], [{
      id: "runner", name: "Runner", version: "1.0.0", kind: "plugin", category: "agents", settings_managed_elsewhere: true, hidden: true,
      connection: { settings: {} }, status: { enabled: true, source: "workspace", health: { ok: true, message: "" } },
    }, {
      id: "agent-chat", name: "Chat with Agent", version: "1.0.0", kind: "plugin", category: "agents",
      connection: { settings: {} }, status: { enabled: true, source: "workspace", health: { ok: true, message: "" } },
    }] as never));
    expect(screen.queryByText("Runner")).toBeNull();
    expect(screen.queryByText("own keys")).not.toBeNull();
    expect(screen.getByRole("switch", { name: "Chat with Agent enabled" })).toBeTruthy();
  });

  it("an extension whose settings live in its own API gets no generic Settings or Remove", () => {
    mount(mergeCatalog([sum("vault", "Vault")], [{
      id: "vault", name: "Vault", version: "1.0.0", kind: "integration", category: "development", settings_managed_elsewhere: true,
      connection: { settings: {} }, status: { enabled: true, source: "workspace", health: { ok: true, message: "" } },
    }] as never));
    expect((screen.getByRole("button", { name: "Settings" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "More actions for Vault" }));
    expect(screen.getByRole("menuitem", { name: "Remove" }).getAttribute("aria-disabled")).toBe("true");
  });

  it("managed elsewhere without a connection: the switch does not open the generic Connect form", () => {
    mount(mergeCatalog([sum("vault", "Vault")], [{
      id: "vault", name: "Vault", version: "1.0.0", kind: "integration", category: "development", settings_managed_elsewhere: true,
      connection: null, status: { enabled: false, source: "none", health: null },
    }] as never));
    const sw = screen.getByRole("switch", { name: "Vault enabled" }) as HTMLButtonElement;
    expect(sw.disabled).toBe(true);
    fireEvent.click(sw);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("a dialog opened in one workspace closes on switching, and never writes to the other", async () => {
    mount(mergeCatalog([sum("gitlab", "GitLab")], [{
      id: "gitlab", name: "GitLab", version: "1.0.0", kind: "integration", category: "development",
      connection: { settings: {} }, status: { enabled: true, source: "workspace", health: { ok: true, message: "" } },
    }] as never));
    fireEvent.click(screen.getByRole("button", { name: "More actions for GitLab" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Remove" }));
    expect(screen.getByRole("dialog")).toBeTruthy();
    act(() => { useWorkspaceStore.setState({ currentWorkspace: { id: "ws2", slug: "other" } } as never); });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(api.disconnect).not.toHaveBeenCalled();
  });

  it("Test connection runs the extension test, and Roll back stays inert until the backend exists", async () => {
    mount(instanceGitlab());
    fireEvent.click(screen.getByRole("button", { name: "More actions for GitLab" }));
    const rollback = screen.getByRole("menuitem", { name: /Roll back/ });
    expect(rollback.getAttribute("aria-disabled")).toBe("true");
    fireEvent.click(rollback);
    expect(api.test).not.toHaveBeenCalled();
    if (!screen.queryByRole("menuitem", { name: "Test connection" })) {
      fireEvent.click(screen.getByRole("button", { name: "More actions for GitLab" }));
    }
    fireEvent.click(screen.getByRole("menuitem", { name: "Test connection" }));
    await waitFor(() => expect(api.test).toHaveBeenCalledWith("ws1", "gitlab"));
    expect(api.setEnabled).not.toHaveBeenCalled();
    expect(api.disconnect).not.toHaveBeenCalled();
  });

  it("a member cannot trigger Test or Remove from the menu (no request is sent)", () => {
    useMemberStore.setState({ myRole: "member" } as never);
    mount(instanceGitlab());
    fireEvent.click(screen.getByRole("button", { name: "More actions for GitLab" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Test connection" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Remove" }));
    expect(api.test).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("a member sees the list read-only", () => {
    useMemberStore.setState({ myRole: "member" } as never);
    mount(instanceGitlab());
    expect((screen.getByRole("switch", { name: "GitLab enabled" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText("Only workspace owners and admins can change extensions")).toBeTruthy();
  });

  it("shows loading, error and empty states", () => {
    const { unmount } = mount([], "loading");
    expect(screen.getByRole("status")).toBeTruthy();
    unmount();
    const e = mount([], "error");
    expect(screen.getByRole("alert").textContent).toContain("Could not load installed extensions");
    e.unmount();
    mount([]);
    expect(screen.getByText("Nothing is installed yet")).toBeTruthy();
  });

  it("Settings of an instance-managed GitLab shows its status and links, never Connect or a consent", () => {
    const tokenField = { settings: { workspace: { type: "object", properties: { token: { type: "string", title: "Access token", "x-secret": true } } } } };
    mount(mergeCatalog([{ ...sum("gitlab", "GitLab"), manifest: { ...sum("gitlab", "GitLab").manifest, ...tokenField } }] as never, [{
      id: "gitlab", name: "GitLab", version: "1.0.0", kind: "integration", category: "development",
      connection: null, status: { enabled: true, source: "instance", health: { ok: true, message: "" } },
      activity: { links: 1855, last_event_at: "2026-10-25T22:00:00Z" },
    }] as never));
    fireEvent.click(screen.getByRole("button", { name: "Settings" }));
    const dlg = screen.getByRole("dialog");
    expect(within(dlg).getByRole("heading", { level: 2 }).textContent).toBe("GitLab settings");
    expect(within(dlg).getByTestId("ext-activity-gitlab").textContent).toMatch(/1,855 links, last event/);
    expect(within(dlg).getByTestId("ext-status-gitlab").textContent).toContain("Health check passed");
    expect(within(dlg).queryByText("This extension will be able to:")).toBeNull();
    expect(within(dlg).queryByRole("button", { name: "Connect" })).toBeNull();
    expect(dlg.querySelector("input")).toBeNull();
    fireEvent.click(within(dlg).getByRole("button", { name: "Close" }));
    expect(api.connect).not.toHaveBeenCalled();
    expect(api.save).not.toHaveBeenCalled();
  });

  it("Settings of a GitHub with its own workspace connection keeps its fields", () => {
    const secretField = { settings: { workspace: { type: "object", properties: { secret: { type: "string", title: "Webhook secret", "x-secret": true } } } } };
    mount(mergeCatalog([{ ...sum("github", "GitHub"), manifest: { ...sum("github", "GitHub").manifest, ...secretField } }] as never, [{
      id: "github", name: "GitHub", version: "1.0.0", kind: "integration", category: "development",
      connection: { settings: {} }, status: { enabled: true, source: "workspace", health: { ok: true, message: "" } },
      activity: { links: 224 },
    }] as never));
    fireEvent.click(screen.getByRole("button", { name: "Settings" }));
    const dlg = screen.getByRole("dialog");
    expect(within(dlg).getByTestId("ext-activity-github").textContent).toMatch(/^224 links/);
    expect(within(dlg).getByRole("button", { name: "Save" })).toBeTruthy();
    expect(within(dlg).queryByText("This extension will be able to:")).toBeNull();
  });
});

describe("Settings of an instance-connected VCS extension", () => {
  const secretFields = { settings: { workspace: { type: "object", properties: {
    token: { type: "string", title: "Access token", "x-secret": true },
    secret: { type: "string", title: "Webhook secret", "x-secret": true },
  } } } };
  const repos = [
    { repo: "entire-vc/evc-mesh", host: "gitlab.example.invalid", links: 1200, last_event_at: "2026-10-25T22:00:00Z" },
    { repo: "entire-vc/evc-spark", host: "gitlab.example.invalid", links: 655 },
  ];
  // GitHub in production carries an empty workspace connection row while the instance governs it; GitLab carries none.
  const vcs = (id: "github" | "gitlab", over: Record<string, unknown> = {}) => {
    const name = id === "github" ? "GitHub" : "GitLab";
    return mergeCatalog([{ ...sum(id, name), manifest: { ...sum(id, name).manifest, ...secretFields } }] as never, [{
      id, name, version: "1.0.0", kind: "integration", category: "development",
      connection: id === "github" ? { settings: {} } : null,
      status: { enabled: true, source: "instance", health: { ok: true, message: "" } },
      activity: { links: 1855, last_event_at: "2026-10-25T22:00:00Z", repos },
      ...over,
    }] as never);
  };
  const open = (id: "github" | "gitlab", over?: Record<string, unknown>) => {
    mount(vcs(id, over));
    fireEvent.click(screen.getByRole("button", { name: "Settings" }));
    return screen.getByRole("dialog");
  };
  // The dialog's skeleton: headings, labels, buttons and links, with the extension name stripped.
  const shape = (dlg: HTMLElement, name: string) =>
    [...dlg.querySelectorAll("h2, dt, button, a, li, input, p")]
      .map((n) => `${n.tagName}:${(n.textContent ?? "").split(name).join("X").replace(/\d[\d,]*/g, "N").replace(/last event .*/, "last event")}`);

  it("instance-managed GitHub shows no input fields and no Save", () => {
    const dlg = open("github");
    expect(dlg.querySelector("input")).toBeNull();
    expect(within(dlg).queryByLabelText(/Access token|Webhook secret/)).toBeNull();
    expect(within(dlg).queryByRole("button", { name: "Save" })).toBeNull();
    expect(within(dlg).getByTestId("ext-status-github").textContent).toContain("keys from the instance");
  });

  it("GitHub and GitLab get the same dialog", () => {
    const gh = shape(open("github"), "GitHub");
    cleanup();
    const gl = shape(open("gitlab"), "GitLab");
    expect(gh.length).toBeGreaterThan(5);
    expect(gh).toEqual(gl);
  });

  it("lists the repositories sending links, and says so when there are none", () => {
    const dlg = open("gitlab");
    const list = within(dlg).getByRole("list", { name: "Repositories sending links" });
    expect(within(list).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      expect.stringMatching(/entire-vc\/evc-mesh.*git\.entire\.host.*1,200 links/),
      expect.stringMatching(/entire-vc\/evc-spark.*655 links/),
    ]);
    cleanup();
    const bare = open("github", { activity: { links: 0 } });
    expect(within(bare).getByText("No repository activity yet")).toBeTruthy();
  });

  it("each repository row puts its meta on a second line under the name, at any width", () => {
    const dlg = open("gitlab");
    const list = within(dlg).getByRole("list", { name: "Repositories sending links" });
    for (const li of within(list).getAllByRole("listitem")) {
      // A stacked column: no side-by-side wrap that keeps short names and their meta on one line.
      expect(li.className).toContain("flex-col");
      expect(li.className).not.toContain("justify-between");
      expect(li.children).toHaveLength(2);
      expect(li.children[1]?.textContent).toMatch(/^git\.entire\.host · /);
    }
  });

  it("an instance admin gets a link to this extension's instance settings", () => {
    instanceAdmin.value = true;
    const dlg = open("github");
    expect(within(dlg).getByRole("link", { name: "Edit at instance level" }).getAttribute("href")).toBe("/w/acme/instance?ext=github");
    expect(within(dlg).queryByText("Credentials are managed by the instance admin")).toBeNull();
  });

  it("a workspace admin who is not the instance admin gets no link, only who manages it", () => {
    const dlg = open("gitlab");
    expect(within(dlg).queryByRole("link", { name: "Edit at instance level" })).toBeNull();
    expect(within(dlg).getByText("Credentials are managed by the instance admin")).toBeTruthy();
  });

  it("own credentials are an explicit opt-in; closing it changes nothing", async () => {
    const dlg = open("github");
    fireEvent.click(within(dlg).getByRole("button", { name: "Use own credentials for this workspace" }));
    expect(within(dlg).getByLabelText(/Webhook secret/)).toBeTruthy();
    fireEvent.click(within(dlg).getByRole("button", { name: "Cancel" }));
    expect(dlg.querySelector("input")).toBeNull();
    expect(api.save).not.toHaveBeenCalled();
    expect(api.connect).not.toHaveBeenCalled();
  });

  it("the own-credentials form keeps Cancel next to its submit, and the hint above the fields", () => {
    for (const [id, submit] of [["github", "Save"], ["gitlab", "Connect"]] as const) {
      const dlg = open(id);
      fireEvent.click(within(dlg).getByRole("button", { name: "Use own credentials for this workspace" }));
      const cancel = within(dlg).getByRole("button", { name: "Cancel" });
      const save = within(dlg).getByRole("button", { name: submit });
      expect(cancel.parentElement).toBe(save.parentElement);
      const hint = within(dlg).getByText(/Nothing changes until you save/);
      const field = within(dlg).getByLabelText(/Access token/);
      expect(hint.compareDocumentPosition(field) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
      cleanup();
    }
  });

  it("own credentials on a workspace without a connection ask for consent and connect", async () => {
    const dlg = open("gitlab");
    fireEvent.click(within(dlg).getByRole("button", { name: "Use own credentials for this workspace" }));
    expect(within(dlg).getByText("This extension will be able to:")).toBeTruthy();
    fireEvent.change(within(dlg).getByLabelText(/Access token/), { target: { value: "glpat-x" } });
    fireEvent.click(within(dlg).getByRole("checkbox"));
    fireEvent.click(within(dlg).getByRole("button", { name: "Connect" }));
    await waitFor(() => expect(api.connect).toHaveBeenCalledWith("ws1", "gitlab", expect.objectContaining({ token: "glpat-x" }), expect.anything()));
  });
});

describe("Installed list — themes", () => {
  const theme = (id: string, name: string) => ({ ...sum(id, name), kind: "theme" });
  const themes = () => mergeCatalog([sum("gitlab", "GitLab"), theme("mesh-default", "Mesh"), theme("graphite", "Graphite")], [], [], "graphite");

  it("lists themes in a section of their own, after the extensions", () => {
    mount(themes());
    const section = screen.getByTestId("section-themes");
    expect(within(section).getByRole("heading", { name: /Themes/ })).toBeTruthy();
    expect(section.querySelector('[data-ext="graphite"]')).toBeTruthy();
    expect(section.querySelector('[data-ext="mesh-default"]')).toBeTruthy();
    expect(section.querySelector('[data-ext="gitlab"]')).toBeNull();
    expect(section.querySelectorAll('[data-kind="theme"]').length).toBe(2);
  });

  it("a theme has the extension switch and Settings, but no check or removal", () => {
    mount(themes());
    expect(screen.getByTestId("status-graphite").textContent).toContain("Enabled in workspace");
    expect(screen.getByTestId("status-mesh-default").textContent).toContain("Available");
    expect(screen.getByRole("switch", { name: "Graphite enabled" }).getAttribute("aria-checked")).toBe("true");
    expect(screen.getByRole("switch", { name: "Mesh enabled" }).getAttribute("aria-checked")).toBe("false");
    expect(screen.queryByRole("button", { name: /More actions for Graphite/ })).toBeNull();
    expect(screen.getByRole("button", { name: /More actions for GitLab/ })).toBeTruthy();
  });

  it("switching a theme on makes it the workspace theme; off goes back to the instance theme", async () => {
    mount(themes());
    fireEvent.click(screen.getByRole("switch", { name: "Mesh enabled" }));
    await waitFor(() => expect(themeApi.put).toHaveBeenCalledWith("ws1", "mesh-default", {}));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(themeApi.apply).toHaveBeenCalledWith("acme");

    fireEvent.click(screen.getByRole("switch", { name: "Graphite enabled" }));
    await waitFor(() => expect(themeApi.put).toHaveBeenCalledWith("ws1", null, {}));
    expect(api.setEnabled).not.toHaveBeenCalled();
    expect(api.connect).not.toHaveBeenCalled();
  });

  it("Settings opens the theme panel in the Settings window, on that theme", () => {
    mount(themes());
    const row = screen.getByTestId("section-themes").querySelector('[data-ext="graphite"]') as HTMLElement;
    fireEvent.click(within(row).getByRole("button", { name: "Settings" }));
    const dialog = screen.getByRole("dialog", { name: "Graphite" });
    const panel = within(dialog).getByTestId("theme-panel");
    expect(panel.dataset.theme).toBe("graphite");
    expect(panel.dataset.level).toBe("workspace");
    expect(panel.dataset.ws).toBe("ws1");
    expect(screen.queryByTestId("theme-screen")).toBeNull();
    // Save sits in the footer, outside the scroll area, so at 393 px it cannot cover a theme card
    const save = within(dialog).getByRole("button", { name: "Save theme" });
    expect(screen.getByTestId("theme-settings-body").contains(save)).toBe(false);
    fireEvent.click(within(dialog).getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("?settings=<id> opens that theme's Settings window", async () => {
    mount(themes(), "ready", "/w/acme/extensions?tab=installed&settings=mesh-default");
    const dialog = await screen.findByRole("dialog", { name: "Mesh" });
    expect(within(dialog).getByTestId("theme-panel").dataset.theme).toBe("mesh-default");
  });

  it("a member opens only the active theme's Settings, read-only, from the row and from ?settings=<id>", async () => {
    useMemberStore.setState({ myRole: "member", myRoleLoaded: true } as never);
    mount(themes(), "ready", "/w/acme/extensions?tab=installed&settings=graphite");
    const dialog = await screen.findByRole("dialog", { name: "Graphite" });
    expect(within(dialog).getByTestId("theme-panel").dataset.readonly).toBe("true");
    fireEvent.click(within(dialog).getByRole("button", { name: "Close" }));
    const settings = (id: string) => within(screen.getByTestId(`status-${id}`).closest("[data-ext]") as HTMLElement)
      .getByRole("button", { name: "Settings" }) as HTMLButtonElement;
    expect(settings("graphite").disabled).toBe(false);
    expect(settings("mesh-default").disabled).toBe(true);
  });

  it("?settings=<inactive theme> does not open a window for a member", () => {
    useMemberStore.setState({ myRole: "member", myRoleLoaded: true } as never);
    mount(themes(), "ready", "/w/acme/extensions?tab=installed&settings=mesh-default");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("a blocked theme reads as blocked with the reason, like any extension, and cannot be changed", () => {
    const rows = mergeCatalog([{ ...theme("graphite", "Graphite"), blocked: true, blocked_reason: "CVE-1" }], [], [], "graphite");
    mount(rows);
    expect(screen.getByTestId("status-graphite").textContent).toContain("Blocked by the catalog");
    expect(screen.getByTestId("status-graphite").textContent).toContain("CVE-1");
    expect((screen.getByRole("button", { name: "Settings" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("switch", { name: "Graphite enabled" }) as HTMLButtonElement).disabled).toBe(true);
  });
});

describe("one health for the row and the Settings window", () => {
  const keep = (ok: boolean) => mergeCatalog([sum("keep", "Keep")], [{
    id: "keep", name: "Keep", version: "1.0.0", kind: "integration", category: "security",
    connection: null,
    status: ok
      ? { enabled: true, source: "instance", health: { ok: true, message: "" } }
      : { enabled: true, source: "instance", health: { ok: false, message: "401 Unauthorized" }, last_error: "401 Unauthorized" },
  }] as never);
  const view = (entries: CatalogState["entries"]) => {
    const catalog = { state: "ready", entries, catalogUnavailable: false, reload: vi.fn(), refresh } as unknown as CatalogState;
    return (
      <MemoryRouter initialEntries={["/w/acme/extensions?tab=installed"]}>
        <Routes><Route path="w/:wsSlug/extensions" element={<InstalledExtensions catalog={catalog} />} /></Routes>
      </MemoryRouter>
    );
  };

  it("an error shows its text and Check; after the check both read the re-read status", async () => {
    refresh = vi.fn();
    const { rerender } = render(view(keep(false)));
    const row = screen.getByTestId("status-keep");
    expect(row.textContent).toContain("Error");
    expect(row.textContent).toContain("401 Unauthorized");
    expect(within(row).getByRole("button", { name: "Check" })).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Settings" }));
    const panel = within(screen.getByRole("dialog")).getByTestId("ext-status-keep");
    expect(panel.textContent).toContain("Error");
    expect(panel.textContent).toContain("Health check failed");
    fireEvent.click(within(panel).getByRole("button", { name: "Check" }));
    await waitFor(() => expect(api.test).toHaveBeenCalledWith("ws1", "keep"));
    await waitFor(() => expect(refresh).toHaveBeenCalled());

    // the list is re-read: the open window follows it instead of keeping its own result
    rerender(view(keep(true)));
    const after = within(screen.getByRole("dialog")).getByTestId("ext-status-keep");
    expect(after.textContent).toContain("Enabled in workspace");
    expect(after.textContent).toContain("Health check passed");
    expect(after.textContent).not.toContain("401");
    expect(screen.getByTestId("status-keep").textContent).toContain("Enabled in workspace");
    expect(screen.getByTestId("status-keep").textContent).not.toContain("401");
  });
});
