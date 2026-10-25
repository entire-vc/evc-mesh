import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

describe("DropdownMenuTrigger asChild", () => {
  it("does not wrap the child in an extra <button> (no nested button)", () => {
    render(
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button type="button" className="my-button">
            Open
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent>
          <DropdownMenuItem>Item</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>,
    );
    expect(document.querySelectorAll("button button")).toHaveLength(0);
    // The single button rendered is the child itself, carrying its own class.
    const button = screen.getByRole("button", { name: "Open" });
    expect(button.className).toContain("my-button");
  });

  it("toggles the menu open on click when asChild is used", () => {
    render(
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button type="button">Open</button>
        </DropdownMenuTrigger>
        <DropdownMenuContent>
          <DropdownMenuItem>Pick me</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>,
    );
    expect(screen.queryByText("Pick me")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    expect(screen.getByText("Pick me")).toBeInTheDocument();
  });

  it("calls the child's own onClick as well as toggling the menu", () => {
    const childClick = vi.fn();
    render(
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button type="button" onClick={childClick}>
            Open
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent>
          <DropdownMenuItem>Pick me</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    expect(childClick).toHaveBeenCalledTimes(1);
    expect(screen.getByText("Pick me")).toBeInTheDocument();
  });

  it("merges className from the trigger onto the child instead of dropping it", () => {
    render(
      <DropdownMenu>
        <DropdownMenuTrigger asChild className="trigger-class">
          <button type="button" className="child-class">
            Open
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent>
          <DropdownMenuItem>Item</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>,
    );
    const button = screen.getByRole("button", { name: "Open" });
    expect(button.className).toContain("child-class");
    expect(button.className).toContain("trigger-class");
  });

  it("without asChild still renders its own <button> wrapper (unchanged default behavior)", () => {
    render(
      <DropdownMenu>
        <DropdownMenuTrigger>Open</DropdownMenuTrigger>
        <DropdownMenuContent>
          <DropdownMenuItem>Item</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    expect(screen.getByText("Item")).toBeInTheDocument();
  });
});

describe("DropdownMenuContent viewport containment", () => {
  const open = (style?: React.CSSProperties) => {
    render(
      <DropdownMenu>
        <DropdownMenuTrigger>Open</DropdownMenuTrigger>
        <DropdownMenuContent style={style}>
          <DropdownMenuItem>Item</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>,
    );
    vi.spyOn(document.documentElement, "clientWidth", "get").mockReturnValue(393);
    const rectSpy = vi
      .spyOn(HTMLElement.prototype, "getBoundingClientRect")
      .mockReturnValue({ left: 300, right: 510, top: 0, bottom: 0, width: 210, height: 0, x: 300, y: 0, toJSON: () => ({}) });
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    rectSpy.mockRestore();
    vi.restoreAllMocks();
    return screen.getByRole("menuitem").parentElement as HTMLElement;
  };

  it("shifts a menu overflowing the right edge back inside the viewport", () => {
    const menu = open();
    expect(menu.style.transform).toBe("translateX(-125px)");
  });

  it("keeps a caller-supplied transform when shifting", () => {
    const menu = open({ transform: "scale(2)" });
    // the shift leads, so a non-unit caller transform cannot scale it
    expect(menu.style.transform).toBe("translateX(-125px) scale(2)");
  });
});
