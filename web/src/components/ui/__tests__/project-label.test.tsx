import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import { ProjectLabel } from "@/components/ui/project-label";

function Folder({ className }: { className?: string }) {
  return <svg data-testid="folder" className={className} />;
}

const label = (c: HTMLElement) => c.querySelector("[data-project-label]") as HTMLElement;

describe("ProjectLabel", () => {
  it("with an emoji icon: a gap between icon and name, both centred on the line", () => {
    const { container } = render(<ProjectLabel icon="🏦" name="Billing" />);
    const el = label(container);
    expect(el.className).toMatch(/\bgap-1\b/);
    expect(el.className).toMatch(/\bitems-center\b/);
    expect(el.children).toHaveLength(2);
    const icon = el.children[0] as HTMLElement;
    const name = el.children[1] as HTMLElement;
    expect(icon.getAttribute("role")).toBe("img");
    expect(icon.textContent).toBe("🏦");
    expect(name.textContent).toBe("Billing");
    expect(el.textContent).toBe("🏦Billing");
  });

  it("never squeezes an emoji into a fixed box (the glyph overflowed into the name)", () => {
    const { container } = render(<ProjectLabel icon="🎯" name="CRM" />);
    const icon = label(container).firstElementChild as HTMLElement;
    expect(icon.className).not.toMatch(/\b[hw]-(?:2\.5|3|\[)/);
    expect(icon.className).toMatch(/\bshrink-0\b/);
  });

  it("without an icon: just the name, no gap", () => {
    for (const icon of ["", null, undefined, "   "]) {
      const { container, unmount } = render(<ProjectLabel icon={icon} name="CRM" />);
      const el = label(container);
      expect(el.className).not.toMatch(/\bgap-/);
      expect(el.children).toHaveLength(1);
      expect(el.textContent).toBe("CRM");
      unmount();
    }
  });

  it("an iconless project with a fallback draws the fallback with the gap, sized to the text", () => {
    const { container, getByTestId } = render(<ProjectLabel icon="" name="Mesh" fallback={Folder} />);
    const el = label(container);
    expect(el.className).toMatch(/\bgap-1\b/);
    expect(getByTestId("folder").getAttribute("class")).toContain("h-[1em] w-[1em]");
  });

  it("keeps the caller's classes and lets the name truncate", () => {
    const { container } = render(<ProjectLabel icon="📢" name="Marketing" className="hidden @xs:inline-flex text-[10px]" />);
    const el = label(container);
    expect(el.className).toMatch(/(^|\s)hidden(\s|$)/);
    expect(el.className).toContain("@xs:inline-flex");
    expect((el.lastElementChild as HTMLElement).className).toContain("truncate");
  });
});
