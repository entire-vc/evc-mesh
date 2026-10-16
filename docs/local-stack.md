# Local stack

`scripts/local-stack.sh up` starts an isolated Postgres and Redis, builds and runs
`cmd/api`, applies migrations, seeds a workspace, project and document with three
comment threads, and starts `pnpm dev`. It prints a browser login URL and does not
require production credentials.

`scripts/local-stack.sh screenshot` captures authenticated browser views at 1440
and 393 pixels using `web/scripts/local-stack/screenshot.mjs`. The helper sizes the
viewport to the actual scrollable content, including pages with an inner scroll
container. `scripts/local-stack.sh selftest` checks this behavior without starting
the stack.

`scripts/local-stack.sh teardown` removes the local containers and processes and
checks that all four ports are free.
