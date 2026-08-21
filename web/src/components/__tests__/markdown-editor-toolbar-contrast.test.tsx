/// <reference types="node" />
import { describe, it, expect, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { MarkdownEditor } from "@/components/markdown-editor";
import {
  AA_NON_TEXT,
  AA_TEXT,
  brandkitThemes,
  contrast,
} from "@/test-utils/brandkit-contrast";

vi.mock("@/lib/api", () => ({
  api: vi.fn(),
  getAccessToken: vi.fn(() => null),
}));

vi.mock("@/components/ui/toast", () => ({
  toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn(), info: vi.fn() }),
}));

function renderInRouter(ui: React.ReactElement) {
  return render(<MemoryRouter>{ui}</MemoryRouter>);
}

// Same defect as the doc-editor toolbar (#630), same file class of bug, this
// time in the task-description editor's toolbar: `--accent`/`--foreground`
// measured 2.82:1 (light) / 1.73:1 (dark), and the pressed state darkened the
// same unreadable fill instead of adding a distinct cue.
describe("MarkdownEditor toolbar hover", () => {
  it("uses the readable secondary pairing, not the old accent one", () => {
    const { container } = renderInRouter(
      <MarkdownEditor value="hello" onChange={vi.fn()} taskId="task-1" />,
    );
    const buttons = Array.from(container.querySelectorAll("button[title]"));
    expect(buttons.length).toBeGreaterThan(3);
    for (const button of buttons) {
      expect(button).toHaveClass("hover:bg-secondary");
      expect(button).not.toHaveClass("hover:bg-accent");
    }
  });

  it("tells a pressed button from a hovered one without darkening it", () => {
    renderInRouter(<MarkdownEditor value="hello" onChange={vi.fn()} taskId="task-1" />);
    const bold = screen.getByTitle("Bold (Ctrl+B)");

    expect(bold).toHaveClass("active:ring-primary");
    expect(bold).toHaveClass("active:ring-inset");
    expect(bold).toHaveClass("active:bg-secondary");
    expect(bold.className).not.toMatch(/active:bg-(accent|primary|foreground)/);
  });

  it("still fires the formatting command after a click", () => {
    const onChange = vi.fn();
    render(
      <MemoryRouter>
        <MarkdownEditor value="hi" onChange={onChange} taskId="task-1" />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByTitle("Bold (Ctrl+B)"));
    expect(onChange).toHaveBeenCalled();
  });
});

/**
 * The hover/pressed colours are theme tokens, so the readability check
 * belongs to the tokens and not to a rendered pixel — same helper the Docs
 * tree and the document-editor toolbar use, reading `brandkit.css`, the file
 * the app actually ships.
 */
describe("MarkdownEditor toolbar hover contrast", () => {
  const { light, dark } = brandkitThemes();

  it("reads on both themes", () => {
    expect(contrast(light, "--secondary", "--secondary-foreground")).toBeCloseTo(16.31, 1);
    expect(contrast(dark, "--secondary", "--secondary-foreground")).toBeCloseTo(11.39, 1);
    expect(contrast(light, "--secondary", "--secondary-foreground")).toBeGreaterThanOrEqual(AA_TEXT);
    expect(contrast(dark, "--secondary", "--secondary-foreground")).toBeGreaterThanOrEqual(AA_TEXT);
  });

  it("beats what it replaced, in both themes and against both foregrounds", () => {
    // The button rests at --muted-foreground and the old hover swapped it to
    // --foreground, so the old pairing was unreadable coming and going.
    expect(contrast(light, "--accent", "--foreground")).toBeCloseTo(2.82, 1);
    expect(contrast(dark, "--accent", "--foreground")).toBeCloseTo(1.73, 1);
    expect(contrast(light, "--accent", "--muted-foreground")).toBeCloseTo(1.21, 1);
    expect(contrast(dark, "--accent", "--muted-foreground")).toBeCloseTo(1.64, 1);

    for (const theme of [light, dark]) {
      expect(contrast(theme, "--accent", "--foreground")).toBeLessThan(AA_TEXT);
      expect(contrast(theme, "--secondary", "--secondary-foreground")).toBeGreaterThan(
        contrast(theme, "--accent", "--foreground"),
      );
    }
  });

  it("keeps the pressed ring visible against the fill it sits on", () => {
    // Non-text contrast (WCAG 1.4.11) is 3:1. This is what separates
    // "pressed" from "hovered" — same fill, ring only on press.
    expect(contrast(light, "--secondary", "--primary")).toBeCloseTo(5.78, 1);
    expect(contrast(dark, "--secondary", "--primary")).toBeCloseTo(6.59, 1);
    expect(contrast(light, "--secondary", "--primary")).toBeGreaterThanOrEqual(AA_NON_TEXT);
    expect(contrast(dark, "--secondary", "--primary")).toBeGreaterThanOrEqual(AA_NON_TEXT);
  });

  it("leaves the icon readable in the moment before the foreground swaps", () => {
    // transition-colors runs fill and text at the same duration, but a
    // dropped hover:text-* would leave --muted-foreground on the fill
    // permanently. That must still clear AA rather than merely being better
    // than before.
    expect(contrast(light, "--secondary", "--muted-foreground")).toBeCloseTo(4.78, 1);
    expect(contrast(dark, "--secondary", "--muted-foreground")).toBeCloseTo(4.03, 1);
  });
});
