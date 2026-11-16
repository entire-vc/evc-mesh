import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";

const repo = vi.hoisted(() => ({
  listRepoRefs: vi.fn(),
  previewRepoExtension: vi.fn(),
  installRepoExtension: vi.fn(),
  updateRepoExtension: vi.fn(),
  updateAllRepoExtensions: vi.fn(),
  rollbackRepoExtension: vi.fn(),
}));
vi.mock("@/lib/extension-repo", async (orig) => ({ ...(await orig<typeof import("@/lib/extension-repo")>()), ...repo }));
vi.mock("@/lib/extension-instance", () => ({ setInstanceAutoUpdate: vi.fn(async () => ({})) }));
vi.mock("@/components/ui/toast", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

import { setLanguage } from "@/i18n";
import { RepoInstallDialog } from "@/components/extensions/repo-install-dialog";
import { UpdatesTab } from "@/components/extensions/updates-tab";
import type { RepoUpdate } from "@/lib/extension-repo";

const SECRET = "glpat-super-secret";

beforeEach(async () => {
  Object.values(repo).forEach((f) => f.mockReset());
  await setLanguage("en");
});

const preview = {
  id: "hello", name: "Hello", version: "0.1.0", publisher: "example", description: "Minimal external extension",
  mesh_api: ">=1", compatible: true, capabilities: ["api:tasks:read"], manifest: { capabilities: { api: ["tasks:read"] } },
  ref: "v0.1.0", commit_sha: "aaa111bbb222", trust_level: "custom", warning: "",
};

describe("RepoInstallDialog", () => {
  it("walks URL → token → tag → manifest → install, warns it is custom, and never renders the token", async () => {
    repo.listRepoRefs.mockResolvedValue({ tags: [{ name: "v0.1.0", sha: "aaa" }], branches: [{ name: "main", sha: "bbb" }] });
    repo.previewRepoExtension.mockResolvedValue(preview);
    repo.installRepoExtension.mockResolvedValue({ extension_id: "hello", version: "0.1.0" });
    const onInstalled = vi.fn();
    const { baseElement } = render(<RepoInstallDialog onClose={() => undefined} onInstalled={onInstalled} />);

    fireEvent.change(screen.getByLabelText("Repository URL"), { target: { value: "https://gitlab.example.invalid/entire-vc/mesh-ext-hello" } });
    expect(screen.queryByLabelText("Access token")).toBeNull();
    fireEvent.click(screen.getByLabelText("Private repository"));
    fireEvent.change(screen.getByLabelText("Access token"), { target: { value: SECRET } });
    expect((screen.getByLabelText("Access token") as HTMLInputElement).type).toBe("password");
    expect((screen.getByRole("button", { name: "Install" }) as HTMLButtonElement).disabled).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: "Load tags and branches" }));
    await screen.findByLabelText("Tag");
    expect(repo.listRepoRefs).toHaveBeenCalledWith({ repo_url: "https://gitlab.example.invalid/entire-vc/mesh-ext-hello", ref: "", auth_token: SECRET });

    fireEvent.click(screen.getByRole("button", { name: "Read manifest" }));
    const section = await screen.findByTestId("repo-preview");
    expect(section.textContent).toContain("Hello");
    expect(section.textContent).toContain("Custom");
    expect(section.textContent).toContain("Custom extension: it was not reviewed by the catalog");
    expect(section.textContent).toContain("aaa111bb");

    // the token is in a password input only; it is not part of any rendered text
    expect(baseElement.textContent).not.toContain(SECRET);
    expect(baseElement.innerHTML).not.toContain(SECRET);

    fireEvent.click(screen.getByRole("button", { name: "Install" }));
    await waitFor(() => expect(onInstalled).toHaveBeenCalled());
    expect(repo.installRepoExtension).toHaveBeenCalledWith({ repo_url: "https://gitlab.example.invalid/entire-vc/mesh-ext-hello", ref: "v0.1.0", auth_token: SECRET });
  });

  it("switching to a branch offers the beta channel", async () => {
    repo.listRepoRefs.mockResolvedValue({ tags: [{ name: "v0.1.0", sha: "a" }], branches: [{ name: "main", sha: "b" }] });
    render(<RepoInstallDialog onClose={() => undefined} onInstalled={() => undefined} />);
    fireEvent.change(screen.getByLabelText("Repository URL"), { target: { value: "https://github.com/o/r" } });
    fireEvent.click(screen.getByRole("button", { name: "Load tags and branches" }));
    await screen.findByLabelText("Tag");
    fireEvent.click(screen.getByLabelText("Branch (beta channel)"));
    expect((await screen.findByLabelText("Branch")).textContent).toContain("main");
    expect(screen.getByText(/follows the branch head/)).toBeTruthy();
  });

  it("shows the refusal and keeps Install disabled when the manifest is rejected", async () => {
    repo.listRepoRefs.mockResolvedValue({ tags: [], branches: [{ name: "main", sha: "b" }] });
    repo.previewRepoExtension.mockRejectedValue(Object.assign(new Error("incompatible"), { code: "400", status: 400, errorCode: "extension_incompatible" }));
    render(<RepoInstallDialog onClose={() => undefined} onInstalled={() => undefined} />);
    fireEvent.change(screen.getByLabelText("Repository URL"), { target: { value: "https://github.com/o/r" } });
    fireEvent.click(screen.getByRole("button", { name: "Load tags and branches" }));
    await screen.findByLabelText("Branch");
    fireEvent.click(screen.getByRole("button", { name: "Read manifest" }));
    const alert = await screen.findByTestId("repo-install-error");
    expect(alert.textContent).toBeTruthy();
    expect((screen.getByRole("button", { name: "Install" }) as HTMLButtonElement).disabled).toBe(true);
  });
});

const upd = (over: Partial<RepoUpdate> = {}): RepoUpdate => ({
  extension_id: "hello", name: "Hello", repo_url: "https://x/y", current_version: "0.1.0", current_ref: "v0.1.0", current_commit: "aaa111bbb",
  latest_version: "0.1.1", latest_ref: "v0.1.1", latest_commit: "ccc333ddd", update_available: true, new_capabilities: [], needs_consent: false,
  pending: false, can_rollback: false, ...over,
});

const state = (updates: RepoUpdate[]) => ({ status: "ready" as const, updates, count: updates.length, reload: vi.fn() });

describe("UpdatesTab", () => {
  it("lists updates, updates one and all", async () => {
    repo.updateRepoExtension.mockResolvedValue({ extension_id: "hello", outcome: "updated", new_capabilities: [] });
    repo.updateAllRepoExtensions.mockResolvedValue({ results: [] });
    const st = state([upd()]);
    render(<UpdatesTab state={st} autoUpdate={false} onAutoUpdateChange={() => undefined} />);
    expect(screen.getByTestId("update-row-hello").textContent).toContain("Available: 0.1.1 (v0.1.1, ccc333dd)");
    fireEvent.click(screen.getByRole("button", { name: "Update" }));
    await waitFor(() => expect(repo.updateRepoExtension).toHaveBeenCalledWith("hello", undefined));
    fireEvent.click(screen.getByRole("button", { name: "Update all" }));
    await waitFor(() => expect(repo.updateAllRepoExtensions).toHaveBeenCalled());
  });

  it("a version with new permissions waits for explicit consent naming the commit", async () => {
    repo.updateRepoExtension.mockResolvedValue({ extension_id: "hello", outcome: "updated", new_capabilities: ["api:comments:write"] });
    const st = state([upd({ needs_consent: true, pending: true, new_capabilities: ["api:comments:write"] })]);
    render(<UpdatesTab state={st} autoUpdate onAutoUpdateChange={() => undefined} />);
    const row = screen.getByTestId("update-row-hello");
    expect(row.textContent).toContain("Awaiting consent");
    expect(row.textContent).toContain("api:comments:write");
    expect((screen.getByRole("button", { name: "Update all" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Agree and update" }));
    await waitFor(() => expect(repo.updateRepoExtension).toHaveBeenCalledWith("hello", "ccc333ddd"));
  });

  it("rolls back and shows an up-to-date row and a check error", async () => {
    repo.rollbackRepoExtension.mockResolvedValue({});
    const st = state([
      upd({ update_available: false, can_rollback: true, previous_version: "0.0.9" }),
      upd({ extension_id: "broken", name: "Broken", update_available: false, error: "repository access denied" }),
    ]);
    render(<UpdatesTab state={st} autoUpdate={false} onAutoUpdateChange={() => undefined} />);
    expect(screen.getByTestId("update-row-broken").textContent).toContain("Couldn't check: repository access denied");
    fireEvent.click(screen.getByRole("button", { name: "Roll back to 0.0.9" }));
    await waitFor(() => expect(repo.rollbackRepoExtension).toHaveBeenCalledWith("hello"));
  });

  it("shows the Runner versions its controllers report next to Installed", () => {
    const st = state([
      upd({ extension_id: "agent-runner", name: "Agent Runner", current_version: "0.2.40", update_available: false, reported_versions: ["0.2.54", "0.2.55"] }),
      upd({ update_available: false }),
    ]);
    render(<UpdatesTab state={st} autoUpdate={false} onAutoUpdateChange={() => undefined} />);
    expect(screen.getByTestId("update-running-agent-runner").textContent).toBe("Running: 0.2.54, 0.2.55");
    expect(screen.queryByTestId("update-running-hello")).toBeNull();
    expect(screen.queryByRole("button", { name: /Roll back/ })).toBeNull();
  });

  it("a catalog row without a git ref drops the empty ref instead of rendering '(, sha)'", () => {
    const st = state([
      upd({ extension_id: "agent-runner", name: "Agent Runner", current_version: "0.2.40", current_ref: "", current_commit: "45aa84f1c0ffee", update_available: false }),
      upd({ extension_id: "keep", name: "Keep", current_version: "0.6.5", current_ref: "main", current_commit: "ab2d26e3ffff", update_available: false }),
      upd({ extension_id: "bare", name: "Bare", current_version: "1.0.0", current_ref: "", current_commit: "", latest_ref: "", latest_commit: "9e4510ba77", latest_version: "1.0.1" }),
      upd({ extension_id: "none", name: "None", current_version: "2.0.0", current_ref: "", current_commit: "", latest_ref: undefined, latest_commit: "", latest_version: "2.0.1" }),
    ]);
    render(<UpdatesTab state={st} autoUpdate={false} onAutoUpdateChange={() => undefined} />);
    const runner = screen.getByTestId("update-row-agent-runner").textContent ?? "";
    expect(runner).toContain("Installed: 0.2.40 (45aa84f1)");
    expect(runner).not.toContain("(,");
    expect(screen.getByTestId("update-row-keep").textContent).toContain("Installed: 0.6.5 (main, ab2d26e3)");
    const bare = screen.getByTestId("update-row-bare").textContent ?? "";
    expect(bare).toContain("Installed: 1.0.0");
    expect(bare).not.toContain("Installed: 1.0.0 (");
    expect(bare).toContain("Available: 1.0.1 (9e4510ba)");
    const none = screen.getByTestId("update-row-none").textContent ?? "";
    expect(none).toContain("Available: 2.0.1");
    expect(none).not.toMatch(/\(\s*,|\(\s*\)/);
  });

  it("a commit-pinned row shows its sha once instead of repeating it as the ref", () => {
    const pinned = "201ad655a0c78952315e0a3c4801cf892e18166a";
    const next = "7f3e9c21d4b5a6978812ab34cd56ef7890ab12cd";
    const st = state([
      upd({ extension_id: "reference", name: "Reference extension", current_ref: pinned, current_commit: pinned, latest_ref: next, latest_commit: next, latest_version: "0.1.1" }),
      upd({ extension_id: "short-ref", name: "Short ref", current_ref: "201AD655", current_commit: pinned, latest_ref: next, latest_commit: "7f3e9c21", latest_version: "0.1.1" }),
      upd({ extension_id: "keep", name: "Keep", current_version: "0.6.5", current_ref: "main", current_commit: "ab2d26e3ffff", update_available: false }),
      upd({ extension_id: "qa", name: "QA pilot", current_ref: "qa-pilot-c7aed647", current_commit: "c7aed6471234", update_available: false }),
      upd({ extension_id: "hexref", name: "Hex branch", current_ref: "deadbeef", current_commit: "ab2d26e3ffff", update_available: false }),
    ]);
    render(<UpdatesTab state={st} autoUpdate={false} onAutoUpdateChange={() => undefined} />);
    const reference = screen.getByTestId("update-row-reference").textContent ?? "";
    expect(reference).toContain("Installed: 0.1.0 (201ad655)");
    expect(reference).toContain("Available: 0.1.1 (7f3e9c21)");
    expect(reference).not.toContain(pinned);
    expect(reference).not.toContain(next);
    const shortRef = screen.getByTestId("update-row-short-ref").textContent ?? "";
    expect(shortRef).toContain("Installed: 0.1.0 (201ad655)");
    expect(shortRef).toContain("Available: 0.1.1 (7f3e9c21)");
    expect(screen.getByTestId("update-row-keep").textContent).toContain("Installed: 0.6.5 (main, ab2d26e3)");
    expect(screen.getByTestId("update-row-qa").textContent).toContain("Installed: 0.1.0 (qa-pilot-c7aed647, c7aed647)");
    expect(screen.getByTestId("update-row-hexref").textContent).toContain("Installed: 0.1.0 (deadbeef, ab2d26e3)");
  });

  it("empty state points at the install button", () => {
    render(<UpdatesTab state={state([])} autoUpdate={false} onAutoUpdateChange={() => undefined} />);
    expect(screen.getByText("Everything is up to date")).toBeTruthy();
  });
});
