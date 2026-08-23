#!/usr/bin/env python3
"""Fail every pull request and merge-group run against this repository's main.

Run as a required status check. See
.github/workflows/showcase-migrated-gate.yml for why the gate needs to be a
required check rather than a convention, and docs/hold-gate.md for the same
argument made in full about the sibling gate this one is modelled on.

This repository's `main` on GitHub is a read-only mirror of GitLab canon
(git.entire.host) as of 2026-08-22 (CLAUDE-workflow.md §0z). A systemd timer
on VM 113 is meant to push GitLab -> GitHub every 10 minutes; nothing here
needs a direct merge, and a direct merge here just sits ahead of canon until
someone notices and reconciles it by hand. Measured 2026-08-23 (task
#e04ebd26): four PRs merged into showcase main in six hours, each one
stalling the mirror until fixed manually.

The mirror has been failing to deliver -- measured 2026-08-23,
`showcase/main..origin/main` = 26 commits, and a hand reconciliation earlier
that same day had already closed the gap once before it reopened. The cause is
this repository's own branch protection refusing the mirror's push, because a
commit arriving from GitLab carries no check-runs. No start date is asserted
here on purpose: the gap opens and closes, so a duration baked into a
permanent message goes stale silently -- read the live count instead. What
matters to the reader is only that delivery is not guaranteed, because a
developer who merges on GitLab, sees nothing appear here, and concludes the
merge failed is a developer about to merge here instead -- which is the leak
this gate exists to stop.

Unlike hold_gate.py this gate has no condition to evaluate -- every
pull_request and merge_group run against this repo's main is refused,
unconditionally, by design. The two events are still distinguished only so
an unexpected third event fails loudly instead of being treated as "some
other case that's presumably fine".
"""

from __future__ import annotations

import os
import sys


def fail(message: str) -> None:
    print(f"::error::{message}")
    summary_path = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary_path:
        try:
            with open(summary_path, "a", encoding="utf-8") as handle:
                handle.write(f"### Showcase migrated gate\n\n{message}\n")
        except OSError:
            pass
    sys.exit(1)


def main() -> None:
    repo = os.environ.get("REPO", "")
    event = os.environ.get("EVENT_NAME", "")

    if not repo:
        fail("REPO is not set; refusing rather than guessing which GitLab project this maps to")
    if event not in ("pull_request", "merge_group"):
        fail(f"unexpected event {event!r}; this gate only knows pull_request and merge_group")

    canon_url = f"https://git.entire.host/{repo}"
    fail(
        f"{repo} on GitHub is a read-only showcase mirror -- GitLab ({canon_url}) is canon. "
        f"Merge this change there instead (`glab-merge <iid> {repo}`). That is the whole of "
        "the job: canon is then correct. NOTE: the GitLab -> GitHub mirror has been "
        "failing to deliver, so your change may not appear here on its own -- check with "
        "`git rev-list --count showcase/main..origin/main` rather than assuming either way. "
        "A missing commit on GitHub does NOT mean your merge failed, and is not a reason to "
        "merge here. This check always fails by design "
        "on this repository's main and is not reporting a defect in your change."
    )


if __name__ == "__main__":
    main()
