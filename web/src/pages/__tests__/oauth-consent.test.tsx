import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: vi.fn() };
});

import { api } from "@/lib/api";
import {
  OAuthConsentPage,
  displayClientName,
  isLoopbackRedirect,
  isSessionExpired,
  isSafeRedirect,
  redirectHost,
} from "@/pages/oauth-consent";
import { useAuthStore } from "@/stores/auth";
import type { OAuthConsentInfo, User } from "@/types";

const mockApi = api as unknown as ReturnType<typeof vi.fn>;

const USER: User = {
  id: "u1",
  email: "ada@example.com",
  name: "Ada",
  username: "ada",
  avatar_url: "",
  is_active: true,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const QUERY =
  "client_id=https%3A%2F%2Fclaude.ai%2Fmeta&redirect_uri=http%3A%2F%2F127.0.0.1%3A5555%2Fcallback" +
  "&response_type=code&code_challenge=abc&code_challenge_method=S256&scope=mesh&state=xyz";

// A normal hosted client. Tests that need a loopback one override redirect_uri,
// redirect_host and loopback_warning together, so the fixture never contradicts
// itself.
const INFO: OAuthConsentInfo = {
  client_name: "Claude Code",
  redirect_uri: "https://claude.ai/api/mcp/auth_callback",
  redirect_host: "claude.ai",
  loopback_warning: false,
  scope: "mesh",
  workspaces: [
    { id: "11111111-1111-1111-1111-111111111111", name: "Acme", slug: "acme" },
    { id: "22222222-2222-2222-2222-222222222222", name: "Side project", slug: "side" },
  ],
};

function LocationProbe() {
  const loc = useLocation();
  return <div data-testid="loc">{loc.pathname + loc.search}</div>;
}

function renderPage() {
  return render(
    <MemoryRouter initialEntries={[`/connect/consent?${QUERY}`]}>
      <Routes>
        <Route path="/connect/consent" element={<OAuthConsentPage />} />
        <Route path="/login" element={<LocationProbe />} />
      </Routes>
    </MemoryRouter>,
  );
}

const LOOPBACK_INFO: OAuthConsentInfo = {
  ...INFO,
  redirect_uri: "http://127.0.0.1:5555/callback",
  redirect_host: "127.0.0.1:5555",
  loopback_warning: true,
};

const assign = vi.fn();

beforeEach(() => {
  mockApi.mockReset();
  assign.mockReset();
  useAuthStore.setState({ user: USER, isAuthenticated: true, isLoading: false });
  Object.defineProperty(window, "location", {
    configurable: true,
    value: { ...window.location, assign },
  });
});

afterEach(() => vi.restoreAllMocks());

describe("isSafeRedirect", () => {
  it("allows only http(s); refuses everything the server never issues", () => {
    expect(isSafeRedirect("https://claude.ai/api/mcp/auth_callback?code=1")).toBe(true);
    expect(isSafeRedirect("http://127.0.0.1:5555/callback?code=1")).toBe(true);
    for (const bad of [
      "javascript:alert(1)",
      "JavaScript:alert(1)",
      " javascript:alert(1)",
      "data:text/html,x",
      "blob:https://claude.ai/abc",
      "about:blank",
      "file:///etc/passwd",
      "cursor://oauth/callback",
      "/relative/path",
      "not a url",
    ]) {
      expect(isSafeRedirect(bad), bad).toBe(false);
    }
  });
});

describe("isSessionExpired", () => {
  it("is true only for the 401 api() raises after its refresh failed", () => {
    expect(isSessionExpired({ status: 401, code: "UNAUTHORIZED" })).toBe(true);
    // The OAuth server's 401 for an unknown client_id: body {"error":"invalid_client"}
    // reaches the caller with no `code`, which api() fills in as "UNKNOWN".
    expect(isSessionExpired({ status: 401, code: "UNKNOWN" })).toBe(false);
    expect(isSessionExpired({ status: 400, code: "UNAUTHORIZED" })).toBe(false);
    expect(isSessionExpired(new Error("x"))).toBe(false);
    expect(isSessionExpired(null)).toBe(false);
  });
});

describe("display helpers", () => {
  it("displayClientName strips bidi/format characters and never returns empty", () => {
    expect(displayClientName("Claude\u202E Code\u200B")).toBe("Claude Code");
    expect(displayClientName("\u202E\u200B")).toBe("Unnamed app");
  });

  it("redirectHost reads the host from the URL (punycode), not the server string", () => {
    expect(redirectHost("https://claude.ai/cb", "x")).toBe("claude.ai");
    // Cyrillic 'а' in place of Latin 'a': must NOT render as claude.ai
    expect(redirectHost("https://cl\u0430ude.ai/cb", "x")).toBe("xn--clude-5ve.ai");
    expect(redirectHost("::not a url", "fallback")).toBe("fallback");
  });

  it("isLoopbackRedirect matches localhost, 127/8 and ::1 only", () => {
    expect(isLoopbackRedirect("http://localhost:1/cb")).toBe(true);
    expect(isLoopbackRedirect("http://127.0.0.1/cb")).toBe(true);
    expect(isLoopbackRedirect("http://127.9.9.9:8080/cb")).toBe(true);
    expect(isLoopbackRedirect("http://[::1]:8080/cb")).toBe(true);
    expect(isLoopbackRedirect("https://claude.ai/cb")).toBe(false);
    expect(isLoopbackRedirect("http://127.0.0.1.evil.test/cb")).toBe(false);
    expect(isLoopbackRedirect("http://localhost.evil.test/cb")).toBe(false);
  });
});

describe("OAuthConsentPage", () => {
  it("sends a signed-out user to /login carrying the whole authorize query", async () => {
    useAuthStore.setState({ user: null, isAuthenticated: false, isLoading: false });
    renderPage();

    const loc = (await screen.findByTestId("loc")).textContent!;
    expect(loc.startsWith("/login?redirect=")).toBe(true);
    const back = new URLSearchParams(loc.split("?")[1]).get("redirect")!;
    expect(back).toBe(`/connect/consent?${QUERY}`);
    expect(mockApi).not.toHaveBeenCalled();
  });

  it("shows the client name and the redirect HOST, and no loopback warning for a normal client", async () => {
    mockApi.mockResolvedValueOnce({
      ...INFO,
      redirect_uri: "https://claude.ai/api/mcp/auth_callback",
      redirect_host: "claude.ai",
    });
    renderPage();

    expect(await screen.findByRole("heading", { name: "Claude Code" })).toBeInTheDocument();
    expect(screen.getByTestId("redirect-host")).toHaveTextContent("claude.ai");
    expect(screen.queryByText(/program on this computer/i)).not.toBeInTheDocument();
    expect(mockApi).toHaveBeenCalledWith("/api/v1/oauth/consent", {
      params: {
        client_id: "https://claude.ai/meta",
        redirect_uri: "http://127.0.0.1:5555/callback",
        response_type: "code",
        code_challenge: "abc",
        code_challenge_method: "S256",
        scope: "mesh",
        state: "xyz",
      },
    });
  });

  it("shows the loopback warning when the server says every redirect URI is loopback", async () => {
    mockApi.mockResolvedValueOnce(LOOPBACK_INFO);
    renderPage();

    expect(await screen.findByText(/program on this computer/i)).toBeInTheDocument();
    expect(screen.getByTestId("redirect-host")).toHaveTextContent("127.0.0.1:5555");
  });

  it("warns on the URI actually presented even if the server's every-URI flag is false", async () => {
    // Client registered an https AND a loopback URI, and used the loopback one.
    mockApi.mockResolvedValueOnce({ ...LOOPBACK_INFO, loopback_warning: false });
    renderPage();
    expect(await screen.findByText(/program on this computer/i)).toBeInTheDocument();
  });

  it("labels the app as unverified and shows a look-alike host as punycode", async () => {
    mockApi.mockResolvedValueOnce({
      ...INFO,
      client_name: "Claude Code\u202E",
      redirect_uri: "https://cl\u0430ude.ai/cb",
      redirect_host: "claude.ai", // what a lying/naive server string would say
    });
    renderPage();

    expect(await screen.findByText(/has not checked who made it/i)).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Claude Code" })).toBeInTheDocument();
    expect(screen.getByTestId("redirect-host")).toHaveTextContent("xn--clude-5ve.ai");
  });

  it("tells the truth about what the connector can do", async () => {
    mockApi.mockResolvedValueOnce(INFO);
    renderPage();
    expect(await screen.findByText(/delete tasks/i)).toBeInTheDocument();
    expect(screen.getByText(/Attach files and post events/)).toBeInTheDocument();
    expect(screen.getByText(/workflow rules, only if you are an admin or owner/)).toBeInTheDocument();
    expect(screen.getByText(/follows your role/)).toBeInTheDocument();
  });

  it("Allow posts the chosen workspace and follows the redirect the server returns", async () => {
    mockApi.mockResolvedValueOnce(INFO);
    mockApi.mockResolvedValueOnce({ redirect_uri: "http://127.0.0.1:5555/callback?code=CODE&state=xyz" });
    renderPage();

    const select = await screen.findByLabelText("Workspace");
    fireEvent.change(select, { target: { value: INFO.workspaces[1]!.id } });
    fireEvent.click(screen.getByRole("button", { name: "Allow" }));

    await waitFor(() =>
      expect(assign).toHaveBeenCalledWith("http://127.0.0.1:5555/callback?code=CODE&state=xyz"),
    );
    const [path, opts] = mockApi.mock.calls[1]!;
    expect(path).toBe("/api/v1/oauth/consent");
    expect(opts.method).toBe("POST");
    expect(opts.body).toMatchObject({
      allow: true,
      workspace_id: INFO.workspaces[1]!.id,
      client_id: "https://claude.ai/meta",
      redirect_uri: "http://127.0.0.1:5555/callback",
      code_challenge: "abc",
      code_challenge_method: "S256",
      response_type: "code",
      scope: "mesh",
      state: "xyz",
    });
  });

  it("Deny posts allow=false and follows the access_denied redirect", async () => {
    mockApi.mockResolvedValueOnce(INFO);
    mockApi.mockResolvedValueOnce({ redirect_uri: "http://127.0.0.1:5555/callback?error=access_denied&state=xyz" });
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "Deny" }));

    await waitFor(() =>
      expect(assign).toHaveBeenCalledWith(
        "http://127.0.0.1:5555/callback?error=access_denied&state=xyz",
      ),
    );
    expect(mockApi.mock.calls[1]![1].body).toMatchObject({
      allow: false,
      workspace_id: INFO.workspaces[0]!.id,
    });
  });

  it("with no workspaces, Allow is disabled and Deny still works with a UUID the server can bind", async () => {
    mockApi.mockResolvedValueOnce({ ...INFO, workspaces: [] });
    mockApi.mockResolvedValueOnce({ redirect_uri: "http://127.0.0.1:5555/callback?error=access_denied" });
    renderPage();

    expect(await screen.findByText(/not a member of any workspace/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Allow" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Deny" }));

    await waitFor(() => expect(assign).toHaveBeenCalled());
    expect(mockApi.mock.calls[1]![1].body.workspace_id).toBe(
      "00000000-0000-0000-0000-000000000000",
    );
  });

  it("refuses to navigate to a javascript: redirect even if the server returned one", async () => {
    mockApi.mockResolvedValueOnce(INFO);
    mockApi.mockResolvedValueOnce({ redirect_uri: "javascript:alert(1)" });
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "Allow" }));

    expect(await screen.findByText(/will not open/i)).toBeInTheDocument();
    expect(assign).not.toHaveBeenCalled();
  });

  it("shows the server's refusal (e.g. viewer role) and stays on the page", async () => {
    mockApi.mockResolvedValueOnce(INFO);
    mockApi.mockRejectedValueOnce(
      Object.assign(new Error("a viewer cannot connect an external application"), {
        code: "FORBIDDEN",
        status: 403,
      }),
    );
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "Allow" }));

    expect(await screen.findByText(/role in this workspace does not allow/i)).toBeInTheDocument();
    expect(assign).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Allow" })).toBeEnabled();
  });

  it("shows an error page, never a consent form, when the request itself is invalid", async () => {
    mockApi.mockRejectedValueOnce(
      Object.assign(new Error("redirect_uri is missing or not registered for this client"), {
        code: "invalid_request",
        status: 400,
      }),
    );
    renderPage();

    expect(await screen.findByText(/Can't connect this app/)).toBeInTheDocument();
    expect(screen.getByText("This connection request is not valid.")).toBeInTheDocument();
    expect(screen.queryByText(/not registered/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Allow" })).not.toBeInTheDocument();
  });

  it("disables both buttons and the picker while a decision is in flight (no double submit)", async () => {
    mockApi.mockResolvedValueOnce(INFO);
    let resolvePost!: (v: { redirect_uri: string }) => void;
    mockApi.mockReturnValueOnce(new Promise((r) => (resolvePost = r)));
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "Allow" }));
    fireEvent.click(screen.getByRole("button", { name: "Allowing…" }));

    expect(screen.getByRole("button", { name: "Allowing…" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Deny" })).toBeDisabled();
    expect(screen.getByLabelText("Workspace")).toBeDisabled();
    expect(mockApi).toHaveBeenCalledTimes(2); // consent GET + exactly ONE decision POST

    resolvePost({ redirect_uri: "https://claude.ai/api/mcp/auth_callback?code=c" });
    await waitFor(() => expect(assign).toHaveBeenCalledTimes(1));
  });

  it("a server error on Allow shows the generic retry copy and keeps the buttons usable", async () => {
    mockApi.mockResolvedValueOnce(INFO);
    mockApi.mockRejectedValueOnce(Object.assign(new Error("boom"), { code: "X", status: 500 }));
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "Allow" }));

    expect(await screen.findByText("Could not save your choice. Try again.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Allow" })).toBeEnabled();
  });

  it("a failed load that is NOT a bad request offers Try again, and Try again reloads", async () => {
    mockApi.mockRejectedValueOnce(Object.assign(new Error("down"), { code: "X", status: 503 }));
    mockApi.mockResolvedValueOnce(INFO);
    renderPage();

    expect(await screen.findByText(/Could not load this request/)).toBeInTheDocument();
    expect(screen.queryByText("This connection request is not valid.")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));

    expect(await screen.findByRole("heading", { name: "Claude Code" })).toBeInTheDocument();
    expect(mockApi).toHaveBeenCalledTimes(2);
  });

  it("a 4xx load failure has NO retry button", async () => {
    mockApi.mockRejectedValueOnce(Object.assign(new Error("bad"), { code: "invalid_request", status: 400 }));
    renderPage();
    expect(await screen.findByText("This connection request is not valid.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Try again" })).not.toBeInTheDocument();
  });

  it("a 401 on Allow (session expired) goes to /login carrying the authorize query", async () => {
    mockApi.mockResolvedValueOnce(INFO);
    mockApi.mockRejectedValueOnce(Object.assign(new Error("Session expired"), { code: "UNAUTHORIZED", status: 401 }));
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "Allow" }));

    await waitFor(() => expect(assign).toHaveBeenCalledTimes(1));
    const target = assign.mock.calls[0]![0] as string;
    expect(target.startsWith("/login?redirect=")).toBe(true);
    expect(new URLSearchParams(target.split("?")[1]).get("redirect")).toBe(`/connect/consent?${QUERY}`);
  });

  it("an unknown client_id (401 invalid_client) shows the error page, not a login bounce", async () => {
    mockApi.mockRejectedValueOnce(
      Object.assign(new Error("invalid_client"), { code: "UNKNOWN", status: 401 }),
    );
    renderPage();

    expect(await screen.findByText("This connection request is not valid.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Allow" })).not.toBeInTheDocument();
    expect(assign).not.toHaveBeenCalled();
  });

  it("a 401 invalid_client on Allow is a plain error, not a login bounce", async () => {
    mockApi.mockResolvedValueOnce(INFO);
    mockApi.mockRejectedValueOnce(
      Object.assign(new Error("invalid_client"), { code: "UNKNOWN", status: 401 }),
    );
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "Allow" }));

    expect(await screen.findByText("Could not save your choice. Try again.")).toBeInTheDocument();
    expect(assign).not.toHaveBeenCalled();
  });

  it("a 401 on the initial load goes to /login carrying the authorize query", async () => {
    mockApi.mockRejectedValueOnce(Object.assign(new Error("Session expired"), { code: "UNAUTHORIZED", status: 401 }));
    renderPage();

    await waitFor(() => expect(assign).toHaveBeenCalledTimes(1));
    expect(new URLSearchParams((assign.mock.calls[0]![0] as string).split("?")[1]).get("redirect")).toBe(
      `/connect/consent?${QUERY}`,
    );
  });
});
