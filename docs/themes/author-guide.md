# Theme author guide

Code blocks in this guide are checked in CI: every `ts`/`tsx` block is compiled against the SDK unless its
info string says `fragment` (a piece that is not a whole module). Shell, JSON and CSS blocks are not compiled.

## 1. What a theme is

A theme is a repository with a manifest, optional tokens, an optional stylesheet and optional slot views.
The `mesh-theme build` command turns it into three files that an instance installs: `mesh-extension.json`,
`theme.css` and `views.js`.

Set `"kind": "theme"` in the manifest (the template does). The catalog then shows a Theme badge and lists the theme under the Themes filter instead of Integrations.

Start from [`packages/mesh-theme-template`](../../packages/mesh-theme-template).

```bash
cp -R packages/mesh-theme-template my-theme && cd my-theme
pnpm install
pnpm build
pnpm test
```

## 2. Tokens

Tokens are CSS variables the whole interface reads. They live in the manifest, per mode:

```json
{
  "theme": {
    "tokens_contract": "1",
    "modes": ["light", "dark"],
    "tokens": {
      "light": { "primary": "217 55% 32%", "radius": "0.5rem" },
      "dark": { "primary": "217 70% 62%", "radius": "0.5rem" }
    }
  }
}
```

- Contract version `1` has 57 colour tokens and four others: `radius`, `space-unit`, `font-sans`, `font-mono`.
  Colours are written as `H S% L%` (no `hsl()` wrapper), the way the CSS reads them: `hsl(var(--primary))`.
- The groups: surfaces and text (`background`, `foreground`, `card`, `popover`, `muted`…), actions (`primary`,
  `secondary`, `accent`, `destructive`, `success`, `warning`, `info`), borders and focus (`border`, `input`,
  `ring`), the sidebar (`sidebar-*`), priorities (`priority-*`), statuses (`status-*`), charts (`chart-1…5`)
  and avatars (`avatar-1…8`).
- An unknown token or a value of the wrong shape is refused at install, with the key named.
- A theme that declares one mode only is used for both; there is no automatic inversion.
- Anything you leave out keeps the built-in value.

## 3. The CSS layer

`src/theme.css` is served inside `@layer mesh-theme`, declared after the core's own layers. Your rules win over
the core's utility classes without `!important` and without raising specificity. A stylesheet written
without a layer is wrapped for you, so it cannot jump the order.

Use your own class names in your own views and the tokens for every colour:

```css
.my-card { border-radius: var(--radius); background: hsl(var(--card)); }
```

A stylesheet cannot load anything: `@import` is refused at install, and `url()` accepts only `data:` URIs and fragments.

To ship a font, put the file next to the stylesheet and reference it relatively: `mesh-theme build` inlines `.woff2`, `.woff`, `.png` and `.svg` files as `data:` URIs. Mind the 512 KB limit on `theme.css`, and ship only fonts whose licence allows redistribution under your theme's licence.

## 4. Slots and contracts

A slot is a place where the interface lets a theme supply the view. A view receives `data` (what to draw),
`actions` (what the user can trigger) and `settings` (the theme's own settings).

| Slot | What it draws |
|---|---|
| `board.layout`, `board.column`, `board.card` | the board, a column, a card |
| `task.row` | a task in a list |
| `task.view` | the task page |
| `dashboard.layout`, `dashboard.widget.<id>` | the dashboard and its widgets |
| `nav.sidebar`, `nav.topbar` | navigation |
| `doc.tree`, `doc.view` | document tree and document page |
| `auth.screen` | sign-in |

Every slot has `<Slot>Data`, `<Slot>Actions`, `<Slot>Settings` types and a `<Slot>View` component type in
`@entire-vc/mesh-theme-sdk`. A view is a function of its props:

```tsx
import { Draggable, TaskBadges, TaskLink } from "@entire-vc/mesh-theme-sdk";
import type { BoardCardView } from "@entire-vc/mesh-theme-sdk";

export const MinimalCard: BoardCardView = ({ data }) => {
  const { task } = data;
  const body = (
    <div className="my-card">
      <TaskLink taskId={task.id}>{task.title}</TaskLink>
      <TaskBadges task={task} />
    </div>
  );
  return data.isOverlay ? body : <Draggable item={{ kind: "task", id: task.id }} label={task.title}>{body}</Draggable>;
};

export default { views: { "board.card": { contract: "^1", view: MinimalCard } } };
```

You can replace any subset of slots; every slot you do not declare keeps the built-in view. Declare the
contract major your view was written for (`"^1"`).

## 5. Primitives, and why a view cannot replace them

Moving, permissions, editing and live updates are behaviour, not looks. The core owns them and a view uses
them:

| Primitive | What the core does for you |
|---|---|
| `DndProvider`, `Draggable`, `DropZone` | pointer and keyboard drag, the handle, drop targets, the announcement for screen readers, the move request |
| `Can`, `useCan` | what this user may do (a viewer has no handle and no editor) |
| `InlineEdit` | quick edit of a field, saved through the core |
| `TaskLink` | opening a task in the right place |
| `TaskBadges` | the `task.badge` insertion point, where extensions draw their badges |
| `useRealtime` | the live event stream |

Replace `Draggable` with a `div` and a card looks the same and cannot be moved by anyone. Replace
`InlineEdit` with an `<input>` and the edit never reaches the server, or reaches it as a different user's
role. That is why these are imported from the SDK and why the contract tests fail a view that does not use
them (see section 6).

At runtime the SDK package is a declaration: Mesh maps `@entire-vc/mesh-theme-sdk`, `react`, `react-dom`,
`react/jsx-runtime` and `react-i18next` to its own single copy. Keep them external; a bundle that carries its
own React is refused.

Data hooks (`useTasks`, `useMyTasks`, `useTriageTasks`, `useMentions`, `useActiveSessions`,
`useWidgetData`, `useSlotContext`) give a view the same data the core uses, for views that need more than
their slot data.

## 6. Contract tests

Each slot has a suite of tests, `CT-<group>-NN`, that mounts your view with fixtures and checks behaviour:
dragging moves the card once, a viewer has no handle, a realtime update changes a card in place, errors and
loading are drawn, the keyboard works, labels pass an accessibility check.

```ts fragment
import { registerContractSuites, setHarnessHost } from "@entire-vc/mesh-theme-sdk/testing";

setHarnessHost(host); // Mesh's own host, with the real primitives; only the network is replaced
registerContractSuites({ "board.card": { view: MyCard, major: 1 } }, (report) => console.log(report));
```

You do not write this yourself: `mesh-theme build` runs the suites on your built bundle. They need the Mesh
`web/` workspace (it contains the host); the CLI finds it next to the SDK in the Mesh repository, or from
`MESH_WEB_DIR`.

A slot passes when every test of its suite is green for the declared major. A red slot does not appear in
the report, and the build fails.

The template's `pnpm test` runs the type check, unit tests for the theme's own logic (`vitest`) and the
contract run into a scratch directory.

## 7. Build and the report

```bash
mesh-theme build [--entry src/index.tsx] [--css src/theme.css] [--out dist]
```

The build writes to `dist/`:

- `views.js`: your views as one ES module;
- `theme.css`: your stylesheet;
- `mesh-extension.json`: your manifest plus the `theme.css` and `theme.views` hashes (sha256), the declared
  `theme.slots` and the `theme.contract_report` (`sdk_version`, `bundle_sha256`, `passed`);
- `contract_report.json`: only when there are views, with the failures.

If a slot fails, the command exits 1, `contract_report.json` lists the failed tests, and **no
`mesh-extension.json` is written**: there is nothing install-ready to publish by mistake. A bundle that
carries its own React, or a manifest that does not match the manifest schema, fails the same way.
An instance checks the report again at install: the bundle hash must match, and every slot you declare must be
in `passed`.

## 8. Development mode

```bash
export MESH_INSTANCE=https://mesh.example.com
pnpm dev
```

`mesh-theme dev` serves the bundle from memory and rebuilds on every save. It keeps serving the last good
bundle when an edit does not compile. It prints one line to run in the browser console of a **development
build** of the instance (`localStorage.setItem("mesh-theme-dev-bundle", …)`); reload, and the instance uses
your bundle instead of the installed theme's views and reloads them after each rebuild. A shipped instance
ignores the flag and only accepts `localhost` URLs for it.

## 9. Installing on an instance

An instance installs a repository at a tag or branch and reads `mesh-extension.json`, `theme.css` and
`views.js` **from the root of that ref**. Publish the contents of `dist/` as the root of a release ref
(the template's CI example does this on a tag).

An instance admin installs from the *Installed themes* block of the instance settings, or through the API:

```bash
curl -X POST "$MESH/api/v1/extensions/repo/preview" -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"source":"repo","repo_url":"https://example.com/you/my-theme","ref":"release-v1.0.0"}'
# the same body to /extensions/install installs it
```

Preview shows the manifest and installs nothing. An install is refused when the theme asks for a contract the
instance does not serve, a token is unknown, a hash does not match or a declared slot is missing from the
report.

An installed theme can be the instance default and can be picked per workspace.

## 10. Versioning the contract

- The SDK version is the version of the slot contracts it describes (`CONTRACT_VERSION`).
- A view declares the major it was written for (`"^1"`). The core serves the current major and the one
  before it, so a theme has one major version of notice before it must move.
- `theme.tokens_contract` is versioned separately; an instance that serves another version refuses the theme.
- Adding a field to a slot's data or a new token is a minor change and breaks nothing. Removing or changing
  one is a major change.
- Pin your own theme's version with the `version` field of the manifest (`major.minor.patch`).

## 11. The reference theme

[`web/src/themes/mesh-default`](../../web/src/themes/mesh-default) is the built-in theme and passes the same
contract tests. Read it for how the real views handle states you will meet:

- `board/card.tsx`: the card (drag handle, chosen fields, overdue, overlay, badges);
- `board/column.tsx`, `board/layout.tsx`: columns, drop zones, loading, error and "load more";
- `list/task-row.tsx`, `task/view.tsx`: a row and the task page;
- `doc/tree.tsx`, `doc/view.tsx`: the document tree and page.

The template's card is a trimmed version of `board/card.tsx`.

Block order of the reference card, top to bottom (default `data.fields` order is `runtime, labels, assignee, gate,
reviewer, priority, delegation, due_date, estimate, subtasks, artifacts`): the title with the subtask count beside it; the
runtime row ("Being worked on…", Waiting, Queued, Stopped); labels and custom fields; the bottom row (assignee, gate,
reviewer, priority, delegation, due date, estimate) with the VCS marks and the attachment count at its right end.
There is no description mark, no edit pencil and no `checkout` field; a stored selection that still names `checkout`
is skipped. A theme draws `data.fields` in the order it is given, so it follows this order without extra code.
