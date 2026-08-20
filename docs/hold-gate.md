# The `hold` label, and why it is a required status check

`hold` on a pull request means "do not merge this yet". This document explains
why enforcing that takes a CI check rather than a convention, since the obvious
cheaper answers were all tried first and all failed the same way.

## The four merge paths

A pull request can reach `main` four different ways. Until 2026-08-20 the label
was read by two of them, and both were ours:

| Path | Reads `hold`? |
|---|---|
| `~/bin/gh-merge` | yes — refuses, fail-closed |
| `~/bin/mesh-merge-train` (auto-merge bot) | yes — refuses, fail-closed |
| GitHub merge queue | no |
| Web "Merge pull request" button, `gh pr merge`, direct REST `PUT .../merge` | no |

Every path GitHub itself offers ignored the label entirely. The two that
honoured it were the two we wrote, which is the trap: the gate looked and felt
real to everyone who used the wrappers, and did nothing to anyone who did not.

It was measured twice on this repository, three days apart:

- PR **#613** was labelled `hold` at 18:19 and entered the merge queue at 18:44.
- PR **#619** merged at 22:18 with the label still attached, and deployed to
  production. Nobody removed it — it is still on the pull request today.

Both times the wrapper gate worked perfectly and changed nothing, because
nothing routed through it.

## Why a required status check

Branch protection is the only thing all four paths consult. A required check is
therefore the one gate that cannot be walked around by choosing a different
button — the label becomes real by being read *here* rather than by being read
more carefully by us.

The alternatives were considered and rejected:

- **Draft instead of `hold`.** Works — the queue does honour draft — but it
  discards a distinction worth keeping: draft means "not finished", `hold` means
  "finished, deliberately not shipping yet". It also silently invalidates every
  existing instruction that tells people to use the label.
- **Required review from a non-author.** Solves a different problem, and makes
  every routine change wait on a second party.

## How it works

- [`.github/workflows/hold-gate.yml`](../.github/workflows/hold-gate.yml) — runs
  on `pull_request` (including `labeled`/`unlabeled`, so applying the label to an
  already-green PR turns it red) and on `merge_group`.
- [`.github/scripts/hold_gate.py`](../.github/scripts/hold_gate.py) — the verdict.
- [`.github/hold-labels.txt`](../.github/hold-labels.txt) — the label list, as
  data rather than as a literal in code.

Two properties are load-bearing and easy to break by accident:

**It must report on `merge_group`.** A required check that stays silent in the
queue wedges the repository permanently: every entry waits for a context that is
never requested, and with `enforce_admins: true` there is nobody left who can
override it. This is why the workflow landed and was watched running in both
contexts *before* it was added to the required list.

**It fails closed on anything it does not understand** — unreadable label list,
empty label list, unknown event, an unparseable merge group. "I could not see a
reason to stop" is not the same as "there is no reason to stop", and treating
them as the same is the entire defect being fixed here.

That property paid for itself before the gate ever shipped. Its first run failed
on its own pull request, which carried no label at all: the sparse checkout
fetched the label list and not the script that reads it. A fail-open gate would
have gone live dead, and reported nothing but green.

**The job's display name is the contract.** Branch protection matches the string
`Hold gate`. Renaming the job un-arms the gate while making it look pending
rather than misconfigured.

## Keeping the list in one piece

`.github/hold-labels.txt` and `HOLD_LABELS` in `bob/scripts/fleet_gate_labels.py`
must hold the same names. They are two copies of one rule, in two repositories,
and they drifted the day the first one was written — three names were honoured by
the wrappers and unknown to this gate. `mesh-merge-train --test` now compares
them and fails on any difference.

## Removing a hold

Take the label off. The check re-runs on `unlabeled` and goes green. If the PR
is in the queue it will need re-queueing, since a red required check ejects it.
