import { describe, it, expect, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Button } from "@/components/ui/button";

// Regression test for the trigger silently discarding `asChild`: it used to
// always wrap `children` in a fresh <button>, so
// `<DropdownMenuTrigger asChild><Button/></DropdownMenuTrigger>` rendered a
// <button> nested inside a <button> — invalid HTML, and React logs a
// hydration-error console.error on every render.
describe("DropdownMenuTrigger asChild", () => {
  it("does not nest a button inside the child button", () => {
    const { container } = render(
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="outline">Open</Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent>
          <DropdownMenuItem>Item</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>,
    );

    expect(container.querySelectorAll("button button")).toHaveLength(0);
    expect(container.querySelectorAll("button")).toHaveLength(1);
  });

  it("does not trigger React's nested-<button> console.error", () => {
    const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      render(
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="outline">Open</Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent>
            <DropdownMenuItem>Item</DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>,
      );
    } finally {
      const nestedButtonWarning = errorSpy.mock.calls.some((args) =>
        String(args[0]).includes("cannot be a descendant of"),
      );
      errorSpy.mockRestore();
      expect(nestedButtonWarning).toBe(false);
    }
  });

  it("opens the menu when the child is clicked", () => {
    render(
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="outline">Open</Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent>
          <DropdownMenuItem>Item one</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>,
    );

    expect(screen.queryByText("Item one")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    expect(screen.getByText("Item one")).toBeInTheDocument();
  });

  it("preserves the child's own onClick alongside the toggle", () => {
    const childOnClick = vi.fn();
    render(
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="outline" onClick={childOnClick}>
            Open
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent>
          <DropdownMenuItem>Item</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    expect(childOnClick).toHaveBeenCalledTimes(1);
    expect(screen.getByText("Item")).toBeInTheDocument();
  });

  it("selecting an item closes the menu and fires the item's onClick", () => {
    const onSelect = vi.fn();
    render(
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="outline">Open</Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent>
          <DropdownMenuItem onClick={onSelect}>Pick me</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    fireEvent.click(screen.getByText("Pick me"));

    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(screen.queryByText("Pick me")).not.toBeInTheDocument();
  });

  it("without asChild still renders a single real button (unchanged behavior)", () => {
    const { container } = render(
      <DropdownMenu>
        <DropdownMenuTrigger>Open</DropdownMenuTrigger>
        <DropdownMenuContent>
          <DropdownMenuItem>Item</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>,
    );

    expect(container.querySelectorAll("button")).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    expect(screen.getByText("Item")).toBeInTheDocument();
  });
});
