#!/usr/bin/env python3
"""GitLab equivalent of `.github/scripts/vendor_trailer_gate.py`.

Read that file first — the detector regex, the term-list contract, and the
self-test below are copied from it on purpose (same shape, same reasoning);
only the parts that are genuinely different for GitLab are explained here.

What GitHub does that GitLab does not
--------------------------------------

GitHub runs on two events: `pull_request` reads the commits ON THE BRANCH;
`merge_group` reads the commits the queue is about to write, because (a) a
human can retype the squash body in the merge box, and (b) GitHub's own
squash message is built by CONCATENATING the branch commit messages and then
APPENDING a `Co-authored-by:` trailer derived from them — so a branch-commit
trailer reaches `main` even when nobody touches the box.

Neither of those is true on GitLab. Measured against this project's history:
three squash-merges whose branch commit carried `Co-Authored-By: Claude ...`
(a72eac8, 3a3e9ca, 6a604f6) all landed on `main` via `~/bin/glab-merge`'s
default `--squash` with **zero** trace of it (99d8429, f97fcec, 170f0f7) —
GitLab's squash commit message is the MR TITLE ONLY, body discarded, nothing
re-appended. Also measured: 111 commits merged to `main` since the
2026-08-23 GitLab-canon cutover carry the trailer zero times; of 45 commits
across the merge requests open right now, exactly one does, and that one is
an explicit throwaway probe commit never intended to merge. So the
branch-commit check below is not reproducing what lands on `main`
byte-for-byte — it can't be, GitLab discards the evidence at squash time —
it is checking whether the AUTHOR followed the convention, which is the only
question left once the squash body can no longer answer it either way, and
which the numbers above say is already the fleet's actual practice.

How the commit list is obtained — git, not the GitLab API
-----------------------------------------------------------

The first version of this file read `GET .../merge_requests/:iid/commits`
with `CI_JOB_TOKEN` in a `JOB-TOKEN` header, mirroring how the GitHub script
calls `gh api repos/:repo/pulls/:number/commits`. Live-tested against a real
merge request pipeline it failed with `404 Project Not Found` — this
project's CI/CD job-token scope does not grant that job read access to its
own project's REST API (changing that needs Maintainer; every agent account
here is Developer). Rather than depend on a permission this job cannot
verify or request for itself, it computes the same list locally: the runner
already checks out this branch's tip as `HEAD` for a merge-request pipeline,
so `git log origin/<target>..HEAD` is exactly "every commit this MR would
add, not yet on the target branch" — the same set the API call would have
returned, using data already present in the job's own working copy. The job
sets `GIT_DEPTH: "0"` (full clone, no shallow-history edge cases) precisely
so this diff is never wrong because history got cut off.

Known residual gap — same shape as GitHub's own accepted one
--------------------------------------------------------------

GitHub's script names its own gap: a hand-retyped squash body on a
repository without a merge queue is invisible to it, because no event ever
exposes that text before it lands. GitLab has no equivalent of a pipeline
that sees the literal contents of the merge-widget's message box before the
write — editing that box does not re-trigger a pipeline, on this project or,
so far as this gate's author could find, on any GitLab tier. That edit box
is this gate's own named residual gap. It is not closed by this file; it is
stated, per the standing rule that an unstated gap gets believed shut.
"""

from __future__ import annotations

import os
import re
import subprocess
import sys

TERMS_FILE = ".github/vendor-trailer-terms.txt"  # reused, not copied — see that file's own header


def fail(message: str) -> None:
    print(f"ERROR: {message}")
    sys.exit(1)


def load_vendor_pattern() -> re.Pattern[str]:
    """Build the detector from the data file. Fails closed if it cannot.

    An unreadable term list is not "nothing is attributed" — it is "we do not
    know", and a gate whose whole job is refusing an unknown must not treat
    its own missing input as a clean result.
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

    alternation = "|".join(re.escape(v) for v in sorted(vendors, key=len, reverse=True))
    return re.compile(
        rf"^.{{0,4}}Generated (?:with|by) \[?(?:{alternation})\b.*$"
        rf"|^Co-Authored-By:\s*(?:{alternation})\b.*$",
        re.I | re.M,
    )


def self_test(pattern: re.Pattern[str]) -> None:
    """A zero must mean "clean", never "dead predicate". Same rationale as
    the GitHub script's self-test — copied verbatim rather than re-derived."""
    must_fire = (
        "fix(x): thing\n\nCo-Authored-By: Claude Opus 5 <noreply@anthropic.com>",
        "feat: y\n\n\U0001f916 Generated with [Claude Code](https://claude.com/claude-code)",
        "chore: z\n\nCo-authored-by: Claude <noreply@anthropic.com>",
    )
    must_not_fire = (
        "fix(x): thing\n\nCo-Authored-By: Garfield Stoun <garfieldstoun@users.noreply.github.com>",
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


def run_git(*args: str) -> str:
    try:
        return subprocess.run(
            ["git", *args], check=True, capture_output=True, text=True
        ).stdout
    except subprocess.CalledProcessError as err:
        fail(f"`git {' '.join(args)}` failed: {err.stderr.strip()[:400]}")
        raise  # unreachable; keeps type checkers quiet


def branch_commits(target: str) -> list[tuple[str, str]]:
    """(sha, full message) for every commit on HEAD not on origin/<target>."""
    run_git("fetch", "--quiet", "origin", target)
    # \x00 separates sha from message, \x03 separates records — neither can
    # appear in a git commit message, unlike a plain newline.
    raw = run_git("log", f"--format=%H%x00%B%x03", f"origin/{target}..HEAD")
    records = [r.lstrip("\n") for r in raw.split("\x03") if r.strip("\n")]
    if not records:
        fail(
            f"HEAD reported zero commits ahead of origin/{target}; refusing rather than "
            "passing a merge request whose contents were never read"
        )
    commits = []
    for rec in records:
        sha, _, msg = rec.partition("\x00")
        commits.append((sha, msg))
    return commits


def main() -> None:
    pattern = load_vendor_pattern()
    self_test(pattern)

    target = os.environ.get("CI_MERGE_REQUEST_TARGET_BRANCH_NAME", "")
    if not target:
        fail(
            "no CI_MERGE_REQUEST_TARGET_BRANCH_NAME; this gate only runs on "
            "merge_request_event pipelines, where it is guaranteed set"
        )

    commits = branch_commits(target)

    offenders: list[str] = []
    for sha, message in commits:
        subject = message.splitlines()[0] if message.splitlines() else ""
        if pattern.search(message):
            offenders.append(f"{sha[:8]} {subject[:72]}")
            print(f"ATTRIBUTED  {sha[:8]}  {subject[:72]}")
            for hit in sorted({h.strip() for h in pattern.findall(message) or []}):
                print(f"              -> {hit}")
        else:
            print(f"clean       {sha[:8]}  {subject[:72]}")

    if offenders:
        fail(
            f"{len(offenders)} of {len(commits)} commit message(s) on this branch carry a "
            "tool-attribution trailer for a development tool:\n  "
            + "\n  ".join(offenders)
            + "\n\nRewrite the messages and force-push the branch — for example "
            "`git rebase -i --exec 'git commit --amend --no-edit --trailer \"Co-Authored-By\" -'` "
            "or simply amend each message by hand."
        )

    print(f"\nAll {len(commits)} commit message(s) clean -- no vendor attribution.")


if __name__ == "__main__":
    main()
