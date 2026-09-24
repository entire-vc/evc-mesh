// Static shell hand-off (perf·Б2, #2417383b).
//
// index.html paints a static copy of the login form into #root before the
// bundle loads. Its inputs are live — a person (or a password manager) can
// type into them while the JS is still downloading — and createRoot() is
// about to throw that DOM away. captureStaticShell() reads what was typed
// and where the caret was BEFORE the first render, and LoginPage seeds its
// state from it, so nothing typed is lost.

export interface StaticShellDraft {
  email: string;
  password: string;
  /** id of the shell input that had focus, if any ("email" | "password"). */
  focusedId: string | null;
}

let draft: StaticShellDraft | null = null;

export function captureStaticShell(root: HTMLElement): void {
  if (root.getAttribute("data-static-shell") !== "shell-login") return;
  const value = (id: string) =>
    (root.querySelector<HTMLInputElement>(`#${id}`)?.value ?? "");
  const active = document.activeElement;
  draft = {
    email: value("email"),
    password: value("password"),
    focusedId:
      active instanceof HTMLInputElement && root.contains(active)
        ? active.id
        : null,
  };
}

/** Values typed into the static login form, or null if there was none. */
export function peekStaticShellDraft(): StaticShellDraft | null {
  return draft;
}

/** One-shot: a later visit to /login must not see the old password again. */
export function clearStaticShellDraft(): void {
  draft = null;
}
