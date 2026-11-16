import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { useMemberStore } from "@/stores/member";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";
import { InitiativesPage } from "@/pages/initiatives";
import type { Initiative, WorkspaceRole } from "@/types";
import { selectInitiatives } from "../hooks";
import { initiativeRights } from "../rights";
import { InitiativesWidget, InitiativeWidget } from "../views";

// The network is the only mock: widgets and the page run their real hooks and store.
vi.mock("@/lib/api", () => ({ api: vi.fn() }));
import { api } from "@/lib/api";

const ini = (id: string, over: Partial<Initiative> = {}): Initiative => ({
  id,
  workspace_id: "ws1",
  name: `Initiative ${id}`,
  description: `About ${id}`,
  status: "active",
  target_date: null,
  created_by: "u1",
  created_at: "2026-11-01T00:00:00Z",
  updated_at: "2026-11-01T00:00:00Z",
  total_tasks: 4,
  completed_tasks: 1,
  ...over,
});

const ALPHA = ini("alpha", { name: "Alpha", created_at: "2026-11-02T00:00:00Z", target_date: "2026-12-20", completed_tasks: 3 });
const BETA = ini("beta", { name: "Beta", status: "archived", created_at: "2026-11-03T00:00:00Z", target_date: "2026-12-01" });
const GAMMA = ini("gamma", { name: "Gamma", created_at: "2026-11-01T00:00:00Z" });
const DETAIL: Initiative = {
  ...ALPHA,
  linked_projects: [{ id: "p1", name: "Core", slug: "core", description: "" } as never],
};

function serve() {
  vi.mocked(api).mockImplementation(async (url: string, opts?: { method?: string; body?: unknown }) => {
    const method = opts?.method ?? "GET";
    if (url === "/api/v1/workspaces/ws1/initiatives" && method === "GET") return [ALPHA, BETA, GAMMA];
    if (url === "/api/v1/workspaces/ws1/initiatives" && method === "POST") {
      return ini("fresh", { name: (opts!.body as { name: string }).name });
    }
    if (url === "/api/v1/initiatives/alpha" && method === "GET") return DETAIL;
    if (url === "/api/v1/initiatives/missing") throw new Error("Initiative not found");
    if (url === "/api/v1/workspaces/ws1/projects") return { items: [], total: 0 };
    return {};
  });
}

function Where() {
  const l = useLocation();
  return <output data-testid="where">{l.pathname + l.search}</output>;
}

function mount(node: React.ReactNode, { role = "member" as WorkspaceRole | null, at = "/w/acme/dash" } = {}) {
  useMemberStore.setState({ myRole: role, myRoleLoaded: true, myRoleWorkspaceId: "ws1" });
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws1", slug: "acme", name: "Acme" } as never });
  useProjectStore.setState({
    projects: [
      { id: "p1", slug: "core", name: "Core" } as never,
      { id: "p2", slug: "ops", name: "Ops" } as never,
    ],
  });
  return render(
    <MemoryRouter initialEntries={[at]}>
      <Routes>
        <Route path="/w/:wsSlug/dash" element={node} />
        <Route path="/w/:wsSlug/initiatives/*" element={<InitiativesPage />} />
        <Route path="*" element={null} />
      </Routes>
      <Where />
    </MemoryRouter>,
  );
}

beforeEach(serve);
afterEach(() => {
  cleanup();
  vi.mocked(api).mockReset();
});

describe("initiativeRights mirrors the server's RBAC", () => {
  it("viewer: nothing; member: create, edit, link but not delete; owner/admin: all", () => {
    expect(initiativeRights("viewer")).toEqual({ create: false, edit: false, link: false, delete: false });
    expect(initiativeRights("member")).toEqual({ create: true, edit: true, link: true, delete: false });
    expect(initiativeRights("admin")).toEqual({ create: true, edit: true, link: true, delete: true });
    expect(initiativeRights("owner")).toEqual({ create: true, edit: true, link: true, delete: true });
    // An unknown role shows no action the server could refuse.
    expect(initiativeRights(null)).toEqual({ create: false, edit: false, link: false, delete: false });
  });
});

describe("selectInitiatives", () => {
  it("filters by status and sorts by each key", () => {
    const all = [ALPHA, BETA, GAMMA];
    expect(selectInitiatives(all, {}).map((i) => i.id)).toEqual(["beta", "alpha", "gamma"]);
    expect(selectInitiatives(all, { statuses: ["active"] }).map((i) => i.id)).toEqual(["alpha", "gamma"]);
    expect(selectInitiatives(all, { sort: "name" }).map((i) => i.id)).toEqual(["alpha", "beta", "gamma"]);
    expect(selectInitiatives(all, { sort: "target_date" }).map((i) => i.id)).toEqual(["beta", "alpha", "gamma"]);
    expect(selectInitiatives(all, { sort: "progress" }).map((i) => i.id)).toEqual(["alpha", "beta", "gamma"]);
  });
});

describe("initiatives widget", () => {
  it("viewer sees the cards but no create button", async () => {
    mount(<InitiativesWidget />, { role: "viewer" });
    expect(await screen.findByText("Alpha")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /initiatives.page.newInitiative|New Initiative/i })).toBeNull();
  });

  it("a role still held from the previous workspace shows no create button", async () => {
    mount(<InitiativesWidget />, { role: "admin" });
    await screen.findByText("Alpha");
    expect(screen.getByRole("button", { name: /newInitiative|New Initiative/i })).toBeTruthy();
    cleanup();
    mount(<InitiativesWidget />, { role: "admin" });
    useMemberStore.setState({ myRoleWorkspaceId: "ws-old" });
    await screen.findByText("Alpha");
    expect(screen.queryByRole("button", { name: /newInitiative|New Initiative/i })).toBeNull();
  });

  it("member creates an initiative and lands on its page by address", async () => {
    mount(<InitiativesWidget />, { role: "member" });
    await screen.findByText("Alpha");
    fireEvent.click(screen.getByRole("button", { name: /newInitiative|New Initiative/i }));
    fireEvent.change(screen.getByLabelText(/form.name|^Name$/i), { target: { value: "Launch" } });
    fireEvent.submit(screen.getByLabelText(/form.name|^Name$/i).closest("form")!);
    await waitFor(() => expect(screen.getByTestId("where").textContent).toBe("/w/acme/initiatives/fresh"));
    expect(vi.mocked(api)).toHaveBeenCalledWith(
      "/api/v1/workspaces/ws1/initiatives",
      expect.objectContaining({ method: "POST", body: expect.objectContaining({ name: "Launch" }) }),
    );
  });

  it("cards are real links to w/:ws/initiatives/:id", async () => {
    mount(<InitiativesWidget settings={{ view: "list", statuses: ["active"] }} />);
    const link = (await screen.findByText("Alpha")).closest("a")!;
    expect(link.getAttribute("href")).toBe("/w/acme/initiatives/alpha");
    expect(within(link).getByText("About alpha")).toBeTruthy();
    expect(screen.queryByText("Beta")).toBeNull();
  });

  it("an empty filter says why, a failed load offers retry", async () => {
    mount(<InitiativesWidget settings={{ statuses: ["completed"] }} />);
    expect(await screen.findByText(/noneWithStatus|No initiatives with the selected statuses/)).toBeTruthy();
    cleanup();
    vi.mocked(api).mockRejectedValue(new Error("down"));
    mount(<InitiativesWidget />);
    expect(await screen.findByRole("alert")).toBeTruthy();
  });
});

describe("initiative widget", () => {
  it("asks for a choice when none is set", () => {
    mount(<InitiativeWidget />);
    expect(screen.getByText(/pickOne|Choose an initiative/)).toBeTruthy();
  });

  it("viewer sees linked projects without link or unlink; member gets both", async () => {
    mount(<InitiativeWidget settings={{ initiativeId: "alpha" }} />, { role: "viewer" });
    expect(await screen.findByText("Core")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /unlink|Unlink/i })).toBeNull();
    expect(screen.queryByRole("combobox")).toBeNull();
    cleanup();
    mount(<InitiativeWidget settings={{ initiativeId: "alpha" }} />, { role: "member" });
    await screen.findByText("Core");
    expect(screen.getByRole("button", { name: /unlink|Unlink/i })).toBeTruthy();
    expect(within(screen.getByRole("combobox")).getByText("Ops")).toBeTruthy();
  });

  it("a linked project shows its own icon; without one, the FolderKanban default", async () => {
    const served = vi.mocked(api).getMockImplementation()!;
    vi.mocked(api).mockImplementation(async (url: string, opts?: { method?: string; body?: unknown }) =>
      url === "/api/v1/initiatives/alpha"
        ? { ...DETAIL, linked_projects: [
            { id: "p1", name: "Core", slug: "core", description: "", icon: "" },
            { id: "p2", name: "Rocket", slug: "rocket", description: "", icon: "🚀" },
          ] }
        : served(url, opts));
    mount(<InitiativeWidget settings={{ initiativeId: "alpha" }} />, { role: "viewer" });
    const row = (name: string) => screen.getByText(name).closest("li")!;
    await screen.findByText("Core");
    expect(row("Core").querySelector("svg.lucide-folder-kanban")).not.toBeNull();
    expect(within(row("Rocket")).getByRole("img", { name: "🚀" })).toBeTruthy();
    expect(row("Rocket").querySelector("svg.lucide-folder-kanban")).toBeNull();
  });
});

describe("initiative page w/:ws/initiatives/:id", () => {
  it("the same route without an id opens «New dashboard» with the Initiatives template, with an id the page", async () => {
    mount(null, { at: "/w/acme/initiatives" });
    await waitFor(() => expect(screen.getByTestId("where").textContent).toBe("/w/acme/dashboard?new-dashboard=initiatives"));
    expect(screen.queryByText("Alpha")).toBeNull();
  });

  it("opens by direct link; member edits but cannot delete", async () => {
    mount(null, { role: "member", at: "/w/acme/initiatives/alpha" });
    expect(await screen.findByRole("heading", { name: "Alpha" })).toBeTruthy();
    expect(screen.getByRole("button", { name: /detail.edit|^Edit$/i })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /detail.delete|Delete initiative/i })).toBeNull();
  });

  it("admin sees delete; viewer sees neither edit nor delete", async () => {
    mount(null, { role: "admin", at: "/w/acme/initiatives/alpha" });
    await screen.findByRole("heading", { name: "Alpha" });
    expect(screen.getByRole("button", { name: /detail.delete|Delete initiative/i })).toBeTruthy();
    cleanup();
    mount(null, { role: "viewer", at: "/w/acme/initiatives/alpha" });
    await screen.findByRole("heading", { name: "Alpha" });
    expect(screen.queryByRole("button", { name: /detail.edit|^Edit$/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /detail.delete|Delete initiative/i })).toBeNull();
  });

  it("an unknown or forbidden id shows a message with retry, not a blank page", async () => {
    mount(null, { at: "/w/acme/initiatives/missing" });
    expect(await screen.findByRole("alert")).toBeTruthy();
  });
});
