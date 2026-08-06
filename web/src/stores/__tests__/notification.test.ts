import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api", () => ({
  api: vi.fn(),
}));
import { api } from "@/lib/api";
import { useNotificationStore } from "@/stores/notification";

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;

beforeEach(() => {
  mockedApi.mockReset();
  useNotificationStore.setState({ notifications: [], unreadCount: 0, preferences: [] });
});
afterEach(() => vi.clearAllMocks());

// Regression coverage for Mesh task 501608d1: these stores used to pass
// JSON.stringify(req) as `body`, and api() itself JSON.stringify()s body —
// double-encoding it into a string the server's c.Bind() rejected with 400.
describe("notification store body encoding", () => {
  it("markAsRead passes a raw object body", async () => {
    mockedApi.mockResolvedValue(undefined);
    await useNotificationStore.getState().markAsRead(["n1"]);

    expect(mockedApi).toHaveBeenCalledWith(
      "/api/v1/notifications/mark-read",
      expect.objectContaining({ body: { ids: ["n1"] } }),
    );
  });

  it("markAllAsRead passes a raw object body", async () => {
    mockedApi.mockResolvedValue(undefined);
    await useNotificationStore.getState().markAllAsRead();

    expect(mockedApi).toHaveBeenCalledWith(
      "/api/v1/notifications/mark-read",
      expect.objectContaining({ body: { mark_all: true } }),
    );
  });

  it("updatePreferences passes a raw object body", async () => {
    const req = {
      workspace_id: "ws1",
      channel: "web_push" as const,
      events: ["task.reviewer_assigned"],
      is_enabled: true,
    };
    mockedApi.mockResolvedValue({ id: "p1", ...req });

    await useNotificationStore.getState().updatePreferences(req);

    expect(mockedApi).toHaveBeenCalledWith(
      "/api/v1/notifications/preferences",
      expect.objectContaining({ body: req }),
    );
  });

  it("updatePreferences propagates a save failure instead of swallowing it", async () => {
    mockedApi.mockRejectedValue(new Error("400 invalid request body"));

    await expect(
      useNotificationStore.getState().updatePreferences({
        workspace_id: "ws1",
        channel: "web_push",
        events: [],
        is_enabled: true,
      }),
    ).rejects.toThrow("400 invalid request body");
  });
});
