# Agent short tags

Source: the approved agent short tag contract and reference attached to the change.

Run only against the disposable local stack. The test creates its own workspace,
project, document, two agents and task; the whole database is discarded by stack
teardown. No production credentials or browser traces are used.

```sh
scripts/local-stack.sh up
cd web
LOCAL_STACK_WEB_URL=http://localhost:3007 pnpm exec playwright test --config playwright.local.config.ts
cd ..
scripts/local-stack.sh teardown
```

At 1440 and 393 pixels, check the tagged and untagged org cards, muted suffix,
unchanged avatar initials, assignment/reviewer option text and stable UUID values.
The editor saves through the real PATCH endpoint, and a GET verifies that name
and slug are preserved. Choosing a tagged mention inserts only the slug.

The local test creates new fixtures for every run. Optional
`LOCAL_STACK_VISUAL_FIXTURES=/tmp/agent-label-fixtures.json` records their routes
and IDs for screenshots; it contains no credentials. Native select options use
plain text; the board displays the tag in the avatar title and accessible label.
