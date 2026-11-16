import { useState } from "react";
import { describe, expect, it } from "vitest";
import { createPortal } from "react-dom";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

/**
 * The shared Dialog is used by every modal in the app, so it alone has to make
 * them modal for assistive tech and the keyboard: announced as a named dialog,
 * closed by Escape, Tab kept inside, focus handed back to the opener.
 */
function Harness({ nested = false }: { nested?: boolean }) {
  const [open, setOpen] = useState(false);
  const [inner, setInner] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        Add widget
      </button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent onClose={() => setOpen(false)} closeLabel="Close">
          <DialogHeader>
            <DialogTitle>Widget catalog</DialogTitle>
            <DialogDescription>Pick a widget to add.</DialogDescription>
          </DialogHeader>
          <input aria-label="Search" />
          {nested && (
            <>
              <button type="button" onClick={() => setInner(true)}>
                Open inner
              </button>
              <Dialog open={inner} onOpenChange={setInner}>
                <DialogContent>
                  <DialogTitle>Inner</DialogTitle>
                  <button type="button">Inner action</button>
                </DialogContent>
              </Dialog>
            </>
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}

describe("Dialog accessibility", () => {
  it("is a modal dialog named by its title and described by its description", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(screen.getByRole("button", { name: "Add widget" }));

    const dialog = screen.getByRole("dialog", { name: "Widget catalog" });
    expect(dialog).toHaveAttribute("aria-modal", "true");
    expect(dialog).toHaveAccessibleDescription("Pick a widget to add.");
  });

  it("moves focus inside on open and keeps Tab and Shift+Tab inside", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(screen.getByRole("button", { name: "Add widget" }));

    const search = screen.getByRole("textbox", { name: "Search" });
    const close = screen.getByRole("button", { name: "Close" });
    expect(search).toHaveFocus();

    await user.tab();
    expect(close).toHaveFocus();
    // Tab from the last element wraps to the first, never to the page behind.
    await user.tab();
    expect(search).toHaveFocus();
    // Shift+Tab from the first wraps to the last.
    await user.tab({ shift: true });
    expect(close).toHaveFocus();
  });

  it("closes on Escape and gives focus back to the opener", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const opener = screen.getByRole("button", { name: "Add widget" });
    await user.click(opener);
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(document.activeElement).toBe(opener);
  });

  it("gives focus back to the opener when closed by its close button", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const opener = screen.getByRole("button", { name: "Add widget" });
    await user.click(opener);
    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(document.activeElement).toBe(opener);
  });

  it("closes only the top dialog on Escape when dialogs are nested", async () => {
    const user = userEvent.setup();
    render(<Harness nested />);
    await user.click(screen.getByRole("button", { name: "Add widget" }));
    const openInner = screen.getByRole("button", { name: "Open inner" });
    await user.click(openInner);
    expect(screen.getByRole("dialog", { name: "Inner" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Inner action" })).toHaveFocus();

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog", { name: "Inner" })).not.toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "Widget catalog" })).toBeInTheDocument();
    expect(openInner).toHaveFocus();

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("does not close when an inner control has already handled Escape", async () => {
    const user = userEvent.setup();
    function InlineEdit() {
      const [open, setOpen] = useState(true);
      return (
        <Dialog open={open} onOpenChange={setOpen}>
          <DialogContent>
            <DialogTitle>Edit</DialogTitle>
            <input
              aria-label="Name"
              onKeyDown={(e) => {
                if (e.key === "Escape") e.stopPropagation();
              }}
            />
          </DialogContent>
        </Dialog>
      );
    }
    render(<InlineEdit />);
    await user.click(screen.getByRole("textbox", { name: "Name" }));
    await user.keyboard("{Escape}");
    expect(screen.getByRole("dialog", { name: "Edit" })).toBeInTheDocument();
  });

  it("keeps Tab inside when focus is in a popover a child portaled out of the dialog", async () => {
    const user = userEvent.setup();
    function WithPopover() {
      return (
        <Dialog open onOpenChange={() => {}}>
          <DialogContent>
            <DialogTitle>Schedule</DialogTitle>
            <button type="button">First</button>
            <button type="button">Last</button>
            {createPortal(
              <div>
                <input aria-label="Date" />
                <button type="button">Done</button>
              </div>,
              document.body,
            )}
          </DialogContent>
        </Dialog>
      );
    }
    render(<WithPopover />);
    const date = screen.getByRole("textbox", { name: "Date" });
    date.focus();
    // Inside the popover Tab moves as usual...
    await user.tab();
    expect(screen.getByRole("button", { name: "Done" })).toHaveFocus();
    // ...and off its last control it comes back into the dialog, not the page.
    await user.tab();
    expect(screen.getByRole("button", { name: "First" })).toHaveFocus();
    date.focus();
    await user.tab({ shift: true });
    expect(screen.getByRole("button", { name: "Last" })).toHaveFocus();
  });

  it("an inner dialog that keeps Escape for itself does not let the outer one close", async () => {
    const user = userEvent.setup();
    render(
      <Dialog open onOpenChange={() => {}}>
        <DialogContent>
          <DialogTitle>Outer</DialogTitle>
          <Dialog open onOpenChange={() => {}} closeOnEscape={false}>
            <DialogContent>
              <DialogTitle>Busy</DialogTitle>
              <button type="button">Wait</button>
            </DialogContent>
          </Dialog>
        </DialogContent>
      </Dialog>,
    );
    expect(screen.getByRole("button", { name: "Wait" })).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(screen.getByRole("dialog", { name: "Outer" })).toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "Busy" })).toBeInTheDocument();
  });

  it("keeps a consumer's own accessible name and role props", () => {
    render(
      <Dialog open onOpenChange={() => {}}>
        <DialogContent aria-label="Own name">
          <DialogTitle>Title text</DialogTitle>
        </DialogContent>
      </Dialog>,
    );
    expect(screen.getByRole("dialog", { name: "Own name" })).toBeInTheDocument();
  });
});
