# docs-layout

`layout_docs.py` moves root-level Mesh documents into the fleet's canon folders
(`specs decisions plans research reports guides e2e registry archive`) from a layout
table, and merges legacy folders (`adrs→decisions`, `rnd→research`, `runbooks→guides`,
`prd→specs`). Python 3 stdlib only.

```bash
export MESH_API_URL=...  MESH_AGENT_KEY=...  MESH_WORKSPACE_ID=...   # or MESH_BEARER_TOKEN
python3 -I layout_docs.py --expect-total 188 --report dry.md            # dry-run (default), all projects
python3 -I layout_docs.py --apply --project mesh-dev                     # one project, writes layout-journal-*.jsonl
python3 -I layout_docs.py --apply --project mesh-dev --prune-merged      # + soft-delete emptied legacy folders
python3 -I layout_docs.py --rollback layout-journal-mesh-dev-<ts>.jsonl  # put everything back
```

Rules: project `website-relaunch` (demo workspace) is excluded and never touched; `state`, `product-state`, `obsidian-share`, program roots (root documents with
children) and `scratch-delete` rows are never moved. Rows labelled `?` go to `archive`
(`--unknown-policy skip` leaves them). A canon folder is created only when something is
put into it. Everything is validated before the first write; any inconsistency (unknown
label, id that resolves to 0 or 2 documents, slug mismatch, slug collision inside a target
folder) aborts with exit 2 and changes nothing. An HTTP 409 during apply stops the run.
The table cuts slugs at 45 characters, so a slug that is a prefix of the live one matches.

Journal (JSONL, one line per write): `op, project, doc_id, slug, old_parent_id,
new_parent_id, ts`. Rollback replays it in reverse. A pruned legacy folder is re-created
by rollback with the same slug and a new id. The dry-run figure "with --prune-merged" is a lower
bound: apply keeps a legacy folder whose page has text of its own.

Threat model of `--rollback`: the journal is a local file written by this script, and whoever
holds it also holds the agent key. Rollback still refuses a journal whose records do not match
the live state (project, slug, current parent, old parent in the same project, protected and
legacy slugs), and it checks everything before the first write. It does not defend against a
journal edited to a different but consistent state. Look at the diff of the journal before a
rollback, and keep it with the person who ran the apply.

`test_layout_docs.py` runs the whole cycle (dry-run writes nothing, red controls, apply,
repeat apply, rollback) against a local stand from `scripts/local-stack.sh up`. Never run it on prod.

## `--rename-slugs`

Renames documents whose slug was mangled by an old transliteration (`adrs001-use-nats`,
`runbooksargus-pulse`, `prdinvoices`, `rndspike`, a hyphen-only `---`). It changes the slug only;
the server (`PATCH /documents/{id}` with `slug`) records the old path as an alias, so old links keep resolving.

    python3 -I layout_docs.py --rename-slugs                       # dry run, all projects: table "was -> becomes" + count
    python3 -I layout_docs.py --rename-slugs --project lab         # dry run, one project
    python3 -I layout_docs.py --rename-slugs --apply --project lab # write; journal layout-rename-journal-<project>-<ts>.jsonl
    python3 -I layout_docs.py --rollback <journal>                 # restore the old slugs (a forged journal is refused)

Rules: `adrs<NNN>` becomes `adr-NNN` (parent `decisions`), `runbooks<name>` and the misspelt
`раннбук`/`ранбук` become `runbook-<name>` (parent `guides`), `prd<name>` becomes `prd-<name>` (parent `specs`),
`rnd<name>` becomes `rnd-<name>` (parent `research`). A mismatched parent is skipped and listed.

- `--apply` needs `--project`; the project `website-relaunch` is never touched.
- A collision with a sibling slug, or between two candidates, aborts the whole run before the first write (exit 2).
- A hyphen-only slug is renamed from the title only with `--allow-irreversible`; `--rollback` cannot restore it and says so.
- Old references resolve by the old FULL path (`decisions/adrs001-use-nats`), by UUID, and for the subtree below it.
  A bare old slug is not an alias.
- Tests: `test_rename_slugs.py` (offline); `test_rename_slugs_e2e.py` (local stand, `scripts/local-stack.sh up`).
- Prod apply is run by the project lead after the server release; rollback is `--rollback <journal>` or reverting the MR.
