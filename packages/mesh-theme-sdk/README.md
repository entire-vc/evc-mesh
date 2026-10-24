# @entire-vc/mesh-theme-sdk

Types, data hooks and primitives for building Mesh themes. The package version is the version of
the slot contracts it describes (`CONTRACT_VERSION`).

Author guide: `docs/themes/author-guide.md`. A theme repository to copy: `packages/mesh-theme-template`.

## What is in it

- **Slot contracts (types):** `board.layout`, `board.column`, `board.card`, `task.row`, `task.view`,
  `dashboard.layout`, `dashboard.widget.<id>`, `nav.sidebar`, `nav.topbar`, `doc.tree`, `doc.view`,
  `auth.screen`. Each slot has `*Data`, `*Actions`, `*Settings` and a `*View` type.
- **Primitives:** `Draggable`, `DropZone`, `DndProvider`, `Can` / `useCan`, `InlineEdit`, `TaskLink`, `TaskBadges` (the `task.badge` insertion point a card must carry),
  `useRealtime`.
- **Data hooks:** `useTasks`, `useWidgetData`, `useMentions`, `useMyTasks`, `useTriageTasks`,
  `useActiveSessions`, `useSlotContext`.

## Runtime

The primitives and hooks are declarations only. Keep `@entire-vc/mesh-theme-sdk` external when you
bundle a theme: Mesh maps it, together with `react`, `react-dom`, `react/jsx-runtime` and
`react-i18next`, to its own single copy through an import map. A bundle that ships its own React
is rejected.

## Contract tests

`@entire-vc/mesh-theme-sdk/testing` ships the contract suites (CT-common-*, CT-<group>-*) every slot view has to pass. It is TypeScript source for vitest with jsdom; `vitest`, `@testing-library/react`, `@testing-library/user-event` and `axe-core` are optional peer dependencies.

```ts
import { registerContractSuites, setHarnessHost } from "@entire-vc/mesh-theme-sdk/testing";

setHarnessHost(host); // the host supplies the real primitives, router, permissions and realtime
registerContractSuites({ "board.card": MyCard }, (report) => {
  // report.passed: { "board.card": "1.0.0" }, report.failed: { slot: ["CT-board-04", ...] }
});
```

`mountSlot(view, scenario)` mounts one view with fixtures, a role (member/viewer), action spies, realtime events, keyboard, locale and mode. Only the transport is replaced; an unexpected `fetch` fails the test. A slot passes when every test of its suite is green for the declared major.

## CLI: `mesh-theme`

Run it in a theme project (a `mesh-extension.json` with `kind: theme` and a `theme` block, views in
`src/index.tsx` exporting `default { views: { "<slot>": { contract: "^1", view } } }`, optional
`src/theme.css`).

```bash
mesh-theme build [--entry src/index.tsx] [--css src/theme.css] [--out dist]
mesh-theme dev --instance https://mesh.example.com [--port 4173]
```

`build` bundles an ES module with `react`, `react-dom`, `react/jsx-runtime`, `react-i18next` and the SDK
external (the bundle never carries its own React), hashes the bundle and the stylesheet (sha256), runs the
slot contract tests and writes `dist/mesh-extension.json`: your manifest with `theme.views`,
`theme.css`, `theme.slots` and `theme.contract_report { sdk_version, bundle_sha256, passed }`, validated
against the extension manifest schema. If a slot fails its contract the slot is left out of `passed`,
`dist/contract_report.json` lists the failures, the command exits 1 and **no manifest is written**, so
nothing install-ready exists to publish by mistake. Without the contract tests there is no report either
(exit 2).

The contract tests run through `cli/vitest-runner.mjs`, which needs the Mesh `web/` checkout (found next to
the package, or set `MESH_WEB_DIR`). `--contract-runner <module>` swaps in another runner. `node
scripts/check-real-runner.mjs` runs the red (no `Draggable`) and green example themes through the real suites.

`dev` serves the bundle from memory, rebuilds on every save and keeps serving the last good bundle when an
edit does not compile. In the browser console of a **dev build** of the instance run the line it prints
(`localStorage.setItem("mesh-theme-dev-bundle", …)`) and reload: the instance uses your local bundle in
place of the installed theme's views and reloads the views on each rebuild. A shipped instance ignores the
flag.

## Develop

```bash
pnpm install
pnpm build   # declarations into dist/
pnpm test    # a theme using only the SDK compiles; one importing a missing slot type or primitive does not
```

Mesh itself reads `src/` directly (see `web/tsconfig.app.json`), so the types the core uses and the
types a theme sees are the same declarations. `web/src/core/themes/shared.ts` is typed against this
package's exports: a declaration the core does not implement fails its typecheck.

## License

Apache-2.0.
