# Showcase migrated gate

This repository's `main` on GitHub (`entire-vc/evc-mesh`) is a **read-only
mirror**. Development canon moved to GitLab (`git.entire.host`) on
2026-08-22 (`CLAUDE-workflow.md` §0z), and a systemd timer on VM 113 is meant
to push GitLab → GitHub every 10 minutes, so nothing here needs a direct push
or merge.

> ⚠️ **The mirror has been failing to deliver, and this document does not
> assume otherwise.** Measured 2026-08-23: `showcase/main..origin/main` = **26**
> commits behind — and a hand reconciliation earlier that same day had already
> closed the gap once before it reopened, so treat the number as a reading, not
> a constant. Check it yourself rather than trusting this line:
>
> ```bash
> git fetch origin && git fetch showcase
> git rev-list --count showcase/main..origin/main   # showcase behind canon
> git rev-list --count origin/main..showcase/main   # commits that leaked here
> ```
>
> The cause is this repository's own branch protection rejecting the mirror's
> push (`GH006 … 9 of 9 required status checks are expected`): a commit arriving
> from GitLab carries no check-runs, and GitHub compares against the checks that
> already exist rather than waiting for new ones. Restoring it is tracked
> separately and needs host access nobody in the fleet currently has.
>
> So today, merging on GitLab is still the **correct** thing to do — it is
> what keeps canon whole — but it does **not** reach GitHub on its own yet.
> Do not read a missing commit on the showcase as your merge having failed.

`Showcase migrated gate` is a CI check that fails **every** pull request and
every merge-group run against this repository's `main`, unconditionally. It
has no condition to evaluate and no exception path: the fix on a red run is
never "make the check pass", it is "merge the same change on GitLab
instead" (`glab-merge <iid> entire-vc/evc-mesh`).

## Why this needed a gate at all

Measured 2026-08-23 (task `#e04ebd26`): four pull requests merged into
showcase `main` in six hours via `gh-merge`/the web button, each one
stalling the GitLab → GitHub sync until fixed by hand — not a one-off
leftover of the migration, an ongoing habit. Agents kept calling the tool
that used to be correct. See [hold-gate.md](hold-gate.md) for the fuller
argument (made there about the sibling `hold` gate) for why a required
status check is the one gate every merge path consults — the web button,
`gh pr merge`, a direct REST `PUT`, and the merge queue — rather than only
the paths that go through our own wrapper scripts.

> **Today this check is NOT yet in the required-checks list, so it reports
> without blocking.** Authoring the workflow (this file's subject) and
> arming it in branch protection are two different pieces of work — see
> task `#e04ebd26`'s subtasks 3/4. Until branch protection names
> `Showcase migrated gate` as required, a red run here is visible on the
> pull request but does not stop the web merge button, `gh pr merge`, a
> direct REST `PUT`, or the merge queue. Writing "blocked" here before that
> lands would repeat exactly the mistake `hold-gate.md` documents: a stated
> guarantee nothing yet enforces.

## What each context does

`pull_request` — the run that actually reports on a pull request as it
stands; once the check is required, the run that stops a pull request from
entering the merge queue at all.

`merge_group` — the queue's own re-run. A required check that stays silent
on `merge_group` wedges the repository permanently: every queue entry waits
forever for a context no workflow ever produces. This gate is unconditional
either way, so the two runs always agree — `merge_group` exists so the
context is reported there at all, not because it can find a different
answer.

## If the check is red on your pull request

That is not a defect in your change. Merge the same commits on GitLab
instead: `glab-merge <iid> entire-vc/evc-mesh`. That is the whole of your
job — canon is then correct.

Once the mirror is delivering again it carries the result to GitHub within 10
minutes, and you verify it by **ancestry, not presence**
(`CLAUDE-workflow.md` §0z point 9) — "the commit is on GitHub" does not by
itself prove it descends from the mirrored history:

```bash
git fetch showcase && git merge-base --is-ancestor <sha> showcase/main
```

While the mirror is not delivering (see the warning at the top) that check
fails for recent commits, and the failure is the mirror's, not yours.

## Scope

One repository (`entire-vc/evc-mesh`), because that is where the pattern was
measured. `.github/scripts/showcase_migrated_gate.py` derives the GitLab
project path from `github.repository`, so the same two files can be copied
to another migrated showcase repo without editing a canon URL by hand — but
copying them is a deliberate act, not something this gate does on its own.
