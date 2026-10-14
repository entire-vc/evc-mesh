import { act, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n, { setLanguage } from "@/i18n";
import { LoginPage } from "@/pages/login";
import { AcceptInvitePage } from "@/pages/accept-invite";
import { useAuthStore } from "@/stores/auth";
import { api } from "@/lib/api";

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: vi.fn() };
});
const mockApi = vi.mocked(api);

const RAW_KEY = /\b(auth|invite|errors)\.[a-zA-Z]+\.?[a-zA-Z]*/;

beforeEach(() => {
  mockApi.mockReset();
  useAuthStore.setState({ user: null, isAuthenticated: false, isLoading: false });
});
afterEach(async () => {
  await i18n.changeLanguage("en");
});

describe("L2.3 auth pages: en by default, ru translated, no raw keys", () => {
  it("login renders in en, then ru", async () => {
    mockApi.mockResolvedValue({ registration_enabled: true });
    const view = render(<MemoryRouter><LoginPage /></MemoryRouter>);
    expect(await screen.findByText("Welcome back")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sign in" })).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "Register" })).toBeInTheDocument();
    view.unmount();

    await act(async () => {
      await setLanguage("ru");
    });
    render(<MemoryRouter><LoginPage /></MemoryRouter>);
    expect(await screen.findByText("С возвращением")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Войти" })).toBeInTheDocument();
    expect(screen.getByPlaceholderText("Ваш пароль")).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(RAW_KEY);
    expect(document.body.textContent).not.toMatch(/Welcome back|Sign in/);
  });

  it("accept-invite renders role and email inside the translated sentence", async () => {
    mockApi.mockResolvedValue({ id: "i1", workspace_id: "w1", email: "ann@example.com", role: "admin", expires_at: "" });
    await act(async () => {
      await setLanguage("ru");
    });
    render(
      <MemoryRouter initialEntries={["/accept-invite/tok"]}>
        <Routes>
          <Route path="/accept-invite/:token" element={<AcceptInvitePage />} />
        </Routes>
      </MemoryRouter>,
    );
    expect(await screen.findByRole("heading", { name: "Принять приглашение" })).toBeInTheDocument();
    expect(screen.getByText("admin").tagName).toBe("STRONG");
    expect(screen.getByText("ann@example.com")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Принять и присоединиться" })).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(RAW_KEY);
  });
});
