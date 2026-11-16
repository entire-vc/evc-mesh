import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { useWorkspaceStore } from "@/stores/workspace";
import { useAuthStore } from "@/stores/auth";
import { CACHE_MS, analyticsParams, clearAnalyticsCache, fetchAnalytics, useAnalytics, type AnalyticsQuery } from "../use-analytics";

// The network is the only mock.
vi.mock("@/lib/api", () => ({ api: vi.fn() }));
import { api } from "@/lib/api";

const metrics = { task_metrics: { total: 7 } };

function Probe({ q, id }: { q: AnalyticsQuery; id: string }) {
  const r = useAnalytics(q);
  return <div data-testid={id}>{r.state === "ready" ? String((r.value as unknown as typeof metrics).task_metrics.total) : r.state}</div>;
}

beforeEach(() => {
  clearAnalyticsCache();
  vi.mocked(api).mockReset();
  vi.mocked(api).mockResolvedValue(metrics);
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws1" } as never });
  useAuthStore.setState({ user: { id: "owner" } as never, isAuthenticated: true });
});
afterEach(cleanup);

describe("useAnalytics", () => {
  it("six widgets with one filter make one request", async () => {
    const q: AnalyticsQuery = { period: { days: 30 } };
    render(
      <>
        {["a", "b", "c", "d", "e", "f"].map((id) => (
          <Probe key={id} id={id} q={{ ...q }} />
        ))}
      </>,
    );
    await waitFor(() => expect(screen.getByTestId("f").textContent).toBe("7"));
    for (const id of ["a", "b", "c", "d", "e"]) expect(screen.getByTestId(id).textContent).toBe("7");
    expect(api).toHaveBeenCalledTimes(1);
  });

  it("a different filter is its own request", async () => {
    render(
      <>
        <Probe id="a" q={{ period: { days: 30 } }} />
        <Probe id="b" q={{ period: { days: 7 } }} />
        <Probe id="c" q={{ period: { days: 30 }, projectId: "p1" }} />
      </>,
    );
    await waitFor(() => expect(screen.getByTestId("c").textContent).toBe("7"));
    expect(api).toHaveBeenCalledTimes(3);
  });

  it("keeps an answer for 30 s, then asks again; a failure is not kept", async () => {
    const p = { from: "2026-11-01" };
    await fetchAnalytics("owner", "ws1", p, 0);
    await fetchAnalytics("owner", "ws1", p, CACHE_MS - 1);
    expect(api).toHaveBeenCalledTimes(1);
    await fetchAnalytics("owner", "ws1", p, CACHE_MS);
    expect(api).toHaveBeenCalledTimes(2);

    vi.mocked(api).mockRejectedValueOnce(new Error("down"));
    const q = { from: "2026-10-01" };
    await expect(fetchAnalytics("owner", "ws1", q, 0)).rejects.toThrow("down");
    await fetchAnalytics("owner", "ws1", q, 1);
    expect(api).toHaveBeenCalledTimes(4);
  });

  it("shows an error with a retry", async () => {
    vi.mocked(api).mockRejectedValueOnce(new Error("down"));
    render(<Probe id="a" q={{}} />);
    await waitFor(() => expect(screen.getByTestId("a").textContent).toBe("error"));
  });
});

describe("useAnalytics across auth sessions", () => {
  const owner = { task_metrics: { total: 3 } };
  const member = { task_metrics: { total: 1 } };
  const as = (id: string | null) => useAuthStore.setState({ user: id ? ({ id } as never) : null, isAuthenticated: !!id });

  it("another user in the same workspace and filter is asked again, not served the cached answer", async () => {
    vi.mocked(api).mockResolvedValueOnce(owner).mockResolvedValueOnce(member);
    const first = render(<Probe id="a" q={{ period: { days: 30 } }} />);
    await waitFor(() => expect(screen.getByTestId("a").textContent).toBe("3"));
    first.unmount();

    as(null);
    as("member");
    render(<Probe id="a" q={{ period: { days: 30 } }} />);
    await waitFor(() => expect(screen.getByTestId("a").textContent).toBe("1"));
    expect(api).toHaveBeenCalledTimes(2);
  });

  it("a user switch without a logout in between still does not reuse the answer", async () => {
    vi.mocked(api).mockResolvedValueOnce(owner).mockResolvedValueOnce(member);
    const first = render(<Probe id="a" q={{}} />);
    await waitFor(() => expect(screen.getByTestId("a").textContent).toBe("3"));
    first.unmount();

    as("member");
    render(<Probe id="a" q={{}} />);
    await waitFor(() => expect(screen.getByTestId("a").textContent).toBe("1"));
    expect(api).toHaveBeenCalledTimes(2);
  });

  it("an answer of the previous session that lands late is never shown", async () => {
    let finishOwner!: (v: unknown) => void;
    vi.mocked(api)
      .mockImplementationOnce(() => new Promise((r) => (finishOwner = r)) as never)
      .mockResolvedValueOnce(member);
    render(<Probe id="a" q={{}} />);
    expect(screen.getByTestId("a").textContent).toBe("loading");

    act(() => as("member"));
    await waitFor(() => expect(screen.getByTestId("a").textContent).toBe("1"));
    await act(async () => finishOwner(owner));
    expect(screen.getByTestId("a").textContent).toBe("1");
  });

  it("positive control: the same user within 30 s shares the cached answer", async () => {
    const first = render(<Probe id="a" q={{}} />);
    await waitFor(() => expect(screen.getByTestId("a").textContent).toBe("7"));
    first.unmount();
    render(<Probe id="a" q={{}} />);
    await waitFor(() => expect(screen.getByTestId("a").textContent).toBe("7"));
    expect(api).toHaveBeenCalledTimes(1);
  });
});

describe("analyticsParams", () => {
  const now = new Date("2026-11-13T15:42:00Z");
  it("a preset sends only its first day, so widgets mounted apart share a key", () => {
    expect(analyticsParams({ period: { days: 7 } }, now)).toEqual({ from: "2026-11-06" });
    expect(analyticsParams({}, now)).toEqual({ from: "2026-10-14" });
  });
  it("a custom range ends at the end of its last day", () => {
    expect(analyticsParams({ period: { from: "2026-10-01", to: "2026-10-31" }, projectId: "p", topTasksSort: "tokens" }, now)).toEqual({
      from: "2026-10-01",
      to: "2026-10-31T23:59:59Z",
      project_id: "p",
      top_tasks_sort: "tokens",
    });
  });
});
