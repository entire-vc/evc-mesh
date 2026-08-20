#!/usr/bin/env python3
"""Fail when a commit message about to reach `main` carries a tool-attribution trailer.

Commit messages here must not carry `Co-Authored-By: <tool>` or
`Generated with/by <tool>` for any name in .github/vendor-trailer-terms.txt.

Unlike a pull-request body or a comment, a commit message cannot be edited after
the fact: it is immutable content addressed by its SHA, and the only remedy once
it lands is rewriting history and force-pushing over every clone and fork. So
the whole of the effort goes into refusing it beforehand.

Two contexts, and they see genuinely different bytes
----------------------------------------------------

* ``pull_request`` — reads the commits ON THE BRANCH. Under this repository's
  squash-merge setting GitHub composes the squash body by concatenating those
  messages and then appending a ``Co-authored-by:`` block derived from their
  trailers, so a trailer on a branch commit reaches `main` twice over. Catching
  it here is what stops the PR before it can even enter the merge queue.

* ``merge_group`` — reads the commits the QUEUE IS ABOUT TO MERGE, which are the
  squash commits themselves. This is the only context that sees a squash message
  a human retyped in the web merge box, which the branch commits cannot reveal.

Both are needed, and neither subsumes the other. A required check that reports
nothing on ``merge_group`` also wedges the queue permanently — every entry waits
for a context no workflow ever produces — which is why this runs there even in
the cases where it has nothing new to say.

Known residual gap, stated rather than implied
----------------------------------------------

A merge performed with the web button on a repository *without* the queue would
be seen only by the ``pull_request`` run, so a hand-retyped squash body could
still get through. On this repository the queue is enabled and this check is
required, so that path is closed here; it is named because the same file may be
copied to a repository where it is not.
"""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys

TERMS_FILE = ".github/vendor-trailer-terms.txt"


def fail(message: str) -> None:
    print(f"::error::{message}")
    sys.exit(1)


def load_vendor_pattern() -> re.Pattern[str]:
    """Build the detector from the data file. Fails closed if it cannot.

    An unreadable term list is not "nothing is attributed" — it is "we do not
    know", and a gate whose whole job is refusing an unknown must not treat its
    own missing input as a clean result.
    """
    try:
        with open(TERMS_FILE, encoding="utf-8") as handle:
            raw = handle.read()
    except OSError as err:
        fail(f"cannot read {TERMS_FILE} ({err}); refusing rather than assuming nothing is attributed")

    vendors = [
        line.strip()
        for line in raw.splitlines()
        if line.strip() and not line.lstrip().startswith("#")
    ]
    if not vendors:
        fail(f"{TERMS_FILE} lists no vendors; a gate that can never fire is not a gate")

    # Longest first, so "Claude Code" is preferred over "Claude" and the error
    # message names the specific thing that was found.
    alternation = "|".join(re.escape(v) for v in sorted(vendors, key=len, reverse=True))

    # Two shapes, both anchored on the ATTRIBUTION, never on the bare name.
    #
    # The leading `.{0,4}` absorbs the emoji some tools prefix. The trailing
    # `.*` is load-bearing and must not become an end anchor: an earlier version
    # of the fleet detector ended at `\)\s*$` and so missed
    # `Generated with [<vendor>] (<name>)` — under-reporting by precisely the
    # worst item in the corpus. Never anchor a leak detector on the tail of a
    # line the author can freely append to.
    return re.compile(
        rf"^.{{0,4}}Generated (?:with|by) \[?(?:{alternation})\b.*$"
        rf"|^Co-Authored-By:\s*(?:{alternation})\b.*$",
        re.I | re.M,
    )


def self_test(pattern: re.Pattern[str]) -> None:
    """A zero must mean "clean", never "dead predicate".

    Without this, a typo in the term file or a bad escape produces a pattern
    that matches nothing, and every run reports success. That failure is
    invisible precisely when the gate matters most, so the gate refuses to
    report at all until it has proven it can still say both yes and no.
    """
    must_fire = (
        "fix(x): thing\n\nCo-Authored-By: Claude Opus 5 <noreply@anthropic.com>",
        "feat: y\n\n\U0001f916 Generated with [Claude Code](https://claude.com/claude-code)",
        "chore: z\n\nCo-authored-by: Claude <noreply@anthropic.com>",
    )
    must_not_fire = (
        # A real human co-author. The single most costly false positive there is:
        # it would redden ordinary collaboration and get the gate switched off.
        "fix(x): thing\n\nCo-Authored-By: appset <284729773+robert-j-evc@users.noreply.github.com>",
        # The bare vendor name as ordinary product documentation.
        "docs: config examples for Claude Code, Codex CLI, OpenCode",
        "chore: routine commit with no trailer at all",
    )
    problems = []
    for message in must_fire:
        if not pattern.search(message):
            problems.append(f"detector did not fire on a known-attributed message: {message!r}")
    for message in must_not_fire:
        if pattern.search(message):
            problems.append(f"detector fired on a known-clean message: {message!r}")
    if problems:
        fail(
            "self-test failed, so this run cannot distinguish a clean history from a "
            "broken detector: " + "; ".join(problems)
        )


def gh_api(path: str) -> object:
    """One authenticated API read. A failure is fatal, never an empty answer."""
    try:
        out = subprocess.run(
            ["gh", "api", "--paginate", path],
            check=True,
            capture_output=True,
            text=True,
        ).stdout
    except subprocess.CalledProcessError as err:
        fail(f"GET {path} failed: {err.stderr.strip()[:400]}")
    try:
        return json.loads(out)
    except json.JSONDecodeError as err:
        fail(f"GET {path} returned something that is not JSON ({err})")


def commits_under_test(repo: str, event: str) -> list[tuple[str, str]]:
    """(sha, message) for every commit this run is responsible for."""
    if event == "pull_request":
        number = os.environ.get("PR_NUMBER", "")
        if not number.isdigit():
            fail(f"no pull request number on a {event} event (got {number!r})")
        payload = gh_api(f"repos/{repo}/pulls/{number}/commits?per_page=100")
        commits = [(c["sha"], c["commit"]["message"]) for c in payload]
        if not commits:
            fail(
                f"pull request #{number} reported zero commits; refusing rather than "
                "passing a pull request whose contents were never read"
            )
        return commits

    if event == "merge_group":
        base = os.environ.get("MERGE_GROUP_BASE_SHA", "")
        head = os.environ.get("MERGE_GROUP_HEAD_SHA", "")
        if not base or not head:
            fail(
                f"merge group gave no base/head to compare (base={base!r}, head={head!r}); "
                "refusing rather than merging a batch whose messages were never read"
            )
        compare = gh_api(f"repos/{repo}/compare/{base}...{head}")
        commits = [(c["sha"], c["commit"]["message"]) for c in compare.get("commits", [])]
        if not commits:
            fail(
                f"merge group {base[:7]}...{head[:7]} contains no commits; refusing, because "
                "an empty comparison and an unreadable one look identical from here"
            )
        return commits

    fail(f"unexpected event {event!r}; this gate only knows pull_request and merge_group")
    return []  # unreachable; keeps type checkers quiet


def main() -> None:
    pattern = load_vendor_pattern()
    self_test(pattern)

    repo = os.environ["REPO"]
    event = os.environ.get("EVENT_NAME", "")
    commits = commits_under_test(repo, event)

    offenders: list[str] = []
    for sha, message in commits:
        hits = sorted({hit.strip() for hit in pattern.findall(message) or []})
        # findall with alternation groups returns the whole match here because
        # every group is non-capturing, so `hits` holds the offending lines.
        if pattern.search(message):
            subject = message.splitlines()[0] if message.splitlines() else ""
            offenders.append(f"{sha[:7]} {subject[:72]}")
            print(f"ATTRIBUTED  {sha[:7]}  {subject[:72]}")
            for hit in hits:
                print(f"              → {hit}")
        else:
            subject = message.splitlines()[0] if message.splitlines() else ""
            print(f"clean       {sha[:7]}  {subject[:72]}")

    if offenders:
        fail(
            f"{len(offenders)} of {len(commits)} commit message(s) carry a tool-attribution trailer for a "
            "development tool:\n  "
            + "\n  ".join(offenders)
            + "\n\nRewrite the messages and force-push the branch — for example "
            "`git rebase -i --exec 'git commit --amend --no-edit --trailer \"Co-Authored-By\" -'` "
            "or simply amend each message by hand. A commit message cannot be fixed after it "
            "reaches main: it is addressed by its SHA, so the only remedy there is rewriting "
            "public history. That is why this refuses now rather than reporting later."
        )

    print(f"\nAll {len(commits)} commit message(s) clean — no vendor attribution.")


if __name__ == "__main__":
    main()
