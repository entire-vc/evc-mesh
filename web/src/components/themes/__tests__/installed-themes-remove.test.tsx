import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: vi.fn() };
});

import { api, ApiRequestError } from "@/lib/api";
import { toast } from "@/components/ui/toast";
import { setLanguage } from "@/i18n";
import { InstalledThemes } from "../installed-themes";

vi.mock("@/components/ui/toast", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const apiMock = vi.mocked(api);

const theme = (id: string, name: string) => ({
  id,
  name,
  version: "1.0.0",
  publisher: "t",
  description: "",
  source: "builtin",
  trust_level: "builtin",
  modes: ["light"],
  has_css: false,
  has_views: false,
  settings: {},
  tokens: {},
  contrast_warnings: [],
});

/** Serves the list from `themes`; DELETE removes the row, or fails with `deleteError`. */
function routes(deleteError?: Error) {
  let themes = [theme("graphite", "Graphite"), theme("paper", "Paper")];
  const del = vi.fn();
  apiMock.mockImplementation(async (path: string, opts?: { method?: string }) => {
    if (opts?.method === "DELETE") {
      del(path);
      if (deleteError) throw deleteError;
      themes = themes.filter((x) => !path.endsWith(`/${x.id}`));
      return {};
    }
    return { default: "mesh-default", themes, builtin_available: [] };
  });
  return del;
}

beforeEach(async () => {
  apiMock.mockReset();
  await setLanguage("en");
});

describe("InstalledThemes remove", () => {
  it("confirm → DELETE → the theme leaves the list and the parent is told", async () => {
    const del = routes();
    const onChanged = vi.fn();
    render(<InstalledThemes onChanged={onChanged} />);
    fireEvent.click(await screen.findByRole("button", { name: "Remove Graphite" }));
    expect(await screen.findByText("Workspaces using it go back to the instance theme.")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(del).toHaveBeenCalledWith("/api/v1/instance/themes/graphite"));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Remove Graphite" })).toBeNull());
    expect(screen.getByRole("button", { name: "Remove Paper" })).toBeTruthy();
    expect(onChanged).toHaveBeenCalled();
  });

  it("cancel in the dialog deletes nothing", async () => {
    const del = routes();
    render(<InstalledThemes />);
    fireEvent.click(await screen.findByRole("button", { name: "Remove Graphite" }));
    fireEvent.click(await screen.findByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByText("Workspaces using it go back to the instance theme.")).toBeNull());
    expect(del).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Remove Graphite" })).toBeTruthy();
  });

  it("a failed DELETE keeps the theme in the list and shows the error", async () => {
    const del = routes(new Error("boom"));
    render(<InstalledThemes />);
    fireEvent.click(await screen.findByRole("button", { name: "Remove Graphite" }));
    fireEvent.click(await screen.findByRole("button", { name: "Remove" }));
    await waitFor(() => expect(del).toHaveBeenCalled());
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("boom"));
    expect(screen.getByRole("button", { name: "Remove Graphite" })).toBeTruthy();
  });

  it.each([
    [404, "not_found"],
    [500, "internal_error"],
  ])("an HTTP %i from DELETE keeps the theme and shows a toast", async (status, code) => {
    const del = routes(new ApiRequestError("Request failed", code, status));
    render(<InstalledThemes />);
    fireEvent.click(await screen.findByRole("button", { name: "Remove Graphite" }));
    fireEvent.click(await screen.findByRole("button", { name: "Remove" }));
    await waitFor(() => expect(del).toHaveBeenCalled());
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
    expect(screen.getByRole("button", { name: "Remove Graphite" })).toBeTruthy();
  });
});
