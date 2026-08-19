import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { DocMeta } from "@/components/doc-meta";
import type { ProjectDocument } from "@/types";

function makeDoc(overrides: Partial<ProjectDocument> = {}): ProjectDocument {
  return {
    id: "doc-1",
    project_id: "proj-1",
    parent_id: null,
    slug: "doc-1",
    title: "Runbook",
    storage_key: "documents/proj-1/doc-1.md",
    position: 0,
    created_by: "11111111-1111-1111-1111-111111111111",
    created_by_type: "user",
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-14T09:30:00Z",
    ...overrides,
  };
}

/**
 * The whole line as one string, so assertions do not depend on how it is split
 * into spans. `getByText` lands on the innermost match, hence the climb.
 */
const line = () =>
  screen.getByText(/Created by/).closest("p")?.textContent ?? "";

describe("DocMeta", () => {
  it("names the creator and says when the page last changed", () => {
    render(<DocMeta doc={makeDoc({ created_by_name: "Pavel Rogozhin" })} />);
    expect(line()).toContain("Created by Pavel Rogozhin");
    expect(line()).toContain("Updated Aug 14, 2026");
  });

  it("does not claim the creator was the last editor on a legacy row", () => {
    // Rows written before updated_by existed carry null; inventing an editor
    // would be a lie the reader cannot check.
    render(<DocMeta doc={makeDoc({ created_by_name: "Pavel Rogozhin" })} />);
    expect(line()).not.toContain("Last updated by");
  });

  it("never prints the raw uuid when the name is missing", () => {
    render(<DocMeta doc={makeDoc()} />);
    expect(line()).not.toContain("1111");
  });

  it("says Unknown when there is no name to be had, and still shows the date", () => {
    render(<DocMeta doc={makeDoc()} />);
    expect(line()).toContain("Created by Unknown");
    expect(line()).toContain("Updated Aug 14, 2026");
  });

  it("renders a machine-readable timestamp with the exact time on hover", () => {
    render(<DocMeta doc={makeDoc({ created_by_name: "Pavel" })} />);
    const stamp = screen.getByText(/Updated Aug 14, 2026/);
    expect(stamp).toHaveAttribute("datetime", "2026-08-14T09:30:00Z");
    expect(stamp.getAttribute("title")).toMatch(/Aug 14, 2026/);
  });
});

describe("DocMeta — the editor the API resolves", () => {
  it("names the last editor as well as the creator", () => {
    render(
      <DocMeta
        doc={makeDoc({
          created_by_name: "Pavel Rogozhin",
          updated_by: "22222222-2222-2222-2222-222222222222",
          updated_by_name: "Daedalus",
        })}
      />,
    );
    expect(line()).toContain("Created by Pavel Rogozhin");
    expect(line()).toContain("Last updated by Daedalus");
    // The date stops repeating the word "Updated" once an editor is named.
    expect(line()).toContain("Aug 14, 2026");
    expect(line()).not.toContain("Updated Aug 14");
  });

  it("says Unknown, not a uuid, for an editor the API could not name", () => {
    // updated_by set but the name join came back empty — a deleted actor.
    render(
      <DocMeta
        doc={makeDoc({
          created_by_name: "Pavel",
          updated_by: "22222222-2222-2222-2222-222222222222",
        })}
      />,
    );
    expect(line()).toContain("Last updated by Unknown");
    expect(line()).not.toContain("2222");
  });
});

describe("DocMeta — bad data", () => {
  it("drops the date rather than throwing on an unparseable timestamp", () => {
    render(
      <DocMeta doc={makeDoc({ created_by_name: "Pavel", updated_at: "soon" })} />,
    );
    expect(line()).toContain("Created by Pavel");
    expect(screen.queryByText(/Updated/)).not.toBeInTheDocument();
  });

  it("ignores a name that is only whitespace", () => {
    render(<DocMeta doc={makeDoc({ created_by_name: "   " })} />);
    expect(line()).toContain("Created by Unknown");
  });
});
