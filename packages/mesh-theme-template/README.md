# Mesh theme template

A starting point for a Mesh theme: a few tokens, a stylesheet and a `board.card` view that follows the
contract (it uses `Draggable`, `InlineEdit`, `TaskLink` and `TaskBadges` from the SDK).

```bash
pnpm install
pnpm build   # dist/: views.js, theme.css, mesh-extension.json (with the contract report)
pnpm test    # type check, unit tests, and the board.card contract tests
pnpm dev     # MESH_INSTANCE=https://your.instance pnpm dev
```

- `mesh-extension.json`: the manifest. Change `id`, `name`, `publisher` and the tokens first.
- `src/index.tsx`: the slots you replace.
- `src/card.tsx`: the `board.card` view. `src/fields.ts` holds its pure logic (see `test/`).
- `src/theme.css`: served in the `mesh-theme` cascade layer.
- `.gitlab/ci-example.yml`: test on every push, publish `dist/` on a tag.

The contract tests run inside `mesh-theme build` and need the Mesh `web/` workspace; set `MESH_WEB_DIR` when
the SDK is not next to it. Replace `Draggable` with a `div` in `src/card.tsx` and `pnpm build` fails with the
`CT-board-*` tests that broke and writes no manifest.

The full guide is in `docs/themes/author-guide.md` of the Mesh repository.
