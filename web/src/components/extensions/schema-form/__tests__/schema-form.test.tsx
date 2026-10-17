import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: vi.fn() };
});

import { setLanguage } from "@/i18n";
import { ApiRequestError } from "@/lib/api";
import { SchemaForm } from "@/components/extensions/schema-form";
import { evalVisibleIf } from "@/components/extensions/schema-form/visibility";
import { validate } from "@/components/extensions/schema-form/validate";
import { buildPayload } from "@/components/extensions/schema-form/state";
import { resolveSchema } from "@/components/extensions/schema-form/resolve";
import type { FieldSchema } from "@/components/extensions/schema-form";
import gitlab from "../../__fixtures__/gitlab.json";
import telegram from "../../__fixtures__/telegram.json";
import runner from "../../__fixtures__/runner.json";
import keep from "../../__fixtures__/keep.json";

const SECRET_VALUE = "glpat-SUPER-SECRET-123";

beforeEach(async () => {
  await setLanguage("en");
});

const tokenInput = () => document.querySelector<HTMLInputElement>("input[type=password]");
const tokenField = () => screen.getByText(/^Access token\*?$/, { selector: "label" });

function form(manifest: { settings: Record<string, unknown> }, level: string, props: Partial<React.ComponentProps<typeof SchemaForm>> = {}) {
  const onSubmit = props.onSubmit ?? vi.fn().mockResolvedValue(undefined);
  render(<SchemaForm schema={manifest.settings[level] as FieldSchema} manifest={manifest} onSubmit={onSubmit} {...props} />);
  return onSubmit as ReturnType<typeof vi.fn>;
}

describe("x-visible-if", () => {
  it("handles equals, one-of, not, in and exists", () => {
    expect(evalVisibleIf(undefined, {})).toBe(true);
    expect(evalVisibleIf({ a: true }, { a: true })).toBe(true);
    expect(evalVisibleIf({ a: true }, { a: false })).toBe(false);
    expect(evalVisibleIf({ a: ["x", "y"] }, { a: "y" })).toBe(true);
    expect(evalVisibleIf({ a: { not: "off" } }, { a: "off" })).toBe(false);
    expect(evalVisibleIf({ a: { not: "off" } }, { a: "daily" })).toBe(true);
    expect(evalVisibleIf({ a: { in: [1, 2] } }, { a: 3 })).toBe(false);
    expect(evalVisibleIf({ a: { exists: true } }, { a: "" })).toBe(false);
    expect(evalVisibleIf({ a: true, b: "x" }, { a: true, b: "y" })).toBe(false);
  });
});

describe("resolveSchema / validate", () => {
  it("inlines $ref from the manifest root", () => {
    const r = resolveSchema(gitlab.settings.workspace as FieldSchema, gitlab);
    expect(Object.keys(r.properties ?? {})).toEqual(["base_url", "token", "webhook_secret"]);
  });

  it("checks required, uri, ranges, enum; skips hidden; stored secret satisfies required", () => {
    const schema = resolveSchema(gitlab.settings.workspace as FieldSchema, gitlab);
    expect(validate(schema, {}, { secretsSet: new Set() })).toMatchObject({
      base_url: { code: "required" },
      token: { code: "required" },
    });
    expect(validate(schema, { base_url: "ftp://x" }, { secretsSet: new Set(["token"]) })).toEqual({
      base_url: { code: "format", params: { format: "uri" } },
    });
    expect(validate(schema, { base_url: "https://git.example.com" }, { secretsSet: new Set(["token"]) })).toEqual({});

    const tg = telegram.settings.workspace as FieldSchema;
    // bot token is hidden while own_bot is off → not validated
    expect(validate(tg, { own_bot: false, digest_mode: "off" }, { secretsSet: new Set() })).toEqual({});
    expect(validate(tg, { own_bot: false, bot_token: "bad" }, { secretsSet: new Set() })).toEqual({});
    expect(validate(tg, { own_bot: true, bot_token: "bad" }, { secretsSet: new Set() })).toMatchObject({ bot_token: { code: "pattern" } });
    expect(validate(tg, { own_bot: false, digest_mode: "daily", digest_hour: 30 }, { secretsSet: new Set() })).toMatchObject({
      digest_hour: { code: "maximum" },
    });
  });

  it("rejects a non-array value for an array field", () => {
    const schema = { type: "object", properties: { tags: { type: "array", items: { type: "string" } } } } as FieldSchema;
    expect(validate(schema, { tags: "oops" }, { secretsSet: new Set() })).toEqual({ tags: { code: "type" } });
    expect(validate(schema, { tags: ["a"] }, { secretsSet: new Set() })).toEqual({});
  });

  it("rejects an empty string for an array field with type, not silently skipped", () => {
    const schema = { type: "object", properties: { tags: { type: "array", items: { type: "string" } } } } as FieldSchema;
    expect(validate(schema, { tags: "" }, { secretsSet: new Set() })).toEqual({ tags: { code: "type" } });
  });

  it("absent optional array is valid; required empty array still gives required", () => {
    const opt = { type: "object", properties: { tags: { type: "array", items: { type: "string" } } } } as FieldSchema;
    const req = { ...opt, required: ["tags"] } as FieldSchema;
    expect(validate(opt, {}, { secretsSet: new Set() })).toEqual({});
    expect(validate(opt, { tags: undefined }, { secretsSet: new Set() })).toEqual({});
    expect(validate(opt, { tags: [] }, { secretsSet: new Set() })).toEqual({});
    expect(validate(req, {}, { secretsSet: new Set() })).toEqual({ tags: { code: "required" } });
    expect(validate(req, { tags: [] }, { secretsSet: new Set() })).toEqual({ tags: { code: "required" } });
  });

  it("does not run a pathological manifest pattern on the UI thread", () => {
    const schema = { type: "object", properties: { x: { type: "string", pattern: "^(a+)+$" } } } as FieldSchema;
    const t = Date.now();
    expect(validate(schema, { x: "a".repeat(40) + "!" }, { secretsSet: new Set() })).toEqual({});
    expect(Date.now() - t).toBeLessThan(500);
  });

  it("skips client pattern check for an over-long pattern or over-long input (server decides)", () => {
    const long = { type: "object", properties: { x: { type: "string", pattern: "^" + "a".repeat(300) + "$" } } } as FieldSchema;
    expect(validate(long, { x: "b" }, { secretsSet: new Set() })).toEqual({});
    const bigInput = { type: "object", properties: { x: { type: "string", pattern: "^a$" } } } as FieldSchema;
    expect(validate(bigInput, { x: "b".repeat(2049) }, { secretsSet: new Set() })).toEqual({});
    expect(validate(bigInput, { x: "b" }, { secretsSet: new Set() })).toEqual({ x: { code: "pattern" } });
  });

  it("never sends a readOnly (server-derived) field", () => {
    const schema = { type: "object", properties: { name: { type: "string" }, bot_username: { type: "string", readOnly: true } } } as FieldSchema;
    expect(buildPayload(schema, { name: "x", bot_username: "unrelated_bot" })).toEqual({ name: "x" });
  });

  it("sends false for an untouched required boolean", () => {
    const schema = { type: "object", required: ["on"], properties: { on: { type: "boolean" }, opt: { type: "boolean" } } } as FieldSchema;
    expect(buildPayload(schema, {})).toEqual({ on: false });
  });
});

describe("SchemaForm from a manifest (no hand-written fields)", () => {
  it("renders the three GitLab levels from the manifest", () => {
    for (const level of ["instance", "workspace"] as const) {
      const { unmount } = render(
        <SchemaForm schema={gitlab.settings[level] as FieldSchema} manifest={gitlab} onSubmit={vi.fn()} />,
      );
      expect(screen.getByLabelText(/GitLab address/)).toBeTruthy();
      expect(tokenField()).toBeTruthy();
      unmount();
    }
    render(<SchemaForm schema={gitlab.settings.project as FieldSchema} manifest={gitlab} onSubmit={vi.fn()} />);
    expect(screen.getByLabelText(/Repository/)).toBeTruthy();
  });

  it("shows a stored secret as «Set · Change» and never puts the mask or value in the DOM", async () => {
    const onSubmit = form(gitlab, "workspace", { value: { base_url: "https://git.example.com", token: "***", webhook_secret: "***" } });
    expect(document.body.innerHTML).not.toContain("***");
    expect(screen.getAllByText("Set")).toHaveLength(2);
    expect(tokenInput()).toBeNull(); // no input until «Change»

    // required secret already stored → saves without retyping; secrets are not sent
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
    expect(onSubmit.mock.calls[0]![0]).toEqual({ base_url: "https://git.example.com" });
  });

  it("sends a replaced secret once and wipes it from the form after saving", async () => {
    const onSubmit = form(gitlab, "workspace", { value: { base_url: "https://git.example.com", token: "***" } });
    fireEvent.click(screen.getByRole("button", { name: /Change: Access token$/ }));
    const input = tokenInput()!;
    expect(input.type).toBe("password");
    fireEvent.change(input, { target: { value: SECRET_VALUE } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
    expect(onSubmit.mock.calls[0]![0]).toMatchObject({ token: SECRET_VALUE });

    await screen.findByRole("status");
    // red control: after the save the secret is nowhere in the document
    expect(document.body.innerHTML).not.toContain(SECRET_VALUE);
    expect(Array.from(document.querySelectorAll("input")).every((i) => i.value !== SECRET_VALUE)).toBe(true);
    expect(screen.getAllByText("Set").length).toBeGreaterThan(0);
  });

  it("blocks submit on client validation and shows the error under the field", async () => {
    const onSubmit = form(gitlab, "instance");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect((await screen.findAllByText("Fill in this field")).length).toBe(2);
    expect(onSubmit).not.toHaveBeenCalled();
    expect(screen.getByLabelText(/GitLab address/).getAttribute("aria-invalid")).toBe("true");
  });

  it("shows a whole-form rejection (details.settings) instead of dropping it", async () => {
    const onSubmit = vi.fn().mockRejectedValue(
      new ApiRequestError("Validation failed", "validation_failed", 400, { settings: "bot_token is required" }),
    );
    form(gitlab, "workspace", { onSubmit, value: { token: "***" } });
    fireEvent.change(screen.getByLabelText(/GitLab address/), { target: { value: "https://ok.example.com" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect((await screen.findByRole("alert")).textContent).toContain("bot_token is required");
  });

  it("maps server details to fields, line by line", async () => {
    const onSubmit = vi.fn().mockRejectedValue(
      new ApiRequestError("Validation failed", "validation_failed", 400, { "settings.base_url": "host is not reachable" }),
    );
    form(gitlab, "workspace", { onSubmit, value: { token: "***" } });
    fireEvent.change(screen.getByLabelText(/GitLab address/), { target: { value: "https://nope.example.com" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("host is not reachable")).toBeTruthy();
    expect(screen.queryByText("Validation failed")).toBeNull();
  });

  it("does not show raw server text for an unmapped failure", async () => {
    const onSubmit = vi.fn().mockRejectedValue(new ApiRequestError("pq: connection refused by internal-db-host", "internal", 500));
    form(gitlab, "workspace", { onSubmit, value: { base_url: "https://x.example.com", token: "***" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("Couldn't save the settings. Try again.")).toBeTruthy();
    expect(document.body.textContent).not.toContain("internal-db-host");
  });

  it("Telegram: x-visible-if shows the token only while the toggle is on", () => {
    form(telegram, "workspace");
    expect(screen.queryByLabelText(/Bot token/)).toBeNull();
    expect(screen.queryByLabelText(/Send hour/)).toBeNull();
    const sw = screen.getByRole("switch", { name: /Own bot/ });
    expect(sw.getAttribute("aria-checked")).toBe("false");
    fireEvent.click(sw);
    expect(sw.getAttribute("aria-checked")).toBe("true");
    expect(screen.getByLabelText(/Bot token/)).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Digests"), { target: { value: "daily" } });
    expect(screen.getByLabelText(/Send hour/)).toBeTruthy();
  });

  it("Telegram: a hidden field's value is not sent", async () => {
    const onSubmit = form(telegram, "workspace");
    fireEvent.click(screen.getByRole("switch", { name: /Own bot/ }));
    fireEvent.change(screen.getByLabelText(/Bot token/), { target: { value: "123:abc" } });
    fireEvent.click(screen.getByRole("switch", { name: /Own bot/ })); // hide again
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalled());
    expect(onSubmit.mock.calls[0]![0]).toEqual({ own_bot: false, digest_mode: "off" });
  });

  it("Telegram: a secret hidden by x-visible-if is not shown as stored after save", async () => {
    const onSubmit = form(telegram, "workspace");
    fireEvent.click(screen.getByRole("switch", { name: /Own bot/ }));
    fireEvent.change(screen.getByLabelText(/Bot token/), { target: { value: "123:abc" } });
    fireEvent.click(screen.getByRole("switch", { name: /Own bot/ })); // hide again
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalled());
    await screen.findByRole("status");
    fireEvent.click(screen.getByRole("switch", { name: /Own bot/ })); // show again
    // red control: the token was never sent, so it must not read «Set»
    expect(screen.queryByText("Set")).toBeNull();
  });

  it("Runner: list, group, textarea and pickers", async () => {
    const onSubmit = form(runner, "workspace", {
      agentOptions: [{ value: "a1", label: "Verne" }],
      projectOptions: [{ value: "p1", label: "Mesh" }],
    });
    fireEvent.change(screen.getByLabelText("Default agent"), { target: { value: "a1" } });
    fireEvent.change(screen.getByLabelText("Default project"), { target: { value: "p1" } });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    fireEvent.change(screen.getByLabelText("Allowed images, item 1"), { target: { value: "ab" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("At least 3 characters")).toBeTruthy(); // item minLength
    fireEvent.change(screen.getByLabelText("Allowed images, item 1"), { target: { value: "runner:1" } });
    fireEvent.change(screen.getByLabelText("Run policy"), { target: { value: "only trusted" } });
    expect(screen.getByRole("group", { name: "Limits" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalled());
    expect(onSubmit.mock.calls[0]![0]).toEqual({
      default_agent: "a1",
      default_project: "p1",
      allowed_images: ["runner:1"],
      policy: "only trusted",
      limits: { max_parallel: 4 },
    });
  });

  it("Keep: x-help is shown for the secret", () => {
    form(keep, "workspace");
    expect(screen.getByText("Выдаётся Keep при подключении")).toBeTruthy();
  });
});
