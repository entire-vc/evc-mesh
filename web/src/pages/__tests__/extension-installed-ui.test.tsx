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
function mount(entries: CatalogState["entries"], state: CatalogState["state"] = "ready") {
  refresh = vi.fn();
  const catalog = { state, entries, catalogUnavailable: false, reload: vi.fn(), refresh } as unknown as CatalogState;
  return render(
    <MemoryRouter initialEntries={["/w/acme/extensions?tab=installed"]}>
      <Routes>
        <Route path="w/:wsSlug/extensions" element={<InstalledExtensions catalog={catalog} />} />
        <Route path="w/:wsSlug/extensions/:extId/connect" element={<div data-testid="theme-screen" />} />
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
  instanceAdmin.value = false;
  useMemberStore.setState({ myRole: "admin" } as never);
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws1", slug: "acme" } } as never);
});

describe("Installed list", () => {
  it("says where an enabled extension comes from", () => {
    mount(instanceGitlab());
    expect(screen.getByTestId("status-gitlab").textContent).toContain("Active · set up by the instance admin");
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
    expect(screen.queryByText("Connected in this workspace")).not.toBeNull();
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
    expect(within(dlg).getByTestId("ext-status-github").textContent).toContain("Active · set up by the instance admin");
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

  it("an instance admin gets a link to this extension's instance settings", () => {
    instanceAdmin.value = true;
    const dlg = open("github");
    expect(within(dlg).getByRole("link", { name: "Edit at instance level" }).getAttribute("href")).toBe("/w/acme/instance/extensions?ext=github");
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
  const themes = () => mergeCatalog([theme("mesh-default", "Mesh"), theme("graphite", "Graphite")], [], [], "graphite");

  it("shows the active theme and the not selected one, with no switch, check or removal", () => {
    mount(themes());
    expect(screen.getByTestId("status-graphite").textContent).toContain("Active in this workspace");
    expect(screen.getByTestId("status-mesh-default").textContent).toContain("Not selected");
    expect(screen.queryByRole("switch")).toBeNull();
    expect(screen.queryByRole("button", { name: /More actions/ })).toBeNull();
    expect(screen.getByRole("button", { name: "Configure" })).toBeTruthy();
  });

  it("a blocked active theme still says it is active, and shows the block", () => {
    const rows = mergeCatalog([{ ...theme("graphite", "Graphite"), blocked: true, blocked_reason: "CVE-1" }], [], [], "graphite");
    mount(rows);
    expect(screen.getByTestId("status-graphite").textContent).toContain("Active in this workspace");
    expect(screen.getByText("Blocked by the catalog: CVE-1")).toBeTruthy();
    expect((screen.getByRole("button", { name: "Configure" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("Choose opens the theme screen and writes nothing here", () => {
    mount(themes());
    fireEvent.click(screen.getByRole("button", { name: "Choose" }));
    expect(screen.getByTestId("theme-screen")).toBeTruthy();
    Object.values(api).forEach((f) => expect(f).not.toHaveBeenCalled());
  });
});
