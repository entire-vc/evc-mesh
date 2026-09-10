import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { ApiKeyRevealPanel } from "@/components/api-key-reveal";

// Shared by register-agent-dialog.tsx, agent-detail-dialog.tsx's
// regenerate-key mode, and invite-agent-dialog.tsx (task U4) — this is the
// one place that has to prove "shown once" for all three.
describe("ApiKeyRevealPanel", () => {
  it("shows the key exactly once, and it is unreachable once the caller closes", () => {
    const onClose = vi.fn();
    const { unmount } = render(
      <ApiKeyRevealPanel apiKey="agk_ws1_deadbeef" onClose={onClose} />,
    );

    expect(screen.getAllByText("agk_ws1_deadbeef")).toHaveLength(1);
    expect(
      screen.getByText(/will only be shown once/i),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(onClose).toHaveBeenCalledTimes(1);

    // What every real caller does on this callback (handleClose +
    // resetForm/resetState) is unmount the dialog. Once that happens there
    // is no path back to the value — no button, no store, nothing that
    // outlives this render.
    unmount();
    expect(screen.queryByText("agk_ws1_deadbeef")).not.toBeInTheDocument();
  });

  it("has no control that could re-reveal a key after this render — the server holds a hash, not the key", () => {
    render(<ApiKeyRevealPanel apiKey="agk_ws1_deadbeef" onClose={vi.fn()} />);
    expect(
      screen.queryByRole("button", { name: /show key/i }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /reveal/i }),
    ).not.toBeInTheDocument();
  });

  it("flips the footer button from Close to Done once the key is copied", async () => {
    Object.assign(navigator, {
      clipboard: { writeText: vi.fn().mockResolvedValue(undefined) },
    });

    render(<ApiKeyRevealPanel apiKey="agk_ws1_deadbeef" onClose={vi.fn()} />);
    const closeButton = screen.getByRole("button", { name: "Close" });

    // The copy button is the icon-only one — the only other button in the
    // panel, with no accessible name of its own to query by.
    const copyButton = screen
      .getAllByRole("button")
      .find((b) => b !== closeButton);
    fireEvent.click(copyButton!);

    await screen.findByRole("button", { name: "Done" });
  });
});
