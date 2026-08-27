#!/usr/bin/env python3
"""Refuse to score unless we can name the binary that served the measurement.

The recall gate measures a LIVE endpoint (`secrets.MESH_API_URL`). `memory-bench.yml`
triggers on `push: branches:[main]`; so does `deploy-backend.yml`. Nothing sequences
them — no `needs:`, no version pin — so a push-triggered gate run spends its first
minutes measuring the PREVIOUS binary and then the binary swaps underneath it.

That is not a hypothetical. Measured 2026-07-28, every run that night crossed a
deploy:

    gate run     started    deploy in flight   window
    30323425190  02:33:02   f76ebad            02:30:52 -> 02:37:59
    30324665531  02:59:33   f161732            02:59:33 -> 03:05:38
    30325802161  03:23:43   de52f76            03:37:14 -> 03:43:53
    30326419253  03:37:14   de52f76            03:37:14 -> 03:43:53

Run 30325802161 was dispatched at 03:23:43 *specifically* to be a clean
post-deploy run; prod went f161732 -> de52f76 at 03:43:10Z, inside it. A score
straddling two binaries is not a measurement of either, and the harness had no
way to say so — it reported a number.

So: a run that cannot confirm which binary it measured must not produce a score.
`cannot measure => exit 2`, the same rule the rest of this harness follows. Never
exit 1: a version race is infrastructure, and a required check that goes red at
an author who cannot clear it is a check people learn to bypass (cf. #342).

## Why "equal" is the wrong test on its own

`memory-bench.yml` fires on pushes touching `scripts/memory-bench/**`.
`deploy-backend.yml` does NOT — its `paths:` are `cmd/**`, `internal/**`,
`go.mod`, `go.sum`, `migrations/**`. A push that only edits the harness therefore
triggers a gate run and no deploy at all, and prod legitimately keeps serving an
earlier commit. Requiring `served == github.sha` would wedge exactly the pushes
that change the gate itself at INCONCLUSIVE, for ever.

The property that actually matters is not "same commit" but "same SERVER CODE":

    served == expected                                  -> exact
    served is an ancestor of expected, and the two do
    not differ in any path that deploy-backend deploys  -> equivalent
    anything else                                       -> refuse

and the deployable path list is READ OUT OF `deploy-backend.yml` rather than
copied here. A second hand-maintained copy of that list is the `MEMORY_PATHS`
failure mode one file over: it drifts, and the drift shows up as a gate that
certifies a run it never made. If the list cannot be parsed we fall back to
strict equality — loud and wrong-way-safe — and `test_serving_version.py` pins
the parse against the real file so drift breaks a test instead of silently
selecting the fallback.

Usage:
    # Pin: wait until prod serves the code under test, else refuse.
    python3 check_serving_version.py --expect <sha> [--wait-seconds 900]

    # Record: print the serving commit without requiring anything of it.
    python3 check_serving_version.py --record

    # Stability: the commit must not have changed since --was.
    python3 check_serving_version.py --was <sha>

Exit codes:
    0  attributable — the serving commit is known and acceptable
    2  NOT attributable — reason on stdout as `GATE_REASON: <kind> - <detail>`
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parent
REPO_ROOT = SCRIPT_DIR.parent.parent
DEPLOY_WORKFLOW = REPO_ROOT / ".github" / "workflows" / "deploy-backend.yml"

# Mirrors run_ci.py. Restated rather than imported: this module is also run by
# the stdlib-only self-check step, before `pip install`, and importing run_ci
# would drag `mcp`/`httpx` in with it.
EXIT_OK = 0
EXIT_INCONCLUSIVE = 2
REASON_PREFIX = "GATE_REASON:"

# Three kinds, not one. The workflow's alert dedups on reason KIND, so folding
# these together would let a live alert for one suppress the arrival of another —
# and they have different owners and different fixes:
#
#   version-mismatch        prod is serving code that is not the code under test.
#                           Fix: let the deploy land, then re-run. Nobody's bug.
#   version-changed-mid-run a deploy landed DURING the measurement, so the score
#                           spans two binaries. Fix: re-run in a quiet window.
#                           Distinct from the above because the run did start
#                           attributable — the wait worked and was still not enough.
#   version-unreadable      /api/version did not answer, or answered without a
#                           commit. Fix: the endpoint, not the gate. Not the same
#                           as "unreachable Mesh" — recall may well be serving.
REASON_VERSION_MISMATCH = "version-mismatch"
REASON_VERSION_CHANGED = "version-changed-mid-run"
REASON_VERSION_UNREADABLE = "version-unreadable"

DEFAULT_WAIT_SECONDS = 900
DEFAULT_POLL_SECONDS = 15
DEFAULT_HTTP_TIMEOUT = 10

_SHA_RE = re.compile(r"^[0-9a-f]{7,40}$")


class VersionUnreadable(Exception):
    """`/api/version` could not be read, or carried no commit."""


# ---------------------------------------------------------------------------
# Reading the live endpoint
# ---------------------------------------------------------------------------


def version_url(base_url: str) -> str:
    return base_url.rstrip("/") + "/api/version"


def fetch_serving_commit(base_url: str, timeout: float = DEFAULT_HTTP_TIMEOUT) -> str:
    """Return the full commit sha `/api/version` reports, or raise.

    stdlib only on purpose: the gate self-checks run before `pip install`, and a
    guard that can only be tested after the dependency step is a guard whose
    tests get skipped the first time the dependency step breaks.
    """
    url = version_url(base_url)
    try:
        with urllib.request.urlopen(url, timeout=timeout) as resp:  # noqa: S310 -- nosemgrep: python.lang.security.audit.dynamic-urllib-use-detected.dynamic-urllib-use-detected (dev/bench tooling; url is version_url(base_url), an operator-supplied CLI arg, not attacker input)
            body = resp.read().decode("utf-8", "replace")
    except (urllib.error.URLError, OSError, TimeoutError) as exc:
        raise VersionUnreadable(f"{url} did not answer ({exc})") from exc

    try:
        payload = json.loads(body)
    except json.JSONDecodeError as exc:
        raise VersionUnreadable(f"{url} did not return JSON ({exc})") from exc

    commit = (payload or {}).get("commit", "") if isinstance(payload, dict) else ""
    if not isinstance(commit, str) or not _SHA_RE.match(commit.strip().lower()):
        # `build_time` is deliberately NOT used as a fallback: it records when the
        # binary was BUILT, not when it began serving, so a build_time that looks
        # recent says nothing about which binary answered this request.
        raise VersionUnreadable(f"{url} returned no usable `commit` field (got {commit!r})")
    return commit.strip().lower()


# ---------------------------------------------------------------------------
# What counts as "the same server code"
# ---------------------------------------------------------------------------


def parse_deploy_paths(text: str) -> list[str] | None:
    """Extract `on.push.paths` from deploy-backend.yml, or None if unparseable.

    A deliberately small reader rather than a YAML dependency — this runs in the
    stdlib-only step. It returns None (not an empty list) when it cannot find the
    block, because "could not tell" and "the list is empty" are different states
    that must not collapse. Neither is permissive — `git diff A B --` with no
    pathspec diffs EVERYTHING, so `[]` would block on any change at all — but they
    are wrong in different ways: `[]` would silently wedge a harness-only push
    (which is the case `classify` exists to let through), while None says so and
    falls back to a stated exact-match rule.
    """
    lines = text.splitlines()
    in_push = False
    push_indent = 0
    paths: list[str] | None = None
    for raw in lines:
        line = raw.rstrip()
        if not line.strip() or line.strip().startswith("#"):
            continue
        indent = len(line) - len(line.lstrip())
        stripped = line.strip()

        if stripped == "push:":
            in_push = True
            push_indent = indent
            continue
        if in_push and indent <= push_indent and not stripped.startswith("-"):
            if stripped == "paths:":
                paths = []
                continue
            # Left the push: block entirely (e.g. `workflow_dispatch:`).
            if paths is None:
                in_push = False
            else:
                break
            continue
        if in_push and stripped == "paths:":
            paths = []
            continue
        if paths is not None and stripped.startswith("- "):
            paths.append(stripped[2:].strip().strip("'\""))
            continue
        if paths is not None and not stripped.startswith("- "):
            break
    if paths is not None and not any(not p.startswith("!") for p in paths):
        # A list of nothing but exclusions matches no push at all, so "what does
        # this deploy?" has no answer to read off it. Collapse to the stated
        # cannot-tell fallback rather than hand git a pathspec of pure negations,
        # which would diff EVERYTHING-except and read as "the whole tree deploys".
        return None
    return paths or None


def to_git_pathspec(paths: list[str]) -> list[str]:
    """Translate GitHub `on.push.paths` entries into git pathspecs.

    GitHub writes an exclusion with a leading `!`; git wants its `:!` magic
    prefix. A bare `!pattern` handed to `git diff -- …` is not an exclusion at
    all — it is an ordinary pattern that matches nothing, so the exclusion
    evaporates silently and every file it was meant to exclude keeps counting as
    deployable.

    That is the direction that refuses FOREVER, not the one that waves things
    through. evc-mesh#408 stopped `*_test.go` merges from deploying, so after a
    test-only merge prod stays behind ON PURPOSE. A pin that still believes that
    file deploys reports "a deploy is due and has not landed" about a deploy that
    will never land, polls to timeout, and refuses with no event able to clear
    it — the same shape as the `!=` waiter this tool exists to replace.

    The two languages are not identical: GitHub is last-match-wins, git's `:!`
    excludes unconditionally. They agree whenever negations come last, which is
    what `deploy-backend.yml` requires of itself in a comment. Where they could
    disagree — a positive pattern re-including after a negation — this errs
    toward treating the file as NOT deployable, i.e. toward measuring rather than
    stalling. `test_serving_version.py` pins the agreement against the real
    workflow so a future edit to either side cannot desynchronise them quietly.
    """
    spec: list[str] = []
    for p in paths:
        spec.append(":!" + p[1:] if p.startswith("!") else p)
    return spec


def deploy_paths() -> list[str] | None:
    try:
        return parse_deploy_paths(DEPLOY_WORKFLOW.read_text())
    except OSError:
        return None


def _git(*args: str) -> subprocess.CompletedProcess:
    return subprocess.run(
        ["git", *args],
        cwd=REPO_ROOT,
        capture_output=True,
        text=True,
        check=False,
    )


def classify(served: str, expected: str, paths: list[str] | None) -> tuple[bool, str]:
    """Is a server running `served` an acceptable stand-in for `expected`?

    Returns (acceptable, human-readable why).
    """
    served = served.lower()
    expected = expected.lower()
    if served == expected or (
        len(served) != len(expected) and (served.startswith(expected) or expected.startswith(served))
    ):
        return True, "exact — prod is serving the commit under test"

    if paths is None:
        return False, (
            "cannot tell: deploy-backend.yml `on.push.paths` did not parse, so the "
            "gate fell back to requiring an exact commit match"
        )

    # Both commits must be present locally. `fetch-depth: 0` on the checkout makes
    # this true in CI; if it is not, we refuse rather than guess.
    for sha in (served, expected):
        if _git("cat-file", "-e", f"{sha}^{{commit}}").returncode != 0:
            return False, f"commit {sha[:7]} is not in this checkout, so it cannot be compared"

    if _git("merge-base", "--is-ancestor", served, expected).returncode != 0:
        return False, (
            f"prod's {served[:7]} is NOT an ancestor of {expected[:7]} — it is running "
            "code that is not on the line under test"
        )

    diff = _git("diff", "--name-only", served, expected, "--", *to_git_pathspec(paths))
    if diff.returncode != 0:
        return False, f"could not diff {served[:7]}..{expected[:7]} ({diff.stderr.strip()})"
    changed = [ln for ln in diff.stdout.splitlines() if ln.strip()]
    if changed:
        return False, (
            f"{served[:7]}..{expected[:7]} changes {len(changed)} deployable file(s) "
            f"(e.g. {changed[0]}) — a deploy is due and has not landed"
        )
    return True, (
        f"equivalent — {served[:7]} is an ancestor of {expected[:7]} and nothing "
        "deploy-backend.yml deploys differs between them"
    )


# ---------------------------------------------------------------------------
# Polling
# ---------------------------------------------------------------------------


def wait_for_version(
    base_url: str,
    expected: str,
    *,
    wait_seconds: float = DEFAULT_WAIT_SECONDS,
    poll_seconds: float = DEFAULT_POLL_SECONDS,
    paths: list[str] | None = None,
    fetch=fetch_serving_commit,
    sleep=time.sleep,
    now=time.monotonic,
    log=print,
) -> tuple[int, str, str]:
    """Poll until prod serves acceptable code. Returns (exit_code, reason, served).

    `reason` is '' on success. `served` is the last commit observed, or '' if the
    endpoint never answered.
    """
    deadline = now() + wait_seconds
    served = ""
    last_why = ""
    attempt = 0
    while True:
        attempt += 1
        try:
            served = fetch(base_url)
            ok, why = classify(served, expected, paths)
            last_why = why
            if ok:
                log(f"[version] attempt {attempt}: serving {served[:7]} — {why}")
                return EXIT_OK, "", served
            log(f"[version] attempt {attempt}: serving {served[:7]} — {why}")
        except VersionUnreadable as exc:
            last_why = str(exc)
            log(f"[version] attempt {attempt}: {exc}")
            served = ""

        remaining = deadline - now()
        if remaining <= 0:
            break
        log(f"[version] waiting {poll_seconds:.0f}s ({remaining:.0f}s of budget left)")
        sleep(min(poll_seconds, remaining))

    kind = REASON_VERSION_UNREADABLE if not served else REASON_VERSION_MISMATCH
    # Name BOTH commits unconditionally. `last_why` does not always carry them —
    # the unparseable-paths fallback, for one, describes the rule rather than the
    # observation — and a mismatch alert whose text omits what prod was actually
    # serving cannot be acted on without re-deriving it from the run log.
    observed = f"prod serving {served[:7]}" if served else "prod's version endpoint never answered"
    detail = (
        f"waited {wait_seconds:.0f}s for prod to serve the commit under test "
        f"({expected[:7]}); {observed}. {last_why}. Nothing was measured, so "
        "nothing was enforced."
    )
    return EXIT_INCONCLUSIVE, f"{kind} — {detail}", served


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------


def _emit(reason: str) -> None:
    print(f"{REASON_PREFIX} {reason}")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Version-attribution guard for the recall gate.")
    parser.add_argument("--base-url", default=os.environ.get("MESH_API_URL", ""))
    parser.add_argument("--expect", default="", help="Require prod to serve this commit (or equivalent).")
    parser.add_argument("--was", default="", help="Require the serving commit to still be this one.")
    parser.add_argument("--record", action="store_true", help="Print the serving commit; require nothing.")
    parser.add_argument("--wait-seconds", type=float, default=DEFAULT_WAIT_SECONDS)
    parser.add_argument("--poll-seconds", type=float, default=DEFAULT_POLL_SECONDS)
    parser.add_argument(
        "--output",
        default=os.environ.get("GITHUB_OUTPUT", ""),
        help="Append `served=<sha>` here (GitHub step output).",
    )
    args = parser.parse_args(argv)

    if not args.base_url:
        _emit("infra-unreachable — MESH_API_URL is not set, so the serving version is unknowable")
        return EXIT_INCONCLUSIVE

    def write_output(sha: str) -> None:
        if args.output and sha:
            with open(args.output, "a", encoding="utf-8") as fh:
                fh.write(f"served={sha}\n")

    # --was: the stability half. Cheap, one request, run AFTER the measurement.
    if args.was:
        try:
            served = fetch_serving_commit(args.base_url)
        except VersionUnreadable as exc:
            # Deliberately NOT inconclusive. `--was` is a bonus check on top of a
            # measurement that already happened attributably; an endpoint that
            # blips at the end is not evidence the binary changed. Say "unknown"
            # rather than invent either answer.
            print(f"[version] post-run check could not read the endpoint: {exc}")
            print("[version] stability: unknown")
            return EXIT_OK
        write_output(served)
        if served == args.was or served.startswith(args.was) or args.was.startswith(served):
            print(f"[version] stability: unchanged at {served[:7]} across the measurement")
            return EXIT_OK
        _emit(
            f"{REASON_VERSION_CHANGED} — prod went {args.was[:7]} -> {served[:7]} during the "
            "measurement. The score spans two binaries and is a measurement of neither."
        )
        return EXIT_INCONCLUSIVE

    if args.record:
        try:
            served = fetch_serving_commit(args.base_url)
        except VersionUnreadable as exc:
            # Record mode runs where no pin is possible (a PR's `github.sha` is a
            # merge commit that is never deployed). Blinding the required check
            # because a version endpoint blipped would trade a real gate for a
            # guard that was never applicable here. Report unknown, loudly.
            print(f"[version] {exc}")
            print("[version] serving commit: unknown — the post-run stability check will be skipped")
            return EXIT_OK
        write_output(served)
        print(f"[version] serving commit: {served} (recorded; nothing required of it)")
        return EXIT_OK

    if not args.expect:
        parser.error("one of --expect, --was or --record is required")

    rc, reason, served = wait_for_version(
        args.base_url,
        args.expect,
        wait_seconds=args.wait_seconds,
        poll_seconds=args.poll_seconds,
        paths=deploy_paths(),
    )
    write_output(served)
    if reason:
        _emit(reason)
    return rc


if __name__ == "__main__":
    sys.exit(main())
