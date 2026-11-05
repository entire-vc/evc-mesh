import { beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.fn();
vi.mock("@/lib/api", () => ({ api: (...a: unknown[]) => api(...a) }));

import { getOperatorAgents, profileLabel, receivingProfiles } from "@/lib/my-runner";

const OWNER = "owner-ws";
const RECV = "recv-ws";
const ref = { resource_owner_workspace_id: OWNER, binding_ref: "shared-support" };
const profile = { harness: "claude_code", execution_mode: "subscription", account_ref: "claude/partner-claude", model: "claude-sonnet-5-5", enabled: true };
const view = (over: Record<string, unknown> = {}) => ({
  enabled: true, admission: { enabled: true, permitted_profiles: ["partner-claude"] }, profiles: { "partner-claude": profile }, ...over,
});
const t = (k: string) => ({ "profiles.mode.subscription": "subscription", "profiles.defaultModel": "default model" })[k] ?? k;

beforeEach(() => api.mockReset());

describe("receivingProfiles", () => {
  it("returns the permitted profile of a valid admission, read from the owner-scoped binding route", async () => {
    api.mockResolvedValueOnce(view());
    const got = await receivingProfiles(RECV, ref);
    expect(api).toHaveBeenCalledWith(`/api/v1/workspaces/${RECV}/runtime/bindings/shared-support?resource_owner_workspace_id=${OWNER}`);
    expect(Object.keys(got ?? {})).toEqual(["partner-claude"]);
    expect(profileLabel("partner-claude", { profiles: got } as never, t)).toBe("Claude Code · subscription partner-claude · Sonnet 5.5");
  });

  it("encodes every URL part it builds", async () => {
    api.mockResolvedValueOnce(view());
    await receivingProfiles("w/s?x", { resource_owner_workspace_id: "o&w", binding_ref: "a/b" });
    expect(api).toHaveBeenCalledWith("/api/v1/workspaces/w%2Fs%3Fx/runtime/bindings/a%2Fb?resource_owner_workspace_id=o%26w");
  });

  it.each([
    ["binding disabled / revoked", view({ enabled: false })],
    ["admission disabled", view({ admission: { enabled: false, permitted_profiles: ["partner-claude"] } })],
    ["nothing permitted", view({ admission: { enabled: true, permitted_profiles: [] } })],
    ["permitted profile not described", view({ admission: { enabled: true, permitted_profiles: ["other"] } })],
  ])("gives no profile: %s", async (_n, v) => {
    api.mockResolvedValueOnce(v);
    expect(await receivingProfiles(RECV, ref)).toEqual({});
  });

  it("gives no profile when the route refuses (grant revoked, not an admin)", async () => {
    api.mockRejectedValueOnce({ status: 403 });
    expect(await receivingProfiles(RECV, ref)).toEqual({});
  });
});

describe("getOperatorAgents", () => {
  it("resolves profiles only for agents with a receiving ref; an ordinary agent is untouched", async () => {
    api.mockResolvedValueOnce({ agents: [
      { agent_id: "a2", name: "own", receiving: null },
      { agent_id: "shared", name: "Shared Support", receiving: ref },
    ] });
    api.mockResolvedValueOnce(view());
    const [own, shared] = await getOperatorAgents(RECV);
    expect(api).toHaveBeenCalledTimes(2);
    expect(own!.receivingProfiles).toBeUndefined();
    expect(Object.keys(shared!.receivingProfiles ?? {})).toEqual(["partner-claude"]);
  });
});
