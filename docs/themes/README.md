# Mesh themes

A Mesh theme changes how an instance or a workspace looks, in three layers that you can use one at a time:

1. **Tokens**: colours, radius, spacing unit and fonts, as CSS variables.
2. **CSS**: a stylesheet served in the `mesh-theme` cascade layer.
3. **Slot views**: React components that replace a built-in view (a board card, a task row, the sidebar…),
   built against the contract of that slot and checked by the contract tests.

Start here:

- [Author guide](./author-guide.md): everything you need to write, test, build and install a theme.
- [`packages/mesh-theme-template`](../../packages/mesh-theme-template): a theme repository to copy. It replaces
  one slot (`board.card`) and passes `pnpm install && pnpm build && pnpm test`.
- [`packages/mesh-theme-sdk`](../../packages/mesh-theme-sdk): types, primitives, contract tests and the
  `mesh-theme` command line tool.
- [`web/src/themes/mesh-default`](../../web/src/themes/mesh-default): the built-in theme. It is the reference:
  when the guide leaves a question open, read the matching view there.
