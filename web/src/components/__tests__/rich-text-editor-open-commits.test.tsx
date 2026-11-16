import { Profiler } from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { RichTextEditor } from "@/components/rich-text-editor";

vi.mock("@/lib/api", () => ({
  api: vi.fn(async () => ({ url: "https://example.com/presigned" })),
  getAccessToken: vi.fn(() => null),
  getMentionables: vi.fn(async () => []),
}));
vi.mock("@/hooks/useProjectTrIntegration", () => ({
  useProjectTrIntegration: () => ({ enabled: false }),
}));

/**
 * React commits an editor costs from mount until it is ready and quiet.
 *
 * Opening a task mounts the empty comment composer, so every commit counted
 * here lands in perf-counters' `task.open.react_commits` (web/perf/budget.json).
 * The block-type picker once added one: the toolbar subscribes to the cursor's
 * block state, and the plugin view reported its first reading as a store
 * change, which useSyncExternalStore renders as its own synchronous commit,
 * separate from the one Milkdown's `loading → false` already makes.
 */

async function commitsUntilReady(value: string) {
  const commits: string[] = [];
  render(
    <Profiler id="editor" onRender={(_id, phase) => commits.push(phase)}>
      <MemoryRouter>
        <RichTextEditor value={value} onChange={() => {}} />
      </MemoryRouter>
    </Profiler>,
  );
  await waitFor(() => {
    expect(document.querySelector(".mesh-doc-prose")).not.toBeNull();
  });
  // Past Milkdown's 200 ms listener debounce: anything the editor still has
  // queued after creation shows up here rather than after the count is taken.
  await new Promise((resolve) => setTimeout(resolve, 320));
  return commits;
}

function blockTypeSelect() {
  return screen.getByRole("combobox", { name: /block type|тип блока/i }) as HTMLSelectElement;
}

describe("opening the rich-text editor", () => {
  it("costs no commit for the block toolbar's first reading", async () => {
    const commits = await commitsUntilReady("");
    // Measured on this test: 5 with the first reading primed, 6 when the
    // plugin announced it through the store. A lower count is welcome (lower
    // the number with it); a higher one is a commit task.open pays for.
    expect(commits.length, `commits: ${commits.join(", ")}`).toBeLessThanOrEqual(5);
    // …and it still reflects the editor, not the store's empty default.
    expect(blockTypeSelect().value).toBe("0");
    expect(blockTypeSelect().disabled).toBe(false);
  });

  it("shows a heading the document opens with", async () => {
    // The control for the case above: a first reading that differs from the
    // paragraph default must still reach the picker without any cursor move.
    await commitsUntilReady("## Title\n\nbody");
    expect(blockTypeSelect().value).toBe("2");
  });

  it("disables the picker in a code block the document opens with", async () => {
    await commitsUntilReady("```\ncode\n```");
    expect(blockTypeSelect().value).toBe("");
    expect(blockTypeSelect().disabled).toBe(true);
  });
});
