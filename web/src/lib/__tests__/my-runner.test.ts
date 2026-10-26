import { describe, expect, it } from "vitest";
import { agentState, entrySummary, findSecretViolation, parseEntryValue, parseLimit, sectionEntries, withEntry, type OperatorAgent } from "@/lib/my-runner";

describe("parseLimit", () => {
  it.each([["0", 0], [" 12 ", 12], ["1000", 1000]])("accepts %j", (text, n) => expect(parseLimit(text)).toBe(n));
  it.each(["", "-1", "1001", "1.5", "abc", "12345"])("refuses %j", (text) => expect(parseLimit(text)).toBeNull());
});

describe("parseEntryValue", () => {
  it("accepts an object", () => expect(parseEntryValue('{"a":1}')).toEqual({ ok: true, value: { a: 1 } }));
  it.each(["[]", "1", "null", "{", ""])("refuses %j", (text) => expect(parseEntryValue(text).ok).toBe(false));
});

describe("config helpers", () => {
  it("sorts entries and tolerates a missing document", () => {
    expect(sectionEntries(null, "pools")).toEqual([]);
    expect(sectionEntries({ pools: { b: {}, a: {} } }, "pools").map(([n]) => n)).toEqual(["a", "b"]);
  });
  it("withEntry sets and removes without touching the original", () => {
    const base = { pools: { a: { x: 1 } } };
    const set = withEntry(base, "pools", "b", { y: 2 });
    expect(Object.keys(set.pools ?? {})).toEqual(["a", "b"]);
    expect(Object.keys(base.pools)).toEqual(["a"]);
    expect(withEntry(set, "pools", "a", undefined).pools).toEqual({ b: { y: 2 } });
  });
});

describe("agentState", () => {
  const base: OperatorAgent = { agent_id: "a", name: "A", operator_user_id: "u", max_concurrent_tasks: 1, runtime_manager: false, controller: null };
  const ctl = { runner_id: "r", name: "R", host: "h", version: "1", lease_expires_at: "2026-01-01T00:00:00Z" };
  it("tells live, lapsed and none apart", () => {
    expect(agentState(base)).toBe("none");
    expect(agentState({ ...base, controller: { ...ctl, live: true } })).toBe("live");
    expect(agentState({ ...base, controller: { ...ctl, live: false } })).toBe("lapsed");
  });
});

describe("findSecretViolation", () => {
  it("accepts plain settings and a cred: reference", () => {
    expect(findSecretViolation({ size: 2, credential: "cred:main", nested: { list: [{ a: 1 }] } })).toBeNull();
  });
  it("names a secret-looking field, even nested", () => {
    expect(findSecretViolation({ api_key: "x" })).toBe("api_key");
    expect(findSecretViolation({ env: { DB_Password: "x" } })).toBe("env.DB_Password");
    expect(findSecretViolation({ list: [{ authToken: "x" }] })).toBe("list[0].authToken");
    expect(findSecretViolation({ private_key: "x" })).toBe("private_key");
    expect(findSecretViolation({ apiKey: "x" })).toBe("apiKey");
  });
  it("requires a credential to be a cred: reference", () => {
    expect(findSecretViolation({ credential: "sk-live-123" })).toBe("credential");
    expect(findSecretViolation({ credential: 5 })).toBe("credential");
    expect(findSecretViolation({ Credential: "sk-live-123" })).toBe("Credential");
    expect(findSecretViolation({ cred: "sk-live-123" })).toBe("cred");
  });
});

describe("catalog credential_ref", () => {
  it("allows the blank (kept) value and a cred: reference only", () => {
    expect(findSecretViolation({ accounts: { a: { credential_ref: "" } } })).toBeNull();
    expect(findSecretViolation({ accounts: { a: { credential_ref: "cred:claude/alex" } } })).toBeNull();
    expect(findSecretViolation({ accounts: { a: { credential_ref: "sk-live-123" } } })).toBe("accounts.a.credential_ref");
    expect(findSecretViolation({ accounts: { a: { credential_ref: 1 } } })).toBe("accounts.a.credential_ref");
  });
});

describe("entrySummary", () => {
  it("picks the fields people scan for", () => {
    expect(entrySummary({ provider: "claude", max_concurrency: 4 })).toEqual({ text: ["claude"], max: 4, disabled: false });
    expect(entrySummary({ harness: "claude-code", model: "opus", enabled: false }).text).toEqual(["claude-code · opus"]);
    expect(entrySummary({ permitted_profiles: ["a", "b", "c", "d", "e"] }).text).toEqual(["a, b, c +2"]);
    expect(entrySummary(null)).toEqual({ text: [], max: null, disabled: false });
  });
});
