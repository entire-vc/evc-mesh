import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: vi.fn().mockResolvedValue({ grants: [] }) };
});

import { ProfilePage } from "@/pages/profile";
import { useAuthStore } from "@/stores/auth";
import type { User } from "@/types";

const USER: User = {
  id: "u1",
  email: "alex@example.com",
  name: "Alex",
  avatar_url: "",
  is_active: true,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
  locale: "en",
};

const renderPage = () =>
  render(
    <MemoryRouter initialEntries={["/w/acme/me"]}>
      <Routes>
        <Route path="w/:wsSlug/me" element={<ProfilePage />} />
      </Routes>
    </MemoryRouter>,
  );

beforeEach(() => {
  localStorage.clear();
  document.documentElement.classList.remove("dark");
  useAuthStore.setState({ user: USER, isAuthenticated: true, isLoading: false });
});

afterEach(() => vi.restoreAllMocks());

describe("ProfilePage", () => {
  it("shows every section, including the My agents placeholder and the App access empty state", async () => {
    renderPage();
    for (const title of ["Appearance", "Language", "My agents", "App access"]) {
      expect(await screen.findByRole("heading", { name: title })).toBeInTheDocument();
    }
    expect(screen.getByText("Coming soon.")).toBeInTheDocument();
    expect(await screen.findByText(/No external apps have access yet/)).toBeInTheDocument();
  });

  it("switching the theme applies the dark class and persists it for the next load", () => {
    renderPage();
    const light = screen.getByRole("radio", { name: "Light" });
    expect(light).toHaveAttribute("aria-checked", "true");

    fireEvent.click(screen.getByRole("radio", { name: "Dark" }));

    expect(document.documentElement.classList.contains("dark")).toBe(true);
    expect(localStorage.getItem("theme")).toBe("dark");
    expect(screen.getByRole("radio", { name: "Dark" })).toHaveAttribute("aria-checked", "true");
  });

  it("changing the language calls setLocale('ru')", async () => {
    const setLocale = vi.fn().mockResolvedValue(undefined);
    useAuthStore.setState({ setLocale });
    renderPage();

    fireEvent.change(screen.getByLabelText("Language"), { target: { value: "ru" } });
    expect(setLocale).toHaveBeenCalledWith("ru");
  });
});
