import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { LoginPage } from "@/pages/login";
import { useAuthStore } from "@/stores/auth";
import { api } from "@/lib/api";

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: vi.fn() };
});

const mockApi = vi.mocked(api);

function renderLogin() {
  return render(
    <MemoryRouter>
      <LoginPage />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockApi.mockReset();
  useAuthStore.setState({ user: null, isAuthenticated: false, isLoading: false });
});

describe("LoginPage — registration link visibility", () => {
  it("shows the Register link when the instance reports registration_enabled: true", async () => {
    mockApi.mockResolvedValueOnce({ registration_enabled: true });
    renderLogin();

    await waitFor(() =>
      expect(screen.getByRole("link", { name: "Register" })).toBeInTheDocument(),
    );
  });

  it("hides the Register link when the instance reports registration_enabled: false", async () => {
    mockApi.mockResolvedValueOnce({ registration_enabled: false });
    renderLogin();

    await waitFor(() => expect(mockApi).toHaveBeenCalledWith("/api/v1/auth/config", { noAuth: true }));
    await waitFor(() =>
      expect(screen.queryByRole("link", { name: "Register" })).not.toBeInTheDocument(),
    );
  });

  it("fails open (shows the link) if the config request errors", async () => {
    mockApi.mockRejectedValueOnce(new Error("network error"));
    renderLogin();

    await waitFor(() =>
      expect(screen.getByRole("link", { name: "Register" })).toBeInTheDocument(),
    );
  });
});
