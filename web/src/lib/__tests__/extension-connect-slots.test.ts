import { beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), api }));

import { connectWorkspaceExtension } from "@/lib/extension-connect";
import { resetSlotRegistryForTests } from "@/lib/extension-slots";
import { useWorkspaceStore } from "@/stores/workspace";

describe("connectWorkspaceExtension and the slot registry", () => {
  beforeEach(() => {
    api.mockReset();
    resetSlotRegistryForTests();
    useWorkspaceStore.setState({ currentWorkspace: { id: "ws-1" } as never });
  });

  it("refetches the workspace registry after a successful connect, not after the TTL", async () => {
    api.mockImplementation(async (path: string) => (path.endsWith("/connect") ? { ok: true } : { extensions: [] }));
    await connectWorkspaceExtension("ws-1", "gitlab", {}, ["ui:task.panel"]);
    const paths = api.mock.calls.map((c) => c[0]);
    expect(paths).toContain("/api/v1/workspaces/ws-1/extensions/gitlab/connect");
    expect(paths).toContain("/api/v1/workspaces/ws-1/extensions");
  });

  it("does not refetch when the connect request is refused", async () => {
    api.mockRejectedValue(new Error("400"));
    await expect(connectWorkspaceExtension("ws-1", "gitlab", {}, [])).rejects.toThrow("400");
    expect(api.mock.calls.map((c) => c[0])).toEqual(["/api/v1/workspaces/ws-1/extensions/gitlab/connect"]);
  });
});
