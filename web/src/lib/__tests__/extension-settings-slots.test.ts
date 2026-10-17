import { beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), api }));

import { deleteProjectExtension, disconnectWorkspaceExtension, setWorkspaceExtensionEnabled } from "@/lib/extension-settings";
import { setExtensionAllowed } from "@/lib/extension-instance";
import { resetSlotRegistryForTests } from "@/lib/extension-slots";
import { useWorkspaceStore } from "@/stores/workspace";

const registry = "/api/v1/workspaces/ws-1/extensions";

describe("extension state changes refresh the slot registry", () => {
  beforeEach(() => {
    api.mockReset();
    api.mockImplementation(async (path: string) => (path === registry ? { extensions: [] } : {}));
    resetSlotRegistryForTests();
    useWorkspaceStore.setState({ currentWorkspace: { id: "ws-1" } as never });
  });

  const refetched = () => api.mock.calls.some((c) => c[0] === registry);

  it("after disable and enable", async () => {
    await setWorkspaceExtensionEnabled("ws-1", "gitlab", false, true);
    expect(refetched()).toBe(true);
    api.mockClear();
    resetSlotRegistryForTests();
    await setWorkspaceExtensionEnabled("ws-1", "gitlab", true);
    expect(refetched()).toBe(true);
  });

  it("after disconnect, project unbind and instance allow-list change", async () => {
    await disconnectWorkspaceExtension("ws-1", "gitlab", "keep");
    expect(refetched()).toBe(true);
    api.mockClear();
    resetSlotRegistryForTests();
    await deleteProjectExtension("p-1", "gitlab");
    expect(refetched()).toBe(true);
    api.mockClear();
    resetSlotRegistryForTests();
    await setExtensionAllowed("gitlab", false);
    expect(refetched()).toBe(true);
  });

  it("not after a refused disable", async () => {
    api.mockRejectedValue(new Error("409"));
    await expect(setWorkspaceExtensionEnabled("ws-1", "gitlab", false)).rejects.toThrow("409");
    expect(refetched()).toBe(false);
  });
});
