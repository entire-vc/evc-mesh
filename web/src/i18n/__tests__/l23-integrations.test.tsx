import { act, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api", () => ({ api: vi.fn() }));

import { api } from "@/lib/api";
import { IntegrationsPage } from "@/pages/integrations";
import i18n, { setLanguage } from "@/i18n";
import { useMemberStore } from "@/stores/member";
import { useWorkspaceStore } from "@/stores/workspace";
import type { Workspace } from "@/types";

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;

const WORKSPACE = {
  id: "ws1",
  name: "Acme",
  slug: "acme",
  owner_id: "u1",
  settings: {},
  billing_plan_id: "free",
  billing_customer_id: "",
  icon_url: null,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
} as Workspace;

beforeEach(() => {
  mockedApi.mockReset();
  mockedApi.mockImplementation((url: string) => {
    if (url === "/api/v1/workspaces/ws1/members/me") {
      return Promise.resolve({ role: "owner" });
    }
    if (url === "/api/v1/workspaces/ws1/integrations") {
      return Promise.resolve({
        integrations: [
          {
            id: "i1",
            workspace_id: "ws1",
            provider: "github",
            is_active: true,
            config: {},
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
        ],
      });
    }
    return Promise.resolve({});
  });
  useWorkspaceStore.setState({ currentWorkspace: WORKSPACE, workspaces: [WORKSPACE] });
  useMemberStore.setState({ myRole: "owner" });
});

afterEach(async () => {
  await i18n.changeLanguage("en");
});

function renderPage() {
  return render(
    <MemoryRouter>
      <IntegrationsPage />
    </MemoryRouter>,
  );
}

describe("L2.3 integrations: en by default, ru translated, no raw keys", () => {
  it("renders the intro and webhook help in en, then ru", async () => {
    const view = renderPage();
    const en = i18n.t("integrations:integrationsPage.intro");
    expect(en).not.toMatch(/[а-яё]/i);
    expect(await screen.findByText(en)).toBeTruthy();
    await waitFor(() => expect(document.body.textContent).toContain("MESH-<task-id>"));
    expect(document.body.textContent).toContain("Settings → Webhooks");
    view.unmount();

    await act(async () => {
      await setLanguage("ru");
    });
    renderPage();
    const ru = i18n.t("integrations:integrationsPage.intro");
    expect(ru).toMatch(/[а-яё]/i);
    expect(await screen.findByText(ru)).toBeTruthy();
    await waitFor(() => expect(document.body.textContent).toContain("MESH-<task-id>"));
    expect(document.body.textContent).toMatch(/Добавьте этот URL/);
    expect(document.body.textContent).not.toMatch(/integrationsPage\.[a-zA-Z]+/);
  });
});
