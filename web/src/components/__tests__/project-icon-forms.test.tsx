import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { ProjectSettingsPage } from "@/pages/project-settings";
import { CreateProjectDialog } from "@/components/create-project-dialog";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import { api } from "@/lib/api";
import type { Project, Workspace } from "@/types";

// The icon chosen in IconPicker reaches the server: PATCH from project settings, POST from the
// create dialog. Only the HTTP boundary (api) is mocked; the stores and the picker are real.

vi.mock("@/lib/api", async () => ({ ...(await vi.importActual("@/lib/api")), api: vi.fn() }));

const project = (icon: string) =>
  ({ id: "project-1", workspace_id: "ws-1", slug: "project", name: "Project", description: "", icon, settings: {} }) as Project;

type Call = { path: string; method?: string; body?: Record<string, unknown> };
const calls = (method: string, path: RegExp): Call[] =>
  vi
    .mocked(api)
    .mock.calls.map(([p, o]) => ({ path: p as string, ...(o as Omit<Call, "path">) }))
    .filter((c) => c.method === method && path.test(c.path));

async function pickEmoji(trigger: HTMLElement, query: string, emoji: string) {
  fireEvent.click(trigger);
  const search = await screen.findByRole("searchbox");
  fireEvent.change(search, { target: { value: query } });
  const cell = await waitFor(() => {
    const el = Array.from(document.querySelectorAll<HTMLElement>("[data-emoji]")).find((b) => b.dataset.emoji === emoji);
    if (!el) throw new Error(`no ${emoji} cell`);
    return el;
  });
  fireEvent.click(cell);
  await waitFor(() => expect(screen.queryByRole("searchbox")).toBeNull());
}

beforeEach(() => {
  vi.mocked(api).mockReset();
  HTMLElement.prototype.scrollIntoView = vi.fn();
});

describe("project settings: the picked icon is saved", () => {
  function show(icon: string) {
    vi.mocked(api).mockImplementation(async (path: string, opts?: { method?: string; body?: unknown }) => {
      if (opts?.method === "PATCH") return { ...project(icon), ...(opts.body as object) };
      return /\/(statuses|custom-fields)$/.test(path) ? [] : { items: [], has_more: false };
    });
    useProjectStore.setState({ statuses: [], projects: [project(icon)], currentProject: project(icon) });
    render(
      <MemoryRouter initialEntries={["/w/ws/p/project/settings"]}>
        <Routes>
          <Route path="/w/:wsSlug/p/:projectSlug/settings" element={<ProjectSettingsPage />} />
        </Routes>
      </MemoryRouter>,
    );
  }
  const form = () => document.getElementById("ps-icon")!.closest("form")!;
  const save = () => fireEvent.click(within(form()).getByRole("button", { name: "Save Changes" }));
  const patched = async () => {
    await waitFor(() => expect(calls("PATCH", /\/projects\/project-1$/)).toHaveLength(1));
    return calls("PATCH", /\/projects\/project-1$/)[0]!.body!;
  };

  it("an emoji picked in IconPicker goes into PATCH and back into the store", async () => {
    show("");
    await pickEmoji(document.getElementById("ps-icon")!, "ракета", "🚀");
    save();
    expect((await patched()).icon).toBe("🚀");
    await waitFor(() => expect(useProjectStore.getState().currentProject?.icon).toBe("🚀"));
  });

  it("«No icon» clears the icon with an empty string", async () => {
    show("icon:rocket");
    fireEvent.click(document.getElementById("ps-icon")!);
    await screen.findByRole("searchbox");
    fireEvent.click(screen.getByRole("button", { name: "No icon" }));
    save();
    expect((await patched()).icon).toBe("");
  });

  it("a legacy text icon left alone is sent back unchanged", async () => {
    show("OLD");
    save();
    expect((await patched()).icon).toBe("OLD");
  });
});

describe("create project dialog: the picked icon is sent with the create request", () => {
  function show() {
    vi.mocked(api).mockImplementation(async (_path: string, opts?: { body?: unknown }) => ({
      ...project(""),
      ...(opts?.body as object),
    }));
    useProjectStore.setState({ projects: [] });
    useWorkspaceStore.setState({ currentWorkspace: { id: "ws-1", slug: "ws", name: "WS" } as Workspace });
    render(
      <MemoryRouter>
        <CreateProjectDialog open onOpenChange={vi.fn()} />
      </MemoryRouter>,
    );
  }
  const posted = async () => {
    await waitFor(() => expect(calls("POST", /\/workspaces\/ws-1\/projects$/)).toHaveLength(1));
    return calls("POST", /\/workspaces\/ws-1\/projects$/)[0]!.body!;
  };
  const submit = () => {
    fireEvent.change(document.getElementById("cp-name")!, { target: { value: "Picked" } });
    fireEvent.submit(document.getElementById("cp-name")!.closest("form")!);
  };

  it("an emoji picked before the name is in the POST body", async () => {
    show();
    await pickEmoji(screen.getByRole("button", { name: "Project icon" }), "rocket", "🚀");
    submit();
    expect(await posted()).toMatchObject({ name: "Picked", slug: "picked", icon: "🚀" });
  });

  it("no pick: the request carries no icon (server default)", async () => {
    show();
    submit();
    expect((await posted()).icon).toBeUndefined();
  });
});
