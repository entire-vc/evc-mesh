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
