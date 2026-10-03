#!/usr/bin/env python3
"""Refuse MR-branch commits whose author or committer is not a known lane.

Why: a lane session can carry a foreign GIT_AUTHOR_*/GIT_COMMITTER_* in its
environment (a showcase persona, or the literal `unconfigured` placeholder).
Environment beats repo/worktree `user.*` config, so commits silently land under
the wrong identity; the workaround "export your own vars before every commit"
rests on memory, not on a check. This job is the check.

Scope: every commit in `origin/<target>..HEAD` (same set, same local-git
method as vendor_trailer_gate.py — see its header for why not the GitLab API).
Both author and committer e-mail must be in `.gitlab/commit-author-allowlist.txt`
as it exists on the TARGET branch (`origin/<target>`), never the MR's own copy:
otherwise an MR could allowlist its own foreign identity. If the target has
no allowlist the job fails: the allowlist lands on the target in its own MR first.
Exceptions are explicit lines in that file; there is no skip flag.

Fail-closed: a missing/empty allowlist, an unreachable target ref or an empty
commit range that git cannot explain is an error, not a pass.
"""

from __future__ import annotations

import contextlib
import io
import os
import subprocess
import sys

ALLOWLIST = ".gitlab/commit-author-allowlist.txt"
SEP = "\x1f"


def fail(message: str) -> None:
    print(f"commit-author-gate: FAIL — {message}", file=sys.stderr)
    sys.exit(1)


def parse_allowlist(text: str) -> tuple[set[str], set[str]]:
    emails: set[str] = set()
    shas: set[str] = set()
    for raw in text.splitlines():
        line = raw.split("#", 1)[0].strip()
        if not line:
            continue
        if line.startswith("sha:"):
            shas.add(line[4:].strip().lower())
        else:
            emails.add(line.lower())
    return emails, shas


def violations(commits: list[tuple[str, str, str, str, str]], emails: set[str], shas: set[str]) -> list[str]:
    """commits: (sha, author_name, author_email, committer_name, committer_email)."""
    out = []
    for sha, an, ae, cn, ce in commits:
        if sha.lower() in shas:
            continue
        for role, name, email in (("author", an, ae), ("committer", cn, ce)):
            if email.lower() not in emails:
                out.append(
                    f"{sha} {role} is `{ascii(name)[1:-1]} <{ascii(email)[1:-1]}>` — expected one of the lane identities "
                    f"in {ALLOWLIST}: {', '.join(sorted(emails))}"
                )
    return out


def self_test() -> None:
    """A gate that cannot go red is not a gate: feed known-bad input first."""
    emails, shas = parse_allowlist("a@entire.vc\n# c\nsha:ABC # waived\n")
    good = ("1" * 40, "A", "a@entire.vc", "A", "A@entire.vc")
    bad_author = ("2" * 40, "R", "1+r@users.noreply.github.com", "A", "a@entire.vc")
    bad_committer = ("3" * 40, "A", "a@entire.vc", "u", "unconfigured@entire.vc")
    waived = ("abc", "R", "x@y", "R", "x@y")
    got = violations([good, bad_author, bad_committer, waived], emails, shas)
    ok = (
        len(got) == 2
        and got[0].startswith("2" * 40 + " author")
        and got[1].startswith("3" * 40 + " committer")
    )
    if not ok:
        fail(f"self-test broken, cannot tell clean from dirty history: {got}")
    esc = violations([("5" * 40, "x\x1b[2Jy", "e\x1b@z", "A", "a@entire.vc")], emails, shas)
    if len(esc) != 1 or "\x1b" in esc[0] or "\\x1b" not in esc[0]:
        fail(f"self-test broken: control characters in commit metadata not escaped: {esc}")


def parse_rows(lines: list[str]) -> list[tuple[str, ...]]:
    """Split `git log` rows; a row that does not parse is a failure, never skipped."""
    rows = [ln.split(SEP) for ln in lines]
    malformed = [r for r in rows if len(r) != 5]
    if malformed:
        # Names are untrusted: one containing the separator shifts the fields.
        fail("commit metadata does not parse (separator inside a name/email?), refusing to skip: "
             + ", ".join(ascii(r[0]) for r in malformed))
    return [tuple(r) for r in rows]


def self_test_rows() -> None:
    """A name carrying the separator must be rejected, not silently dropped."""
    evil = SEP.join(["4" * 40, "evil", "name", "1+r@x", "c", "1+r@x"])
    try:
        with contextlib.redirect_stderr(io.StringIO()):  # expected failure, keep it out of a green log
            parse_rows([evil])
    except SystemExit:
        return
    fail("self-test broken: malformed row not detected")


def git(*args: str) -> str:
    r = subprocess.run(["git", *args], capture_output=True, text=True)
    if r.returncode != 0:
        fail(f"`git {' '.join(args)}` exited {r.returncode}: {r.stderr.strip()}")
    return r.stdout


def main() -> None:
    self_test()
    self_test_rows()
    target = os.environ.get("CI_MERGE_REQUEST_TARGET_BRANCH_NAME")
    if not target:
        fail("CI_MERGE_REQUEST_TARGET_BRANCH_NAME unset — this job only runs in merge-request pipelines")
    git("rev-parse", "--verify", f"origin/{target}")
    shown = subprocess.run(["git", "show", f"origin/{target}:{ALLOWLIST}"], capture_output=True, text=True)
    if shown.returncode == 0:
        text = shown.stdout
        print(f"commit-author-gate: allowlist taken from origin/{target}")
    else:
        # Fail closed: an MR must never supply the policy that approves itself.
        fail(f"{ALLOWLIST} is absent on origin/{target} — land the allowlist on {target} first, in its own MR")
    emails, shas = parse_allowlist(text)
    if not emails:
        fail(f"{ALLOWLIST} lists no identities")
    fmt = SEP.join(["%H", "%an", "%ae", "%cn", "%ce"])
    commits = parse_rows(git("log", f"--format={fmt}", f"origin/{target}..HEAD").splitlines())
    bad = violations(commits, emails, shas)
    print(f"commit-author-gate: checked {len(commits)} commit(s) on origin/{target}..HEAD")
    if bad:
        print("\n".join(bad), file=sys.stderr)
        fail(
            f"{len(bad)} identity violation(s). Fix: export your own GIT_AUTHOR_NAME/EMAIL and "
            "GIT_COMMITTER_NAME/EMAIL (check with `git var GIT_AUTHOR_IDENT`), "
            "then `git rebase -r origin/<target> --exec 'git commit --amend --reset-author --no-edit'` and force-with-lease."
        )
    print("commit-author-gate: OK")


if __name__ == "__main__":
    main()
