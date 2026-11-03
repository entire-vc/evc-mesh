import { act, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { Sidebar } from "../sidebar";
import { setLanguage } from "@/i18n";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";
import { useAuthStore } from "@/stores/auth";
import { useCapabilitiesStore } from "@/stores/capabilities";

const workspace = { id: "ws1", slug: "acme", name: "Acme" } as never;

function stubFetch(instanceSpark: boolean, effective: unknown[], integrations: unknown[] = []) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      const body = String(url).includes("/integrations")
        ? { integrations, effective }
        : String(url).includes("version")
          ? { spark_enabled: instanceSpark }
          : {};
      return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    }),
  );
}

const sparkEffective = (effective_enabled: boolean) => [{ provider: "spark", effective_enabled }];

const integrationsFetched = () =>
  vi.mocked(fetch).mock.calls.some(([u]) => String(u).includes("/integrations"));

beforeEach(async () => {
  await setLanguage("en");
  useAuthStore.setState({ user: null });
  useWorkspaceStore.setState({ workspaces: [workspace], currentWorkspace: workspace, isLoading: false });
  useProjectStore.setState({ projects: [], currentProject: null, isLoading: false, error: null });
  useCapabilitiesStore.setState({ sparkEnabled: true, sparkUrl: "", fetched: false });
});
afterEach(() => vi.unstubAllGlobals());

const renderSidebar = () =>
  render(
    <MemoryRouter initialEntries={["/w/acme/dashboard"]}>
      <Routes>
        <Route path="/w/:wsSlug/*" element={<Sidebar collapsed={false} />} />
      </Routes>
    </MemoryRouter>,
  );

describe("Sidebar — Spark link follows the effective workspace availability", () => {
  it("shows Spark when it is effective for the workspace although the instance flag is off", async () => {
    stubFetch(false, sparkEffective(true));
    renderSidebar();
    await waitFor(() => expect(useCapabilitiesStore.getState().sparkEnabled).toBe(false));
    await waitFor(() => expect(integrationsFetched()).toBe(true));
    await new Promise((r) => setTimeout(r, 100));
    expect(await screen.findByRole("link", { name: /Spark/ })).toHaveAttribute("href", "/w/acme/spark");
  });

  it("hides Spark when it is not effective for the workspace although the instance flag is on", async () => {
    stubFetch(true, sparkEffective(false), [{ provider: "spark", is_active: false }]);
    renderSidebar();
    await waitFor(() => expect(useCapabilitiesStore.getState().fetched).toBe(true));
    await waitFor(() => expect(screen.queryByRole("link", { name: /Spark/ })).not.toBeInTheDocument());
  });

  it("hides Spark for an active placeholder row the server reports as not effective", async () => {
    stubFetch(false, sparkEffective(false), [{ provider: "spark", is_active: true }]);
    renderSidebar();
    await waitFor(() => expect(useCapabilitiesStore.getState().fetched).toBe(true));
    await waitFor(() => expect(screen.queryByRole("link", { name: /Spark/ })).not.toBeInTheDocument());
  });

  it("hides Spark when neither the instance nor the workspace has it", async () => {
    stubFetch(false, []);
    renderSidebar();
    await waitFor(() => expect(useCapabilitiesStore.getState().fetched).toBe(true));
    await waitFor(() => expect(screen.queryByRole("link", { name: /Spark/ })).not.toBeInTheDocument());
  });

  it("falls back to the instance flag when the workspace is deselected", async () => {
    stubFetch(true, sparkEffective(false));
    renderSidebar();
    await waitFor(() => expect(integrationsFetched()).toBe(true));
    await waitFor(() => expect(screen.queryByRole("link", { name: /Spark/ })).not.toBeInTheDocument());
    act(() => useWorkspaceStore.setState({ currentWorkspace: null }));
    expect(await screen.findByRole("link", { name: /Spark/ })).toBeInTheDocument();
  });
});
