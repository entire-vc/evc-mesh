import { beforeEach, describe, expect, it, vi } from "vitest";
import { render } from "@testing-library/react";

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: vi.fn() };
});

import { setLanguage } from "@/i18n";
import { SchemaForm, type FieldSchema } from "@/components/extensions/schema-form";
// The manifest the server serves, not a copy: the form is checked against what ships.
import teamRelay from "../../../../../../internal/extensions/team-relay/mesh-extension.json";

const SHARE_ID = "5ba2a6c4-3b9b-485b-b0e4-b8b8691dc49a";
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

// A stored binding as GET /projects/:p/extensions/team-relay returns it.
const stored = { share_id: SHARE_ID, share_slug: "mesh-docs", share_name: "Mesh Docs", agent_key: "***" };

function uuidInputs(schema: FieldSchema) {
  const { container, unmount } = render(<SchemaForm schema={schema} manifest={teamRelay} value={stored} onSubmit={vi.fn()} />);
  const found = [...container.querySelectorAll<HTMLInputElement>("input, textarea")].filter((el) => UUID.test(el.value.trim()));
  const shown = [...container.querySelectorAll<HTMLInputElement>("input")].map((el) => el.value);
  unmount();
  return { found, shown };
}

beforeEach(async () => {
  await setLanguage("ru");
});

describe("Team Relay project form", () => {
  const project = teamRelay.settings.project as FieldSchema;

  it("shows the share by name and has no field holding its UUID", () => {
    const { found, shown } = uuidInputs(project);
    expect(found).toHaveLength(0);
    expect(shown).toContain("Mesh Docs");
  });

  it("positive control: a share_id text field in the schema is caught", () => {
    const withIdField: FieldSchema = { ...project, properties: { share_id: { type: "string", title: "Share" }, ...project.properties } };
    expect(uuidInputs(withIdField).found).toHaveLength(1);
  });
});
