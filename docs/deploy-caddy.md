# Caddyfile Deploy & Drift Guard

## Why this exists

`/etc/caddy/Caddyfile` on `mesh-vm` decides how prod serves the SPA, API, MCP
and S3 — and until now it lived **only on the host**, edited by hand, with
history kept as adjacent `.bak.*` files. A May 2026 header fix silently
didn't survive the July Helsinki host migration, and nobody noticed for three
months because "no header" and "wrong header" both look like a 200 from
outside. See Mesh `e8c44540` for the incident writeup.

## Source of truth

`deploy/caddy/mesh-vm.Caddyfile` in this repo is now the tracked source of
truth. The live file on the host must always match it byte-for-byte.

## How a change ships

1. Edit `deploy/caddy/mesh-vm.Caddyfile` in a PR, same as any other file.
2. On merge to `main`, `.github/workflows/deploy-caddy.yml` triggers:
   - uploads the new file to a **throwaway path** on `mesh-vm` (never the
     live path),
   - runs `caddy validate` against that throwaway path,
   - **only if validation passes**: backs up the current live file
     (`Caddyfile.bak.<timestamp>-pre-ci-deploy-<sha>`), moves the candidate
     into place, validates again in-place, then `systemctl reload caddy`,
   - smoke-tests `https://mesh.entire.host/api/v1/healthz/version` for a 200,
   - re-runs the drift check to confirm the live file now matches the repo.
3. A validation failure at either step means the live config is **never
   touched** and `reload` never runs — fail-closed by construction, not by
   convention.

Manual trigger: `workflow_dispatch` on `deploy-caddy.yml` (Actions tab), e.g.
to re-ship after a host-side hotfix gets folded back into the repo.

## Drift guard

`.github/workflows/caddy-drift-check.yml` runs `scripts/caddy-drift-check.sh`
every 6h (and on `workflow_dispatch`): it reads the live file over SSH
(read-only, no writes) and diffs it against `deploy/caddy/mesh-vm.Caddyfile`.
Any difference — or an unreachable host — fails the job. This is the guard
that catches a direct host edit that bypassed the PR path entirely; without
it, a repo copy is just documentation that quietly goes stale.

Run it locally against real prod at any time (read-only):

```bash
./scripts/caddy-drift-check.sh deploy/caddy/mesh-vm.Caddyfile
```

## Emergency host-side edit

If you ever have to hand-edit the live file (incident response, no time for
a PR): as soon as it's stable, `ssh mesh-vm cat /etc/caddy/Caddyfile` back
into `deploy/caddy/mesh-vm.Caddyfile` and open a PR immediately. The drift
check will otherwise start failing on its next scheduled run — which is the
point, not a bug to silence.
