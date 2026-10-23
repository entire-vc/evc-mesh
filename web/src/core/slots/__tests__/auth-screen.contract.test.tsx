import { describe, it, expect, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { resolveSlotView, registerSlotView, Slot } from "../registry";
import "../mesh-default";
import type { AuthField, AuthScreenActions, AuthScreenData } from "../auth-screen/types";
import type { SlotContext } from "../types";

// CT-auth-01..08 from the spec «Каталог слотов тем v1» (auth.screen), run on
// the mesh-default view through the same `Slot` entry the containers use.

const ctx = (over: Partial<SlotContext> = {}): SlotContext => ({
  slot: "auth.screen",
  contractVersion: "1.0.0",
  locale: "en",
  mode: "light",
  isMobile: false,
  workspace: null,
  ...over,
});

const loginData = (over: Partial<AuthScreenData> = {}): AuthScreenData => ({
  mode: "login",
  status: "form",
  heading: { title: "Welcome back", subtitle: "Sign in" },
  fields: [
    { name: "email", type: "email", label: "Email", required: true, autoComplete: "email", value: "" },
    { name: "password", type: "password", label: "Password", required: true, autoComplete: "current-password", value: "" },
  ],
  submitLabel: "Sign in",
  submitting: false,
  formError: null,
  registrationEnabled: true,
  links: { login: "/login", register: "/register", forgot: null },
  footer: { text: "No account?", linkLabel: "Register", to: "/register" },
  components: {},
  ...over,
});

const mkActions = (): AuthScreenActions => ({
  setValue: vi.fn(),
  submit: vi.fn(async () => {}),
  navigate: vi.fn(),
});

function mount(data: AuthScreenData, actions = mkActions(), c = ctx()) {
  render(
    <MemoryRouter>
      <Slot name="auth.screen" data={data} actions={actions} themeSettings={{}} ctx={c} />
    </MemoryRouter>,
  );
  return actions;
}

describe("auth.screen contract on mesh-default", () => {
  it("CT-auth-01 login renders a form with labelled, typed fields", () => {
    mount(loginData());
    expect(screen.getByLabelText("Email")).toHaveAttribute("autocomplete", "email");
    expect(screen.getByLabelText("Password")).toHaveAttribute("type", "password");
    expect(document.querySelector("form")).not.toBeNull();
  });

  it("CT-auth-02 input calls setValue; the submit button submits (real Enter: e2e/auth-screen-mobile.spec.ts)", () => {
    const d = loginData();
    d.fields.forEach((f) => (f.value = "x@y.co")); // required fields filled: native validation passes
    const a = mount(d);
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "a" } });
    expect(a.setValue).toHaveBeenCalledWith("email", "a");
    // Enter in a field submits through the enclosing <form> because it has a
    // type=submit button (implicit submission); the click path is the button.
    const button = screen.getByRole("button", { name: "Sign in" });
    expect(button).toHaveAttribute("type", "submit");
    expect(button.closest("form")).toContainElement(screen.getByLabelText("Password"));
    fireEvent.click(button);
    expect(a.submit).toHaveBeenCalledTimes(1);
    fireEvent.submit(button.closest("form")!);
    expect(a.submit).toHaveBeenCalledTimes(2);
  });

  it("CT-auth-03 submitting disables the button and Enter does not resubmit", () => {
        const a = mount(loginData({ submitting: true }));
    expect(screen.getByRole("button", { name: "Sign in" })).toBeDisabled();
    fireEvent.submit(document.querySelector("form")!);
    expect(a.submit).not.toHaveBeenCalled();
  });

  it("CT-auth-04 form and field errors are announced", () => {
    const d = loginData({ formError: "Wrong password" });
    d.fields[0]!.error = "Bad email";
    mount(d);
    expect(screen.getAllByRole("alert").map((n) => n.textContent)).toEqual(["Wrong password", "Bad email"]);
    expect(screen.getByLabelText("Email")).toHaveAttribute("aria-describedby", "email-error");
  });

  it("CT-auth-05 no register link when registration is off", () => {
    mount(loginData({ registrationEnabled: false, footer: null }));
    expect(screen.queryByRole("link", { name: "Register" })).toBeNull();
  });

  it("CT-auth-06 accept-invite shows the invite details", () => {
    mount(
      loginData({
        mode: "accept-invite",
        heading: { title: "Accept", subtitle: "Join Acme", detail: "Invited by Ann" },
        footer: null,
      }),
    );
    expect(screen.getByText("Join Acme")).toBeInTheDocument();
    expect(screen.getByText("Invited by Ann")).toBeInTheDocument();
  });

  it("CT-auth-07 workspace is null and the view does not touch it", () => {
    expect(() => mount(loginData(), mkActions(), ctx({ workspace: null }))).not.toThrow();
  });

  it("CT-auth-08 mobile+dark render with fields inside one full-width column", () => {
    // jsdom has no layout: the real 393px overflow check lives in
    // e2e/auth-screen-mobile.spec.ts. Here: no fixed-pixel widths in the tree.
    mount(loginData(), mkActions(), ctx({ isMobile: true, mode: "dark" }));
    const all = [...document.body.querySelectorAll("*")];
    expect(all.some((n) => /(^|\s)w-\[\d+px\]/.test(n.getAttribute("class") ?? ""))).toBe(false);
    expect(screen.getByRole("button", { name: "Sign in" })).toHaveClass("w-full");
  });

  it("CT-common-01 loading and invalid states are not empty; invalid offers navigation", () => {
    mount(loginData({ mode: "accept-invite", status: "loading", heading: { title: "Loading…" }, fields: [], footer: null }));
    expect(screen.getByText("Loading…")).toBeInTheDocument();
    const a = mkActions();
    mount(
      loginData({
        mode: "accept-invite",
        status: "invalid",
        heading: { title: "Not found", subtitle: "Expired" },
        fields: [],
        footer: { linkLabel: "Go to sign in", to: "/login" },
      }),
      a,
    );
    fireEvent.click(screen.getByRole("button", { name: "Go to sign in" }));
    expect(a.navigate).toHaveBeenCalledWith("/login");
  });
});

describe("auth.screen: logic does not live in the view", () => {
  it("a stub view with a different layout drives the same actions", () => {
        registerSlotView("auth.screen", "stub", ({ data, actions }) => (
      <aside>
        <h2>{data.heading.title}</h2>
        {data.fields.map((f: AuthField) => (
          <input key={f.name} aria-label={f.label} value={f.value} onChange={(e) => actions.setValue(f.name, e.target.value)} />
        ))}
        <button onClick={() => void actions.submit()}>go</button>
      </aside>
    ));
    const a = mkActions();
    render(
      <MemoryRouter>
        <Slot name="auth.screen" themeId="stub" data={loginData()} actions={a} themeSettings={{}} ctx={ctx()} />
      </MemoryRouter>,
    );
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "x" } });
    fireEvent.click(screen.getByText("go"));
    expect(a.setValue).toHaveBeenCalledWith("email", "x");
    expect(a.submit).toHaveBeenCalledTimes(1);
  });

  it("CT-common-07 a view that throws falls back to mesh-default", () => {
    registerSlotView("auth.screen", "broken", () => {
      throw new Error("boom");
    });
    vi.spyOn(console, "error").mockImplementation(() => {});
    render(
      <MemoryRouter>
        <Slot name="auth.screen" themeId="broken" data={loginData()} actions={mkActions()} themeSettings={{}} ctx={ctx()} />
      </MemoryRouter>,
    );
    expect(screen.getByLabelText("Email")).toBeInTheDocument();
    expect(resolveSlotView("auth.screen", "broken")).toBeDefined();
  });
});
