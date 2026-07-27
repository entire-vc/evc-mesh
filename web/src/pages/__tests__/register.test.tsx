import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { RegisterPage } from "@/pages/register";
import { useAuthStore } from "@/stores/auth";
import { api } from "@/lib/api";

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: vi.fn() };
});

const mockApi = vi.mocked(api);

function renderRegister() {
  return render(
    <MemoryRouter>
      <RegisterPage />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockApi.mockReset();
  useAuthStore.setState({ user: null, isAuthenticated: false, isLoading: false });
});

describe("RegisterPage — closed-registration guard", () => {
  it("renders the signup form when registration is open", async () => {
    mockApi.mockResolvedValueOnce({ registration_enabled: true });
    renderRegister();

    await waitFor(() =>
      expect(screen.getByRole("button", { name: /Create account/i })).toBeInTheDocument(),
    );
  });

  it("shows a closed-registration notice instead of the form when closed", async () => {
    mockApi.mockResolvedValueOnce({ registration_enabled: false });
    renderRegister();

    await waitFor(() =>
      expect(screen.getByText(/Registration is closed/i)).toBeInTheDocument(),
    );
    expect(screen.queryByRole("button", { name: /Create account/i })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Back to sign in/i })).toHaveAttribute(
      "href",
      "/login",
    );
  });
});
