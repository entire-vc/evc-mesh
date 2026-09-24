import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: vi.fn() };
});
vi.mock("@/components/ui/toast", () => ({
  toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }),
}));

import { api } from "@/lib/api";
import { toast } from "@/components/ui/toast";
import { ConnectedAppsPage } from "@/pages/connected-apps";
import type { OAuthGrant } from "@/types";

const mockApi = api as unknown as ReturnType<typeof vi.fn>;

const GRANT: OAuthGrant = {
  id: "g1",
  client_id: "https://claude.ai/meta",
  workspace_id: "w1",
  agent_id: "a1",
  scope: "mesh",
  created_at: "2026-09-01T10:00:00Z",
  client_name: "Claude Code",
  agent_name: "Claude Code — ada",
  workspace: { id: "w1", name: "Acme", slug: "acme" },
};
const REVOKED: OAuthGrant = {
  ...GRANT,
  id: "g2",
  client_name: "Old Client",
  revoked_at: "2026-09-10T10:00:00Z",
};

const renderPage = () =>
  render(
    <MemoryRouter>
      <ConnectedAppsPage />
    </MemoryRouter>,
  );

beforeEach(() => {
  mockApi.mockReset();
  vi.mocked(toast.success).mockReset();
  vi.mocked(toast.error).mockReset();
});

describe("ConnectedAppsPage", () => {
  it("lists active grants with a Revoke button and revoked ones without", async () => {
    mockApi.mockResolvedValueOnce({ grants: [GRANT, REVOKED] });
    renderPage();

    expect(await screen.findByText("Claude Code")).toBeInTheDocument();
    expect(screen.getByText("Old Client")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Revoke access" })).toHaveLength(1);
    expect(screen.getByText("Revoked", { selector: "span" })).toBeInTheDocument();
  });

  it("shows an empty state when the API returns no grants (null-safe)", async () => {
    mockApi.mockResolvedValueOnce({ grants: null });
    renderPage();
    expect(await screen.findByText("No apps are connected.")).toBeInTheDocument();
  });

  it("revokes only after confirmation, then reloads the list", async () => {
    mockApi.mockResolvedValueOnce({ grants: [GRANT] });
    mockApi.mockResolvedValueOnce(undefined); // DELETE
    mockApi.mockResolvedValueOnce({ grants: [{ ...GRANT, revoked_at: "2026-09-24T10:00:00Z" }] });
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "Revoke access" }));
    expect(mockApi).toHaveBeenCalledTimes(1); // nothing sent yet

    expect(await screen.findByText(/will stop working in Acme immediately/)).toBeInTheDocument();
    fireEvent.click(screen.getAllByRole("button", { name: "Revoke access" }).slice(-1)[0]!);

    await waitFor(() =>
      expect(mockApi).toHaveBeenCalledWith("/api/v1/oauth/grants/g1", { method: "DELETE" }),
    );
    await waitFor(() => expect(screen.getByText("No apps are connected.")).toBeInTheDocument());
    expect(toast.success).toHaveBeenCalled();
  });

  it("Cancel sends nothing", async () => {
    mockApi.mockResolvedValueOnce({ grants: [GRANT] });
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "Revoke access" }));
    fireEvent.click(await screen.findByRole("button", { name: "Cancel" }));

    expect(mockApi).toHaveBeenCalledTimes(1); // only the list load
    expect(screen.getAllByRole("button", { name: "Revoke access" })).toHaveLength(1);
  });

  it("shows the app as revoked even if the reload after a successful revoke fails", async () => {
    mockApi.mockResolvedValueOnce({ grants: [GRANT] });
    mockApi.mockResolvedValueOnce(undefined); // DELETE ok
    mockApi.mockRejectedValueOnce(new Error("reload failed"));
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "Revoke access" }));
    await screen.findByText(/will stop working in Acme immediately/);
    fireEvent.click(screen.getAllByRole("button", { name: "Revoke access" }).slice(-1)[0]!);

    await waitFor(() => expect(screen.getByText("Revoked", { selector: "span" })).toBeInTheDocument());
    expect(screen.queryByRole("button", { name: "Revoke access" })).not.toBeInTheDocument();
  });

  it("reports a failed revoke and keeps the app listed", async () => {
    mockApi.mockResolvedValueOnce({ grants: [GRANT] });
    mockApi.mockRejectedValueOnce(new Error("boom"));
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "Revoke access" }));
    await screen.findByText(/will stop working in Acme immediately/);
    fireEvent.click(screen.getAllByRole("button", { name: "Revoke access" }).slice(-1)[0]!);

    await waitFor(() => expect(toast.error).toHaveBeenCalled());
    expect(screen.getAllByText("Claude Code").length).toBeGreaterThan(0);
  });
});
