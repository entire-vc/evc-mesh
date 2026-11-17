import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { UninstallExtensionButton, canUninstall } from "@/components/extensions/uninstall-extension";
import { api } from "@/lib/api";
import { toast } from "@/components/ui/toast";
import { refreshSlotRegistryAfterChange } from "@/lib/extension-slots";

vi.mock("@/lib/api", async () => ({ ...(await vi.importActual("@/lib/api")), api: vi.fn() }));
vi.mock("@/lib/extension-slots", () => ({ refreshSlotRegistryAfterChange: vi.fn() }));
vi.mock("@/components/ui/toast", () => ({ toast: { error: vi.fn(), success: vi.fn() }, Toaster: () => null }));

type Entry = Parameters<typeof UninstallExtensionButton>[0]["entry"];
const repoExt: Entry = { id: "hello", name: "Hello", kind: "plugin", installed: true, source: "repo" };
const UNINSTALL = /^(uninstall|удалить)$/i;

const renderButton = (entry: Entry = repoExt, admin = true) => {
  const onUninstalled = vi.fn();
  render(<UninstallExtensionButton entry={entry} isInstanceAdmin={admin} onUninstalled={onUninstalled} />);
  return onUninstalled;
};

beforeEach(() => {
  vi.mocked(api).mockReset();
  vi.mocked(toast.success).mockReset();
  vi.mocked(toast.error).mockReset();
  vi.mocked(refreshSlotRegistryAfterChange).mockReset();
});

describe("canUninstall", () => {
  it("is for an instance admin and an installed catalog or repository extension only", () => {
    expect(canUninstall(repoExt, true)).toBe(true);
    expect(canUninstall({ ...repoExt, source: "catalog" }, true)).toBe(true);
    expect(canUninstall(repoExt, false)).toBe(false);
    expect(canUninstall({ ...repoExt, source: "builtin" }, true)).toBe(false);
    expect(canUninstall({ ...repoExt, installed: false }, true)).toBe(false);
    expect(canUninstall({ ...repoExt, id: "runner", source: "catalog" }, true)).toBe(false);
  });
});

describe("UninstallExtensionButton", () => {
  it("is not shown to a workspace member who is not the instance admin, nor for a built-in", () => {
    renderButton(repoExt, false);
    expect(screen.queryByRole("button")).toBeNull();
    renderButton({ ...repoExt, source: "builtin" });
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("asks for confirmation first; cancel removes nothing", async () => {
    const onUninstalled = renderButton();
    fireEvent.click(await screen.findByRole("button", { name: UNINSTALL }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent(/Hello/);
    expect(dialog).toHaveTextContent(/data is kept|данные расширения сохранятся/i);
    fireEvent.click(screen.getByRole("button", { name: /cancel|отмена/i }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(api).not.toHaveBeenCalled();
    expect(onUninstalled).not.toHaveBeenCalled();
  });

  it("confirm calls DELETE /extensions/:id, refreshes the slots and the page", async () => {
    vi.mocked(api).mockResolvedValue({ extension_id: "hello", data_kept: true } as never);
    const onUninstalled = renderButton();
    fireEvent.click(await screen.findByRole("button", { name: UNINSTALL }));
    const dialog = await screen.findByRole("dialog");
    const confirm = Array.from(dialog.querySelectorAll("button")).find((b) => UNINSTALL.test(b.textContent?.trim() ?? ""));
    expect(confirm).toBeDefined();
    fireEvent.click(confirm!);
    await waitFor(() => expect(onUninstalled).toHaveBeenCalledTimes(1));
    expect(api).toHaveBeenCalledWith("/api/v1/extensions/hello", { method: "DELETE" });
    expect(refreshSlotRegistryAfterChange).toHaveBeenCalled();
    expect(toast.success).toHaveBeenCalled();
  });

  it("a theme goes through the theme removal", async () => {
    vi.mocked(api).mockResolvedValue({} as never);
    const onUninstalled = renderButton({ ...repoExt, id: "dusk", name: "Dusk", kind: "theme" });
    fireEvent.click(await screen.findByRole("button", { name: UNINSTALL }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(Array.from(dialog.querySelectorAll("button")).find((b) => UNINSTALL.test(b.textContent?.trim() ?? ""))!);
    await waitFor(() => expect(onUninstalled).toHaveBeenCalledTimes(1));
    expect(api).toHaveBeenCalledWith("/api/v1/instance/themes/dusk", { method: "DELETE" });
  });

  it("a refusal (409) shows the server's reason and keeps the page as it is", async () => {
    vi.mocked(api).mockRejectedValue(Object.assign(new Error("a built-in extension cannot be uninstalled; turn it off instead"), { status: 409 }));
    const onUninstalled = renderButton();
    fireEvent.click(await screen.findByRole("button", { name: UNINSTALL }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(Array.from(dialog.querySelectorAll("button")).find((b) => UNINSTALL.test(b.textContent?.trim() ?? ""))!);
    await waitFor(() => expect(toast.error).toHaveBeenCalledTimes(1));
    expect(onUninstalled).not.toHaveBeenCalled();
  });
});
