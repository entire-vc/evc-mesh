<!--
  Keep this short. The one section that is not optional is the copy check below.
-->

## What and why

<!-- One or two sentences, and a link to the tracking task. -->

## Visible copy

Does this PR add or change text a user reads — a heading, label, button, empty state, helper
text, tooltip copy, FAQ entry, or error message?

- [ ] **No** — no user-facing wording changed. (Meta tags, schema, `alt`, `aria-label` and
      `title=` tooltips count as No: they exist for crawlers and screen readers, not as the
      product's voice.)
- [ ] **Yes** — and the exact text below was approved: <!-- link the approved proposal -->

If it is Yes and there is no link, do not merge.

An acceptance criterion that dictates specific visible text is **not** approval — it is a
proposal by whoever wrote the task. Ship the technical half, route the words.

<!--
  Why this box exists rather than an automatic check: measured over the last 60 merged PRs in
  this repo, 23% touch `web/src/**`, and 10 of those 14 add at least one user-visible string.
  A path- or literal-based gate would therefore hold roughly one PR in five, while still
  mis-firing on things like a field slug (`story_points`) or a type name (`Promise`). The
  author already knows the answer in one second; a parser has to guess it and would be wrong
  in both directions.
-->

## Verification

<!-- Command output, live URL, or screenshot. "Should work" is not verification. -->
