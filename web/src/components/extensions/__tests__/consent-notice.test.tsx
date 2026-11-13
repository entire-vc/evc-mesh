import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { ConsentNotice } from "@/components/extensions/consent-notice";
import type { CatalogEntry, ConnectionDto } from "@/hooks/useExtensionCatalog";
import { api } from "@/lib/api";
import { toast } from "@/components/ui/toast";

vi.mock("@/lib/api", async () => ({ ...(await vi.importActual("@/lib/api")), api: vi.fn() }));
vi.mock("@/lib/extension-slots", () => ({ refreshSlotRegistryAfterChange: vi.fn() }));
vi.mock("@/components/ui/toast", () => ({ toast: { error: vi.fn(), success: vi.fn() }, Toaster: () => null }));

const pendingConn: ConnectionDto = {
  consented_at: "2026-10-14T17:00:00Z", consented_version: "0.3.0", consented_commit: "aaa111",
  needs_consent: true,
  pending_consent: { version: "0.4.0", commit: "bbb222", added_capabilities: ["api:docs:read"], added_events: ["task.created"] },
};
const entry = (connection: ConnectionDto | null): CatalogEntry =>
  ({ id: "memory-service", name: "Memory", version: "0.4.0", connection } as unknown as CatalogEntry);

const renderNotice = (connection: ConnectionDto | null, canManage = true) => {
  const onChanged = vi.fn();
  render(<ConsentNotice entry={entry(connection)} workspaceId="ws1" canManage={canManage} onChanged={onChanged} />);
  return onChanged;
};

beforeEach(() => { vi.mocked(api).mockReset(); vi.mocked(toast.success).mockReset(); });

describe("ConsentNotice", () => {
  it("renders nothing when nothing is pending", () => {
    renderNotice({ consented_version: "0.4.0", needs_consent: false, pending_consent: null });
    expect(screen.queryByTestId("consent-notice-memory-service")).toBeNull();
  });

  it("renders nothing for a connection the server gave no consent fields for", () => {
    renderNotice({ settings: {} });
    expect(screen.queryByTestId("consent-notice-memory-service")).toBeNull();
  });

  it("names the version and lists each added right", () => {
    renderNotice(pendingConn);
    const notice = screen.getByTestId("consent-notice-memory-service");
    expect(notice).toHaveTextContent("0.4.0");
    const items = screen.getAllByRole("listitem");
    expect(items).toHaveLength(2);
    expect(notice).toHaveTextContent("task.created");
    expect(screen.getByRole("button", { name: /0\.4\.0/ })).toBeEnabled();
  });

  it("agrees with exactly the version and commit it shows, then asks the list to refresh", async () => {
    vi.mocked(api).mockResolvedValue({} as never);
    const onChanged = renderNotice(pendingConn);
    fireEvent.click(screen.getByRole("button", { name: /0\.4\.0/ }));
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1));
    expect(api).toHaveBeenCalledWith("/api/v1/workspaces/ws1/extensions/memory-service/consent", {
      method: "POST", body: { version: "0.4.0", commit: "bbb222" },
    });
    expect(toast.success).toHaveBeenCalled();
  });

  it("on 409 version_changed shows the conflict, refreshes and agrees to nothing", async () => {
    vi.mocked(api).mockRejectedValue(Object.assign(new Error("conflict"), { status: 409, code: "conflict", errorCode: "version_changed" }));
    const onChanged = renderNotice(pendingConn);
    fireEvent.click(screen.getByRole("button", { name: /0\.4\.0/ }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/changed/i);
    expect(onChanged).toHaveBeenCalledTimes(1);
    expect(toast.success).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: /0\.4\.0/ })).toBeEnabled();
  });

  it("on another failure shows a retryable error and does not refresh", async () => {
    vi.mocked(api).mockRejectedValue(Object.assign(new Error("boom"), { status: 500, code: "internal" }));
    const onChanged = renderNotice(pendingConn);
    fireEvent.click(screen.getByRole("button", { name: /0\.4\.0/ }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/try again/i);
    expect(onChanged).not.toHaveBeenCalled();
  });

  it("shows a viewer the notice and the rights, without the action", () => {
    renderNotice(pendingConn, false);
    expect(screen.getByTestId("consent-notice-memory-service")).toHaveTextContent("0.4.0");
    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.getByText(/only an owner or admin/i)).toBeInTheDocument();
  });

  it("an older consent with empty version and commit is still a consent, not 'no consent'", () => {
    renderNotice({ ...pendingConn, consented_version: "", consented_commit: "" });
    const notice = screen.getByTestId("consent-notice-memory-service");
    expect(notice).toHaveTextContent(/has not agreed to/i);
    expect(notice).not.toHaveTextContent(/no consent is recorded/i);
  });

  it("a token with an inherited-property kind is skipped instead of crashing the screen", () => {
    renderNotice({ ...pendingConn, pending_consent: { ...pendingConn.pending_consent!, added_capabilities: ["toString:x", "__proto__:y", "api:docs:read"], added_events: [] } });
    expect(screen.getAllByRole("listitem")).toHaveLength(1);
  });

  it("a connection with no consent record says so instead of 'more permissions'", () => {
    renderNotice({ needs_consent: true, pending_consent: { ...pendingConn.pending_consent!, added_capabilities: ["api:docs:read", "api:tasks:read"] } });
    expect(screen.getByTestId("consent-notice-memory-service")).toHaveTextContent(/no consent is recorded/i);
  });
});
