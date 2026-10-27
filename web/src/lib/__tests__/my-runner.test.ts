import { describe, expect, it } from "vitest";
import { agentState, bindRefs, entrySummary, fieldErrorsByEntry, findSecretViolation, fromFormValue, isSchemaPath, parseLimit, refOptions, sectionEntries, toFormValue, unknownFields, withEntry, type OperatorAgent } from "@/lib/my-runner";
import type { FieldSchema } from "@/components/extensions/schema-form";

describe("parseLimit", () => {
  it.each([["0", 0], [" 12 ", 12], ["1000", 1000]])("accepts %j", (text, n) => expect(parseLimit(text)).toBe(n));
  it.each(["", "-1", "1001", "1.5", "abc", "12345"])("refuses %j", (text) => expect(parseLimit(text)).toBeNull());
});

describe("entry form helpers", () => {
  it("tells a path the form can show from one it cannot", () => {
    const schema: FieldSchema = { type: "object", properties: {
      g: { type: "object", properties: { b: { type: "string" } } },
      m: { type: "object", additionalProperties: { type: "array", items: { type: "string" } } },
    } };
    expect(isSchemaPath(schema, "g.b")).toBe(true);
    expect(isSchemaPath(schema, "m.any-key")).toBe(true);
    expect(isSchemaPath(schema, "g.x")).toBe(false);
    expect(isSchemaPath(schema, "burst")).toBe(false);
  });
  const config = {
    controllers: { mini: { capabilities: ["claude_code", "codex"] } },
    accounts: { "a/one": { provider: "anthropic" }, "o/one": { provider: "openai" } },
    pools: { "a/5h": { provider: "anthropic" }, "o/x": { provider: "openai" } },
    profiles: { p1: {}, p2: {}, "p.dotted": {} },
    bindings: { vera: {}, "vera.v2": {} },
  };

  it("offers only what exists, narrowed by the entry", () => {
    const acct = refOptions(config, "accounts", { provider: "openai" });
    expect(acct.pools).toEqual(["o/x"]);
    expect(acct.providers).toEqual(["anthropic", "openai"]);
    expect(refOptions(config, "profiles", { controller_ref: "mini" }).capabilities).toEqual(["claude_code", "codex"]);
    expect(refOptions(config, "bindings", { permitted_profiles: ["p2"] }).permitted_profiles).toEqual(["p2"]);
  });

  it("turns x-ref into enum, nested and in maps", () => {
    const schema: FieldSchema = {
      type: "object",
      properties: {
        account_ref: { type: "string", "x-ref": "accounts" },
        pools: { type: "array", items: { type: "string", "x-ref": "pools" } },
        edges: { type: "object", "x-key-ref": "profiles", additionalProperties: { type: "array", items: { type: "string", "x-ref": "profiles" } } },
      },
    };
    const b = bindRefs(schema, { accounts: ["a"], pools: ["x"], profiles: ["p"] });
    expect(b.properties!.account_ref!.enum).toEqual(["a"]);
    expect(b.properties!.pools!.items!.enum).toEqual(["x"]);
    expect(b.properties!.edges!.propertyNames!.enum).toEqual(["p"]);
    expect((b.properties!.edges!.additionalProperties as FieldSchema).items!.enum).toEqual(["p"]);
    expect(schema.properties!.account_ref!.enum).toBeUndefined();
  });

  it("names fields the schema does not know", () => {
    const schema: FieldSchema = { type: "object", properties: { a: { type: "string" }, g: { type: "object", properties: { b: { type: "string" } } } } };
    expect(unknownFields(schema, { a: "1", x: 1, g: { b: "2", y: 2 } })).toEqual(["x", "g.y"]);
  });

  it("keeps a stored credential reference unless a new one is typed", () => {
    expect(toFormValue("accounts", { provider: "anthropic", credential_ref: "" }, false).credential_ref).toBe("********");
    expect(toFormValue("accounts", {}, true).credential_ref).toBeUndefined();
    expect(fromFormValue("accounts", { provider: "anthropic" })).toEqual({ provider: "anthropic", credential_ref: "" });
    expect(fromFormValue("accounts", { credential_ref: "cred:new" })).toEqual({ credential_ref: "cred:new" });
    expect(fromFormValue("pools", { provider: "anthropic" })).toEqual({ provider: "anthropic" });
  });

  it("groups the server's reasons by entry, names with dots included", () => {
    expect(fieldErrorsByEntry({
      "config.profiles.p.dotted.account_ref": "missing_reference",
      "config.bindings.vera.v2.policy.primary_profiles": "not_permitted",
      "config.bindings.vera.v2.enabled": "invalid",
      "config.pools.gone.max_concurrency": "invalid",
    }, config)).toEqual([
      { section: "profiles", name: "p.dotted", fields: { account_ref: "missing_reference" } },
      { section: "bindings", name: "vera.v2", fields: { "policy.primary_profiles": "not_permitted", enabled: "invalid" } },
    ]);
  });
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
