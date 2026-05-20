# Backend Deploy Guide

## How it works

Backend deploys are **fully automated** through `.github/workflows/deploy-backend.yml`.

A deploy triggers whenever a push to `main` touches backend paths (`cmd/**`, `internal/**`, `go.mod`, `go.sum`). The workflow:

1. Runs `golangci-lint` + full test suite (with PostgreSQL, Redis, NATS) — both must pass.
2. Cross-compiles the API binary for `linux/amd64` with embedded build metadata.
3. Uploads the binary to `tw-mesh` and performs an atomic swap + `systemctl restart`.
4. Verifies the deployed commit SHA via `/api/v1/healthz/version`.

**No manual SSH builds.** The prod binary only changes through this workflow.

---

## Verifying a deploy

```bash
curl https://mesh.entire.host/api/v1/healthz/version
# → {"commit":"<sha>","build_time":"2026-05-20T17:00:00Z","version":"v1.2.3","environment":"prod","service":"evc-mesh-api"}
```

The `commit` field must match the merge commit SHA on `main`.

The legacy `/api/version` endpoint returns identical data and remains for backward compatibility.

---

## Burst-deploy guard

The workflow will **fail** if the previous deploy completed less than 5 minutes ago. This prevents accidental rapid-fire deploys (root cause of the 2026-05-20 prod incident).

To override, add `[force-deploy]` to your commit message, or trigger via `workflow_dispatch` with `force: true`:

```bash
git commit -m "fix: hotfix for critical bug [force-deploy]"
```

> Use sparingly. A forced deploy still runs full CI — it only bypasses the 5-minute cooldown.

---

## Build metadata (ldflags)

Four vars are injected at compile time via `-ldflags`:

| Go var | ldflag path | Deploy value |
|--------|-------------|--------------|
| `BuildSHA` | `main.BuildSHA` | `$GITHUB_SHA` (full 40-char SHA) |
| `BuildTime` | `main.BuildTime` | UTC ISO-8601 timestamp |
| `BuildVersion` | `main.BuildVersion` | `git describe --tags --always` |
| `BuildEnv` | `main.BuildEnv` | `prod` |

For local cross-compilation use `make build-prod`.

---

## Emergency rollback

If a bad deploy slips through (smoke test passes but runtime breaks):

```bash
ssh root@216.57.106.222
cp /opt/evc-mesh/mesh-api.prev /opt/evc-mesh/mesh-api   # if backup exists
systemctl restart mesh-api
```

The deploy script does **not** automatically keep a `.prev` backup — add one to the workflow if rollback speed becomes critical. For now, re-deploy the last known-good commit via GitHub Actions.

---

## Adding a backup step (future)

Add this before the atomic swap in `deploy-backend.yml`:

```bash
ssh root@... 'cp /opt/evc-mesh/mesh-api /opt/evc-mesh/mesh-api.prev || true'
```

---

## Manual SSH builds — PROHIBITED

Do **not** run `go build` locally and `scp` the binary to prod. Reasons:

- Bypasses CI gate (broken tests can reach prod).
- No ldflags → `/api/v1/healthz/version` returns `commit: "dev"` → impossible to confirm what's running.
- Leaves no audit trail in GitHub Actions history.

If you need an emergency fix outside CI, escalate to Pavel.
