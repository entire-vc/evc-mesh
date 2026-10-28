import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: vi.fn() };
});

import { setLanguage } from "@/i18n";
import { api, ApiRequestError } from "@/lib/api";
import { SchemaForm, type FieldSchema } from "@/components/extensions/schema-form";
import { resolveSchema } from "@/components/extensions/schema-form/resolve";
import { useWorkspaceStore } from "@/stores/workspace";
import type { Workspace } from "@/types";

const WS = "df814cd2-0d5c-4b1e-9a43-6f1d2b7a0c11";
const OTHER_WS = "5e0b9a7c-3d21-4f88-b0c4-2a9e6d1f7b33";
const AGENT_A = "738413ee-6a0b-4c1d-8e2f-9b3a4c5d6e7f";
const KATE = "11c2d3e4-5f60-4a7b-8c9d-0e1f2a3b4c5d";
const AGENT_A_GRANT = "c0d9339e-1a2b-4c3d-8e4f-5a6b7c8d9e0f";
const AGENT_A_GRANT_2 = "a7b8c9d0-1e2f-4a3b-9c4d-5e6f7a8b9c0d";
const KATE_GRANT = "f1e2d3c4-b5a6-4978-8a9b-0c1d2e3f4a5b";
const UUID = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i;

// the manifest the server ships, not a copy: the test breaks if a picker is dropped there
const manifest = JSON.parse(readFileSync(resolve(__dirname, "../../../../../../internal/extensions/runner/mesh-extension.json"), "utf8"));
const entry = resolveSchema(manifest.schemas.binding as FieldSchema, manifest);
// the identity block of the entry as shipped; profiles and policy have their own tests
const binding: FieldSchema = { type: "object", properties: { binding: entry.properties!.binding! }, required: ["binding"] };

/** The binding schema as it was before pickers: plain text fields for the three ids. */
function withoutPickers(s: FieldSchema): FieldSchema {
  const ids = s.properties!.binding!;
  const strip = (f: FieldSchema): FieldSchema => {
    const { "x-widget": _w, "x-agent-field": _a, "x-workspace-field": _ws, ...rest } = f;
    return (void _w, void _a, void _ws, rest);
  };
  return { ...s, properties: { ...s.properties, binding: { ...ids, properties: Object.fromEntries(Object.entries(ids.properties!).map(([k, f]) => [k, strip(f)])) } } };
}

const agents = [
  { value: AGENT_A, label: "agent-a" },
  { value: KATE, label: "kate" },
];

function grantsOf(agent: string) {
  if (agent === AGENT_A) {
    return [
      { id: AGENT_A_GRANT, agent_id: AGENT_A, workspace_id: WS, role: "member", created_at: "2026-10-01T00:00:00Z", workspace: { id: WS, name: "Mesh", slug: "mesh" } },
      { id: AGENT_A_GRANT_2, agent_id: AGENT_A, workspace_id: OTHER_WS, role: "admin", created_at: "2026-10-02T00:00:00Z", workspace: { id: OTHER_WS, name: "Spark", slug: "spark" } },
    ];
  }
  if (agent !== KATE) return [];
  return [{ id: KATE_GRANT, agent_id: KATE, workspace_id: WS, role: "member", created_at: "2026-10-03T00:00:00Z", workspace: { id: WS, name: "Mesh", slug: "mesh" } }];
}

/** Inputs and selects whose visible value is a raw id. */
function idsOnScreen(): string[] {
  const shown: string[] = [];
  for (const el of document.querySelectorAll<HTMLInputElement>("input")) if (el.type !== "hidden" && UUID.test(el.value)) shown.push(el.value);
  for (const el of document.querySelectorAll<HTMLSelectElement>("select")) {
    const text = el.selectedOptions[0]?.textContent ?? "";
    if (UUID.test(text)) shown.push(text);
  }
  if (UUID.test(document.body.textContent ?? "")) shown.push("text");
  return shown;
}

const stored = { binding: { agent_id: AGENT_A, workspace_id: WS, grant_id: AGENT_A_GRANT } };

beforeEach(async () => {
  await setLanguage("en");
  useWorkspaceStore.setState({ currentWorkspace: { id: WS, name: "Mesh", slug: "mesh" } as Workspace, workspaces: [{ id: WS, name: "Mesh", slug: "mesh" } as Workspace] });
  vi.mocked(api).mockReset();
  vi.mocked(api).mockImplementation(async (path: string) => {
    const m = path.match(/^\/api\/v1\/agents\/([^/]+)\/workspaces$/);
    if (m) return { workspaces: grantsOf(m[1]!), count: 0 } as never;
    throw new Error(`unexpected ${path}`);
  });
});

describe("runner agent settings: ids are picked by name", () => {
  it("positive control: the same form without pickers shows the raw ids", () => {
    render(<SchemaForm schema={withoutPickers(binding)} value={stored} agentOptions={agents} onSubmit={vi.fn()} />);
    expect(idsOnScreen()).toEqual(expect.arrayContaining([AGENT_A, WS, AGENT_A_GRANT]));
  });

  it("shows agent, access and workspace by name, with no id anywhere", async () => {
    render(<SchemaForm schema={binding} value={stored} agentOptions={agents} onSubmit={vi.fn()} />);
    const agent = screen.getByLabelText(/^Agent/) as HTMLSelectElement;
    expect(agent.selectedOptions[0]?.textContent).toBe("agent-a");
    const grant = (await screen.findByLabelText(/^Access/)) as HTMLSelectElement;
    await waitFor(() => expect(grant.selectedOptions[0]?.textContent).toMatch(/^Mesh · member · since /));
    expect(screen.getByTestId("schema-form-workspace").textContent).toBe("Mesh");
    expect(idsOnScreen()).toEqual([]);
  });

  it("picking another agent takes its only access and that access's workspace", async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(<SchemaForm schema={binding} value={stored} agentOptions={agents} onSubmit={onSubmit} />);
    fireEvent.change(screen.getByLabelText(/^Agent/), { target: { value: KATE } });
    await waitFor(() => expect((screen.getByLabelText(/^Access/) as HTMLSelectElement).value).toBe(KATE_GRANT));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalled());
    expect(onSubmit.mock.calls[0]![0].binding).toEqual({ agent_id: KATE, workspace_id: WS, grant_id: KATE_GRANT });
  });

  it("a workspace the user is not a member of is named from the agent's access, not as \"other\"", async () => {
    render(<SchemaForm schema={binding} value={{ binding: { agent_id: AGENT_A, workspace_id: OTHER_WS, grant_id: AGENT_A_GRANT_2 } }} agentOptions={agents} onSubmit={vi.fn()} />);
    await waitFor(() => expect(screen.getByTestId("schema-form-workspace").textContent).toBe("Spark"));
    expect(idsOnScreen()).toEqual([]);
  });

  it("a workspace id known neither to the user nor to the agent's access shows as not found", async () => {
    const gone = "9d9d9d9d-9d9d-4d9d-8d9d-9d9d9d9d9d9d";
    render(<SchemaForm schema={binding} value={{ binding: { agent_id: AGENT_A, workspace_id: gone, grant_id: AGENT_A_GRANT } }} agentOptions={agents} onSubmit={vi.fn()} />);
    await waitFor(() => expect(screen.getByTestId("schema-form-workspace").textContent).toMatch(/^Not found/));
    expect(idsOnScreen()).toEqual([]);
  });

  it("an access in another workspace moves the workspace with it", async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(<SchemaForm schema={binding} value={stored} agentOptions={agents} onSubmit={onSubmit} />);
    const grant = (await screen.findByLabelText(/^Access/)) as HTMLSelectElement;
    await waitFor(() => expect(grant.value).toBe(AGENT_A_GRANT));
    fireEvent.change(grant, { target: { value: AGENT_A_GRANT_2 } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalled());
    expect(onSubmit.mock.calls[0]![0].binding).toEqual({ agent_id: AGENT_A, workspace_id: OTHER_WS, grant_id: AGENT_A_GRANT_2 });
  });

  it("a new binding starts in the current workspace; a stale id shows as not found, not as an id", async () => {
    render(<SchemaForm schema={binding} value={{ binding: { agent_id: "0b0b0b0b-0b0b-4b0b-8b0b-0b0b0b0b0b0b" } }} agentOptions={agents} onSubmit={vi.fn()} />);
    expect((screen.getByLabelText(/^Agent/) as HTMLSelectElement).selectedOptions[0]?.textContent).toMatch(/Not found/);
    expect(screen.getByTestId("schema-form-workspace").textContent).toBe("Mesh");
    await screen.findByText(/no access to any workspace/);
    expect(idsOnScreen()).toEqual([]);
  });

  it("someone else's agent (403) falls back to its grants into this workspace", async () => {
    vi.mocked(api).mockImplementation(async (path: string) => {
      if (path.endsWith("/workspaces")) throw new ApiRequestError("forbidden", "FORBIDDEN", 403);
      if (path === `/api/v1/workspaces/${WS}/agent-grants`) return { agent_grants: [{ ...grantsOf(KATE)[0], agent: { id: KATE, name: "kate", slug: "kate" } }], count: 1 } as never;
      throw new Error(`unexpected ${path}`);
    });
    render(<SchemaForm schema={binding} value={{ binding: { agent_id: KATE, workspace_id: WS } }} agentOptions={agents} onSubmit={vi.fn()} />);
    await waitFor(() => expect((screen.getByLabelText(/^Access/) as HTMLSelectElement).value).toBe(KATE_GRANT));
  });

  it("a server error on the agent's grants is a load failure, not an empty access list", async () => {
    vi.mocked(api).mockImplementation(async (path: string) => {
      if (path.endsWith("/workspaces")) throw new ApiRequestError("boom", "INTERNAL", 500);
      if (path === `/api/v1/workspaces/${WS}/agent-grants`) return { agent_grants: [], count: 0 } as never;
      throw new Error(`unexpected ${path}`);
    });
    render(<SchemaForm schema={binding} value={{ binding: { agent_id: KATE, workspace_id: WS } }} agentOptions={agents} onSubmit={vi.fn()} />);
    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(screen.queryByText(/no access to any workspace/)).toBeNull();
  });
});
