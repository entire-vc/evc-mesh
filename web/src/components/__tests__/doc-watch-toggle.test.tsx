import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";

const mockedApi = vi.fn();
vi.mock("@/lib/api", () => ({
  api: (...args: unknown[]) => mockedApi(...(args as [string, unknown])),
  getAccessToken: vi.fn(() => null),
}));

const toastSuccess = vi.fn();
const toastError = vi.fn();
vi.mock("@/components/ui/toast", () => ({
  toast: { success: (m: string) => toastSuccess(m), error: (m: string) => toastError(m) },
}));

import { DocWatchToggle, type DocumentWatchState } from "@/components/doc-watch-toggle";

function state(overrides: Partial<DocumentWatchState> = {}): DocumentWatchState {
  return { watching: false, muted: false, watcher_count: 0, ...overrides };
}

/**
 * The button that follows a document.
 *
 * Its job is not just to toggle: subscriptions here can be created FOR you (you
 * made the page, you commented on it), so the assertions below are mostly about
 * the button being honest about a state the user did not choose — and about an
 * unsubscribe being a refusal rather than an absence, which is the distinction
 * that stops the next comment from silently re-subscribing you.
 */
describe("DocWatchToggle", () => {
  beforeEach(() => {
    mockedApi.mockReset();
    toastSuccess.mockReset();
    toastError.mockReset();
  });

  it("offers to subscribe when the caller is not watching", async () => {
    mockedApi.mockResolvedValueOnce(state());

    render(<DocWatchToggle documentId="doc-1" />);

    const button = await screen.findByTestId("doc-watch-toggle");
    expect(button).toHaveTextContent("Watch");
    expect(button).toHaveAttribute("aria-pressed", "false");
  });

  it("says WHY you are watching when you never pressed the button", async () => {
    // The failure this guards: a toggle already switched on, with no
    // explanation, reads as a bug — and the first instinct is to switch it off.
    mockedApi.mockResolvedValueOnce(
      state({ watching: true, source: "commenter", watcher_count: 3 }),
    );

    render(<DocWatchToggle documentId="doc-1" />);

    const button = await screen.findByTestId("doc-watch-toggle");
    expect(button).toHaveTextContent("Watching");
    expect(button).toHaveAttribute("aria-pressed", "true");
    expect(button.getAttribute("title")).toContain("because you commented");
  });

  it("distinguishes an unsubscribe from never having subscribed", async () => {
    mockedApi.mockResolvedValueOnce(state({ muted: true }));

    render(<DocWatchToggle documentId="doc-1" />);

    const button = await screen.findByTestId("doc-watch-toggle");
    // Same label as "never subscribed" — the difference the user needs is the
    // promise that commenting will not quietly turn it back on.
    expect(button).toHaveTextContent("Watch");
    expect(button.getAttribute("title")).toContain("will not re-subscribe you");
  });

  it("subscribes with PUT and unsubscribes with DELETE", async () => {
    mockedApi
      .mockResolvedValueOnce(state())
      .mockResolvedValueOnce(state({ watching: true, source: "explicit", watcher_count: 1 }));

    render(<DocWatchToggle documentId="doc-1" />);
    const button = await screen.findByTestId("doc-watch-toggle");

    fireEvent.click(button);

    await waitFor(() => expect(button).toHaveTextContent("Watching"));
    expect(mockedApi).toHaveBeenLastCalledWith("/api/v1/documents/doc-1/watch", {
      method: "PUT",
    });

    mockedApi.mockResolvedValueOnce(state({ muted: true }));
    fireEvent.click(button);

    await waitFor(() => expect(button).toHaveTextContent("Watch"));
    expect(mockedApi).toHaveBeenLastCalledWith("/api/v1/documents/doc-1/watch", {
      method: "DELETE",
    });
  });

  it("keeps the old state and says so when the toggle fails", async () => {
    // A button that flips to "Watching" on a request that failed is a lie the
    // user only discovers by not receiving notifications.
    mockedApi
      .mockResolvedValueOnce(state())
      .mockRejectedValueOnce(new Error("nope"));

    render(<DocWatchToggle documentId="doc-1" />);
    const button = await screen.findByTestId("doc-watch-toggle");

    fireEvent.click(button);

    await waitFor(() => expect(toastError).toHaveBeenCalled());
    expect(button).toHaveTextContent("Watch");
    expect(button).toHaveAttribute("aria-pressed", "false");
  });

  it("renders nothing when the subscription state cannot be read", async () => {
    mockedApi.mockRejectedValueOnce(new Error("down"));

    render(<DocWatchToggle documentId="doc-1" />);

    await waitFor(() => expect(mockedApi).toHaveBeenCalled());
    expect(screen.queryByTestId("doc-watch-toggle")).toBeNull();
  });

  it("shows how many others are watching, and only when there are others", async () => {
    mockedApi.mockResolvedValueOnce(state({ watching: true, watcher_count: 1 }));
    const { unmount } = render(<DocWatchToggle documentId="doc-1" />);
    let button = await screen.findByTestId("doc-watch-toggle");
    expect(button).not.toHaveTextContent("1");
    unmount();

    mockedApi.mockResolvedValueOnce(state({ watching: true, watcher_count: 4 }));
    render(<DocWatchToggle documentId="doc-2" />);
    button = await screen.findByTestId("doc-watch-toggle");
    expect(button).toHaveTextContent("4");
  });
});
