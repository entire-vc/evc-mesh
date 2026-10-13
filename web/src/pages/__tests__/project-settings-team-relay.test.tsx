import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { ProjectSettingsPage } from "@/pages/project-settings";
import { useProjectStore } from "@/stores/project";
import { api } from "@/lib/api";
import type { Project } from "@/types";

vi.mock("@/lib/api", async () => ({ ...await vi.importActual("@/lib/api"), api: vi.fn() }));

const trConfig = {
  enabled: true, share_slug: "mesh-dev-share", agent_key_hint: "••••abcd",
  subfolder: "Mesh/Mesh dev", include_project_slug: true,
};
const TR_PATH = "/api/v1/projects/project-1/integrations/team-relay";

function show() {
  return render(<MemoryRouter initialEntries={["/w/ws/p/project/settings?tab=integrations"]}>
    <Routes><Route path="/w/:wsSlug/p/:projectSlug/settings" element={<ProjectSettingsPage />} /></Routes>
  </MemoryRouter>);
}
const patchBody = (n: number) => (patchCalls()[n]![1] as { body: Record<string, unknown> }).body;
const patchCalls = () => vi.mocked(api).mock.calls.filter(([, opts]) => (opts as { method?: string } | undefined)?.method === "PATCH");

describe("project settings Team Relay form", () => {
  beforeEach(() => {
    vi.mocked(api).mockReset();
    vi.mocked(api).mockImplementation(async (path: string) => {
      if (path === TR_PATH) return trConfig;
      if (/\/(statuses|custom-fields)$/.test(path)) return [];
      return { items: [], has_more: false };
    });
    HTMLElement.prototype.scrollIntoView = vi.fn();
    useProjectStore.setState({ statuses: [], currentProject: { id: "project-1", slug: "project", name: "Project", settings: {} } as Project });
  });

  it("disables password-manager autofill on the key field", async () => {
    show();
    const key = await screen.findByLabelText("Agent Key");
    expect(key).toHaveAttribute("autocomplete", "new-password");
    expect(key).toHaveValue("");
  });

  it("never sends agent_key when switching the integration off, even if the field got filled", async () => {
    show();
    const key = await screen.findByLabelText("Agent Key");
    fireEvent.change(key, { target: { value: "••••••••••••••" } }); // browser-filled value
    fireEvent.click(screen.getByLabelText("Enable Team Relay publishing"));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(patchCalls()).toHaveLength(1));
    const body = patchBody(0);
    expect(body.enabled).toBe(false);
    expect(body).not.toHaveProperty("agent_key");
  });

  it("never sends a masked autofill value while the integration stays enabled", async () => {
    show();
    const key = await screen.findByLabelText("Agent Key");
    fireEvent.change(key, { target: { value: "••••••••••••••" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(patchCalls()).toHaveLength(1));
    const body = patchBody(0);
    expect(body.enabled).toBe(true);
    expect(body).not.toHaveProperty("agent_key");
  });

  it("never sends a valid-shaped autofilled key that the user did not type", async () => {
    show();
    const key = await screen.findByLabelText("Agent Key");
    fireEvent.change(key, { target: { value: "tr_agent_" + "b".repeat(48) } }); // autofill: no key/paste event
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(patchCalls()).toHaveLength(1));
    expect(patchBody(0)).not.toHaveProperty("agent_key");
  });

  it("sends a pasted key", async () => {
    show();
    const key = await screen.findByLabelText("Agent Key");
    const fresh = "tr_agent_" + "c".repeat(48);
    fireEvent.paste(key);
    fireEvent.change(key, { target: { value: fresh } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(patchCalls()).toHaveLength(1));
    expect(patchBody(0).agent_key).toBe(fresh);
  });

  it("omits agent_key when blank and sends it only when a new key is typed", async () => {
    show();
    const key = await screen.findByLabelText("Agent Key");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(patchCalls()).toHaveLength(1));
    expect(patchBody(0)).not.toHaveProperty("agent_key");

    const fresh = "tr_agent_" + "a".repeat(48);
    const field = await screen.findByLabelText("Agent Key");
    fireEvent.keyDown(field, { key: "t" });
    fireEvent.change(field, { target: { value: fresh } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(patchCalls()).toHaveLength(2));
    expect(patchBody(1).agent_key).toBe(fresh);
    void key;
  });

  it("shows the server's reason instead of a generic failure", async () => {
    vi.mocked(api).mockImplementation(async (path: string, opts?: { method?: string }) => {
      if (path === TR_PATH && opts?.method === "PATCH") {
        throw Object.assign(new Error("Validation failed"), { code: "VALIDATION_ERROR", status: 400, details: { agent_key: "agent_key looks like the masked placeholder" } });
      }
      if (path === TR_PATH) return trConfig;
      if (/\/(statuses|custom-fields)$/.test(path)) return [];
      return { items: [], has_more: false };
    });
    show();
    await screen.findByLabelText("Agent Key");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText(/masked placeholder/)).toBeInTheDocument();
    expect(screen.queryByText("Failed to save settings")).not.toBeInTheDocument();
  });
});
