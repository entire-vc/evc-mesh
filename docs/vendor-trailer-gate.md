# Tool-attribution trailers in commit messages

Commit messages in this repository must not carry a tool-attribution trailer —
`Co-Authored-By: <tool>` or `Generated with/by <tool>` for any of the names in
[`.github/vendor-trailer-terms.txt`](../.github/vendor-trailer-terms.txt).

A commit message is immutable content addressed by its SHA. Unlike a comment or
a pull-request body there is no edit that takes one back: the only remedy after
it lands is rewriting history and force-pushing over every clone and fork. So
the whole of the effort goes into refusing it beforehand.

## The two layers

**`.githooks/commit-msg`** — install it once per clone:

```
git config core.hooksPath .githooks
```

It fails in milliseconds, before the commit exists, which is the difference
between fixing a message and rebasing a branch. It is advisory by construction:
it lives on whichever machine installed it, and a fresh clone, another machine,
a web edit or `--no-verify` all walk past it.

**`Vendor trailer gate`** is the CI check. Verify the repository's current
branch-protection settings before relying on it to block merges: a reporting
check is not necessarily a required check, and every enabled merge path must
enforce the intended requirement.

Before requiring this check for a merge queue, verify that it reports in the
`merge_group` context. A required check that never reports can block the queue.
The live protection settings and observed check results are authoritative;
this document does not assert the current repository configuration.

## Why a branch trailer matters under squash merge

The branch messages do not disappear when a pull request is squashed. GitHub
composes the squash body by concatenating them, then appends its own
`Co-authored-by:` block derived from the trailers it found in them — so one
trailer on one branch commit arrives twice, without anyone typing it into the
merge box. Both routes derive from the branch commits, which is why refusing at
commit time closes both.

## What each context reads

`pull_request` reads the commits on the branch. This is the run that reports on
the branch as it stands, and — once the check is required — the run that stops a
pull request from entering the merge queue.

`merge_group` reads the commits the queue is about to merge — the squash commits
themselves. It has to run there regardless, because a required check that
reports nothing on `merge_group` wedges the repository forever. It is not
ceremonial either: it is the only context that sees a squash body retyped by
hand in the web merge box.

## If the check is red on your pull request

Rewrite the offending messages and force-push the branch. One commit: `git
commit --amend` and delete the line. Several: `git rebase -i` and edit each. The
check names every offending SHA and the exact line it found.

## Scope

Going forward, on this repository. Existing history is untouched — force-pushing
a public branch with a merge queue enabled breaks every clone and fork,
invalidates signed tags and release artefacts, and orphans every reference to a
commit SHA, including `/api/v1/version`, which reports the deployed one.

## Verifying it is alive

A check that has never said "no" is not known to be a check. `self_test` in
[`.github/scripts/vendor_trailer_gate.py`](../.github/scripts/vendor_trailer_gate.py)
runs on every invocation and proves the detector can both fire and stay silent,
so a term file emptied by a bad edit goes red rather than quietly passing.
Mutation coverage — empty term list, missing term file, crippled pattern,
unexpected event — each produces refusal rather than success.
