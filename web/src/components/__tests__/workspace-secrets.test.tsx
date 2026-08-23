import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";

const mockedApi = vi.fn();
vi.mock("@/lib/api", () => ({
  api: (...args: unknown[]) => mockedApi(...(args as [string, unknown])),
  getAccessToken: vi.fn(() => null),
}));

vi.mock("@/components/ui/toast", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

import { WorkspaceSecrets } from "@/components/workspace-secrets";
import type { Secret } from "@/types";

/**
 * The write-only secret store.
 *
 * The property under test is not "the form submits" — it is that a plaintext
 * value goes in and never comes back out of this component, and that "replace"
 * never reads the old one. So the assertions are about the rendered DOM as a
 * whole (searched for the literal secret string) rather than about specific
 * fields, because an assertion naming only the fields we thought of would still
 * pass if a value appeared somewhere we did not think of.
 */

const WS = "11111111-1111-1111-1111-111111111111";
const PLAINTEXT = "ghp_ThisMustNeverBeRenderedAnywhere";

const secret: Secret = {
  id: "22222222-2222-2222-2222-222222222222",
  name: "GITHUB_TOKEN",
  scope: "workspace",
  value_sha256_prefix: "3b1f9a02",
  value_length: 40,
  value_char_class: "a-z+A-Z+0-9",
  created_by: "33333333-3333-3333-3333-333333333333",
  created_by_type: "user",
  created_at: "2026-08-20T10:00:00Z",
};

function mockList(rows: Secret[]) {
  mockedApi.mockImplementation((_path: string, opts?: { method?: string }) => {
    if (!opts?.method || opts.method === "GET") return Promise.resolve(rows);
    return Promise.resolve({});
  });
}

beforeEach(() => {
  mockedApi.mockReset();
});

describe("WorkspaceSecrets", () => {
  it("renders every field env-inventory.py prints, and no value column", async () => {
    mockList([secret]);
    render(<WorkspaceSecrets workspaceId={WS} canManage />);

    await screen.findByTestId("secret-list");

    // The four masking fields, by their rendered values — 1:1 with what
    // scripts/env-inventory.py emits (NAME / FP / LEN / CHARS). Scope is
    // asserted too, but it is Mesh's own field: the script has no notion of
    // scope, so this list is a superset of the script's, not a copy of it.
    expect(screen.getByText("GITHUB_TOKEN")).toBeInTheDocument();
    expect(screen.getByText("3b1f9a02")).toBeInTheDocument();
    expect(screen.getByText("40")).toBeInTheDocument();
    expect(screen.getByText("a-z+A-Z+0-9")).toBeInTheDocument();
    expect(screen.getByText("workspace")).toBeInTheDocument();
  });

  it("never renders the plaintext after storing it", async () => {
    mockList([]);
    const { container } = render(<WorkspaceSecrets workspaceId={WS} canManage />);
    await screen.findByTestId("secret-empty");

    fireEvent.change(screen.getByTestId("secret-name-input"), { target: { value: "GITHUB_TOKEN" } });
    fireEvent.change(screen.getByTestId("secret-value-input"), { target: { value: PLAINTEXT } });
    fireEvent.click(screen.getByTestId("secret-submit"));

    await waitFor(() => {
      expect(mockedApi).toHaveBeenCalledWith(
        `/api/v1/workspaces/${WS}/secrets`,
        expect.objectContaining({ method: "POST" }),
      );
    });

    // The value reached the API...
    const post = mockedApi.mock.calls.find(
      (c) => (c[1] as { method?: string })?.method === "POST",
    );
    expect((post?.[1] as { body: { value: string } }).body.value).toBe(PLAINTEXT);

    // ...and is gone from the DOM, including out of the input it was typed in.
    await waitFor(() => {
      expect(container.innerHTML).not.toContain(PLAINTEXT);
    });
    expect((screen.getByTestId("secret-value-input") as HTMLInputElement).value).toBe("");
  });

  it("opens replace with an empty field — the old value is never prefilled", async () => {
    mockList([secret]);
    render(<WorkspaceSecrets workspaceId={WS} canManage />);
    await screen.findByTestId("secret-list");

    fireEvent.click(screen.getByTestId("secret-rotate-GITHUB_TOKEN"));

    const input = (await screen.findByTestId("secret-rotate-input")) as HTMLInputElement;
    expect(input.value).toBe("");
    // No GET for a value happened — the only calls so far are list reads.
    for (const call of mockedApi.mock.calls) {
      expect(String(call[0])).not.toMatch(/\/secrets\/[^/]+$/);
    }
  });

  it("replace calls rotate, which the API implements as a new record", async () => {
    mockList([secret]);
    render(<WorkspaceSecrets workspaceId={WS} canManage />);
    await screen.findByTestId("secret-list");

    fireEvent.click(screen.getByTestId("secret-rotate-GITHUB_TOKEN"));
    const rotateInput = await screen.findByTestId("secret-rotate-input");
    fireEvent.change(rotateInput, { target: { value: "new-value" } });
    fireEvent.click(screen.getByTestId("secret-rotate-submit"));

    await waitFor(() => {
      expect(mockedApi).toHaveBeenCalledWith(
        `/api/v1/secrets/${secret.id}/rotate`,
        expect.objectContaining({ method: "POST" }),
      );
    });
    // Never a PATCH/PUT: replacing is an insert, not an edit.
    for (const call of mockedApi.mock.calls) {
      const method = (call[1] as { method?: string })?.method;
      expect(method === "PATCH" || method === "PUT").toBe(false);
    }
  });

  it("hides the form and row actions when the caller cannot manage secrets", async () => {
    mockList([secret]);
    render(<WorkspaceSecrets workspaceId={WS} canManage={false} />);
    await screen.findByTestId("secret-list");

    expect(screen.queryByTestId("secret-create-form")).toBeNull();
    expect(screen.queryByTestId("secret-rotate-GITHUB_TOKEN")).toBeNull();
    expect(screen.queryByTestId("secret-delete-GITHUB_TOKEN")).toBeNull();
  });

  it("rejects a name that is not an env var shape, without calling the API", async () => {
    mockList([]);
    render(<WorkspaceSecrets workspaceId={WS} canManage />);
    await screen.findByTestId("secret-empty");

    const before = mockedApi.mock.calls.length;
    fireEvent.change(screen.getByTestId("secret-name-input"), { target: { value: "9BAD" } });
    fireEvent.change(screen.getByTestId("secret-value-input"), { target: { value: "x" } });
    fireEvent.click(screen.getByTestId("secret-submit"));

    expect(await screen.findByTestId("secret-form-error")).toBeInTheDocument();
    expect(mockedApi.mock.calls.length).toBe(before);
  });

  it("marks an expired secret rather than hiding it", async () => {
    mockList([{ ...secret, expires_at: "2020-01-01T00:00:00Z" }]);
    render(<WorkspaceSecrets workspaceId={WS} canManage />);
    expect(await screen.findByTestId("secret-expired-GITHUB_TOKEN")).toBeInTheDocument();
  });
});
