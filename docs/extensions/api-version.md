# Extensions API version

Mesh has no semver: the only tag is `v0.1.0-alpha` and `BuildVersion` comes from
`git describe`. Extension manifests therefore reference `mesh_api`, an integer
version of the **extension API**, not a Mesh build version.

- Source of truth: `extensions.ExtensionsAPIVersion` in `internal/extensions/version.go` (starts at 1).
- Exposed as `extensions_api` in `GET /api/v1/version` (aliases `/api/version`, `/api/v1/healthz/version`).

## When it is raised

Only on a breaking change to the extension contract:

- manifest schema (a field removed, renamed, or its meaning changed);
- slots (a slot removed or its props/contract changed);
- permissions (a permission removed, renamed, or narrowed);
- events (an event removed or its payload changed incompatibly).

Additive changes (new optional manifest field, new slot, new permission, new
event) that do not change existing behaviour do not raise it.

## Who decides

Maintainers document each breaking extension API change and its migration path
for existing extensions. Update the version constant together with that decision
and the matching handler test so the change is deliberate and reviewable.

## Agent API contract

The `/api/v1/agents/me/*` surface used by external runners is pinned by contract
tests (`go test ./internal/handler -run Contract`); what counts as breaking is in
`agents-me-contract.md`.

## External extension contract

The calls Mesh makes to an external service, the extension actor routes and
the public event bodies are pinned by contract tests against the reference
extension in `examples/external-extension`
(`go test ./internal/... -run 'Contract|RefExt'`); what counts as breaking is
in `extension-actor-contract.md`.
