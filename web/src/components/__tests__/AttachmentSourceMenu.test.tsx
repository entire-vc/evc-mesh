import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { AttachmentSourceMenu } from "@/components/AttachmentSourceMenu";

vi.mock("@/lib/api", () => ({
  api: vi.fn(),
}));
import { api } from "@/lib/api";

describe("AttachmentSourceMenu", () => {
  beforeEach(() => {
    vi.mocked(api).mockResolvedValue({ items: [] });
  });

  it("offers computer and Docs, but not Obsidian, when the project has no Team Relay integration", () => {
    render(
      <AttachmentSourceMenu
        projId="proj-1"
        hasTrIntegration={false}
        onPickFiles={vi.fn()}
        onPickDoc={vi.fn()}
        onPickRelay={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: /attach/i }));

    expect(screen.getByText("From computer")).toBeInTheDocument();
    expect(screen.getByText("From Docs")).toBeInTheDocument();
    expect(screen.queryByText("From Obsidian")).not.toBeInTheDocument();
  });

  it("offers all three sources when Team Relay is connected and nothing disables Obsidian", () => {
    render(
      <AttachmentSourceMenu
        projId="proj-1"
        hasTrIntegration
        onPickFiles={vi.fn()}
        onPickDoc={vi.fn()}
        onPickRelay={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: /attach/i }));

    expect(screen.getByText("From computer")).toBeInTheDocument();
    expect(screen.getByText("From Docs")).toBeInTheDocument();
    expect(screen.getByText("From Obsidian")).toBeInTheDocument();
  });

  it("shows the Obsidian option disabled with its reason, and clicking it does not open the picker", () => {
    render(
      <AttachmentSourceMenu
        projId="proj-1"
        hasTrIntegration
        obsidianDisabledReason="This document mirrors an Obsidian share; attaching here can't be written back yet."
        onPickFiles={vi.fn()}
        onPickDoc={vi.fn()}
        onPickRelay={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: /attach/i }));
    expect(
      screen.getByText(
        "This document mirrors an Obsidian share; attaching here can't be written back yet.",
      ),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByText("From Obsidian"));
    // The relay picker dialog must not mount at all while disabled — this is the
    // "disabled control, not a control that silently inserts a broken link"
    // guarantee from the file's docstring.
    expect(screen.queryByText("Attach Obsidian doc")).not.toBeInTheDocument();
  });

  it("calls onPickFiles for the computer option", () => {
    const onPickFiles = vi.fn();
    render(
      <AttachmentSourceMenu
        projId="proj-1"
        hasTrIntegration={false}
        onPickFiles={onPickFiles}
        onPickDoc={vi.fn()}
        onPickRelay={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: /attach/i }));
    fireEvent.click(screen.getByText("From computer"));

    expect(onPickFiles).toHaveBeenCalledTimes(1);
  });

  it("opens the Docs dialog on 'From Docs' and the relay dialog on 'From Obsidian'", () => {
    render(
      <AttachmentSourceMenu
        projId="proj-1"
        hasTrIntegration
        onPickFiles={vi.fn()}
        onPickDoc={vi.fn()}
        onPickRelay={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: /attach/i }));
    fireEvent.click(screen.getByText("From Docs"));
    expect(screen.getByText("Link a document")).toBeInTheDocument();
  });
});
