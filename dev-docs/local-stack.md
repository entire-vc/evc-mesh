# Local stand — one command, no prod credentials

`scripts/local-stack.sh up` brings up a throwaway Postgres + Redis, builds and runs `cmd/api`
against them (migrations apply automatically on boot), seeds a workspace/project/document with
three comment threads (a nested reply, a resolved thread, and a whole-page thread with no anchor),
starts `pnpm dev`, and prints a URL you can log into with a real browser session — all without
touching prod or needing a Casdoor/agent credential, which don't exist for the native Mesh login
on this box. `scripts/local-stack.sh screenshot` then drives a real authed login and takes 1440 and
393 screenshots via `web/scripts/local-stack/screenshot.mjs`, which grows the browser viewport to
fit the page's actual scrollable content instead of relying on Playwright's `fullPage` (a no-op
whenever the content scrolls inside an inner container rather than `<body>` — the trap that let a
misleading, silently-cropped screenshot pass every green assertion during D7, `#fd2bfec6`); run
`scripts/local-stack.sh selftest` any time to see that detector accept a normal page and refuse a
page whose scroller doesn't track the viewport, with no stack required. `scripts/local-stack.sh
teardown` removes the containers and processes and confirms all four ports are free.
