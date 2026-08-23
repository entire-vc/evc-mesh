# evc-mesh Deploy Runbook

Hands-on guide for deploying the evc-mesh API server on prod (systemd, bare-metal / VPS).

---

## Prerequisites

- SSH access to prod host
- `gh` CLI authenticated (or direct `git` access to the repo)
- `systemctl` access (root or sudo)
- Docker access (PostgreSQL runs in docker-compose — bare `psql`/`pg_dump` are not on the host)

---

## Getting the change into `main` first

Everything below deploys what is already on `main`. Landing it there no longer
goes through a direct merge: `main` has a **merge queue**, so a pull request is
*enqueued* and the queue merges it after building and testing your change
combined with whatever is queued ahead of it. A raw
`gh api -X PUT repos/.../pulls/<n>/merge` no longer merges anything.

Both fleet merge paths detect the queue per call and switch by themselves, so
the command you run is unchanged:

```bash
~/bin/gh-merge <pr> entire-vc/evc-mesh        # enqueues when the branch has a queue
```

Two consequences for anyone timing a deploy:

- **Enqueued is not merged.** The entry can still be rejected if the combined
  tree fails, so wait for `state=MERGED` before starting a deploy — an
  `ENQUEUED` report is not a landing.
- **A merge group is not instant.** Every group runs the full memory bench,
  because that gate's scope check treats any non-`pull_request` event as
  in-scope by design, so budget tens of minutes rather than seconds.

Details and how to inspect the queue: `docs/contributing.md`.

### Re-landing a batch: turn batching on, then turn it back off

By default the queue merges **each entry as soon as its own group is green**
(`min_entries_to_merge: 1`). That is deliberate for normal traffic — with one or
two open PRs there is nothing to batch, and waiting for company would only add
latency to every merge.

Before a **re-land batch** (several PRs that must all go back in), raising the
minimum makes the queue collect entries into one group: one build, one test run,
one merge, instead of one of each per PR. Turn it off again afterwards.

Read-modify-write, so the other six parameters are carried over rather than
reset — sending a bare `{"min_entries_to_merge": 2}` would drop
`check_response_timeout_minutes: 60`, and a lower timeout ejects every group
whose memory bench runs the normal 30-45 minutes.

```bash
# ENABLE batching (before the re-land):
gh api repos/entire-vc/evc-mesh/rulesets/20589286 \
  | jq '{rules: [.rules[] | if .type=="merge_queue"
        then .parameters.min_entries_to_merge = 2 else . end]}' \
  | gh api --method PUT repos/entire-vc/evc-mesh/rulesets/20589286 --input -

# VERIFY (read back, do not trust the write's echo):
gh api repos/entire-vc/evc-mesh/rulesets/20589286 \
  --jq '.rules[]|select(.type=="merge_queue")|.parameters.min_entries_to_merge'
# -> 2

# ROLLBACK (immediately after the batch has landed):
gh api repos/entire-vc/evc-mesh/rulesets/20589286 \
  | jq '{rules: [.rules[] | if .type=="merge_queue"
        then .parameters.min_entries_to_merge = 1 else . end]}' \
  | gh api --method PUT repos/entire-vc/evc-mesh/rulesets/20589286 --input -
```

**The price, while it is on:** every *single* PR waits for company for up to
`min_entries_to_merge_wait_minutes` (currently 5) before it merges alone. That
is a tax on the common case to buy a saving on the rare one, which is why it is
a temporary switch and not the default. Turn it back to `1` as soon as the batch
is in.

**What it does and does not buy:** it saves CI *runs*, not wall-clock — two
entries under `min: 1` build two groups **in parallel**, so the elapsed time is
about the same either way. The saving is one fewer full memory bench per extra
PR in the batch.

> Not yet exercised: the read-modify-write payload above was verified to change
> `min_entries_to_merge` and nothing else, but the `PUT` itself has never been
> run — the queue has only ever operated at `min: 1`. Whoever runs this first
> should treat the verify step as load-bearing and check the read-back before
> queuing the batch.

---

## Normal deploy (no out-of-order migrations)

**Mandatory order:**
`migrate (goose up)` → `binary swap` → `restart`. Never swap the binary before migrations pass.

CI enforces this automatically via the `migrate` job in `deploy-backend.yml`. For manual hotfix deploys that bypass CI, follow the steps below exactly — do not skip the migration step.

```bash
# 1. Build binary locally or pull from CI artifact
go build -o bin/mesh-api ./cmd/api

# 2. Copy binary AND migration files to prod
scp bin/mesh-api root@prod-host:/opt/evc-mesh/bin/mesh-api.new
rsync -avz --checksum migrations/ root@prod-host:/opt/evc-mesh/migrations/

# 3. On the prod host: backup DB, run migrations FIRST, then swap binary
ssh root@prod-host

  cd /opt/evc-mesh

  # Backup DB (Postgres runs in docker-compose — use docker exec)
  docker exec <postgres-container> pg_dump -U mesh -d mesh -Fc \
    > /opt/evc-mesh/db-backups/pre-deploy-$(date +%Y%m%dT%H%M%S).dump

  # STEP 1 — Run migrations BEFORE touching the binary (fail-closed).
  # goose CLI is installed at /opt/evc-mesh/bin/goose by the CI migrate job.
  # If this exits non-zero, STOP — do NOT proceed to the binary swap.
  # Build the DSN from the component vars .env.prod actually contains
  # (there is no DATABASE_URL — see "Environment variables" below).
  set -a; source /opt/evc-mesh/.env.prod; set +a
  DB_URL="postgres://${DB_USER}:${DB_PASSWORD}@${DB_HOST}:${DB_PORT:-5432}/${DB_NAME}?sslmode=${DB_SSL_MODE:-disable}"
  /opt/evc-mesh/bin/goose -dir /opt/evc-mesh/migrations postgres "$DB_URL" up

  # STEP 2 — Swap binary atomically (only after migrations succeed)
  mv bin/mesh-api bin/mesh-api.bak.$(date +%Y%m%d-%H%M%S)
  mv bin/mesh-api.new bin/mesh-api

  # STEP 3 — Restart
  # goose.Up(WithAllowMissing) in main.go is a belt-and-suspenders safety net
  # for out-of-order hotfix migrations; it is a no-op if all migrations already applied.
  sudo systemctl restart mesh-api

  # Verify
  sudo systemctl status mesh-api
  curl -s http://localhost:8005/health | jq .
```

---

## evc-mesh-mcp deploy

**The MCP server is not built from this repository.** It lives in
`entire-vc/evc-mesh-mcp` and ships from that repository's own workflow
(`.github/workflows/deploy-mesh-vm.yml`, dispatched by hand: `dry-run` prints
the plan and changes nothing, `deploy` swaps, `rollback` reverts). That
workflow takes a rollback anchor before the swap and reverts automatically if
its smoke test fails — prefer it over the manual steps below.

This repo used to carry a duplicate copy under `cmd/mcp`, and this runbook
built from it. It had drifted 12 tools behind and was deleted (Mesh
#e85e4e05); the steps below are kept only as the by-hand fallback for when the
workflow itself is unavailable.

**Mandatory order:**
`migrate (goose up)` → `binary swap` → `restart`. Never swap the binary before migrations pass.

> evc-mesh-mcp does not run its own migrations — it reads the same DB as the evc-mesh API.
> The goose step below ensures the shared schema is up-to-date before the new binary serves traffic.

```bash
# 1. Build for the prod target, from a checkout of entire-vc/evc-mesh-mcp
#    (NOT from this repository)
GOOS=linux GOARCH=amd64 go build -o mesh-mcp .

# 2. Copy binary to prod
scp mesh-mcp root@mesh-vm:/opt/evc-mesh/bin/mesh-mcp.new

# 3. On the prod host: run migrations FIRST, then swap binary
ssh root@mesh-vm

  # STEP 1 — Run evc-mesh DB migrations (fail-closed).
  # goose CLI is installed at /opt/evc-mesh/bin/goose by the CI migrate job.
  # If this exits non-zero, STOP — do NOT swap the binary.
  # Build the DSN from the component vars .env.prod actually contains
  # (there is no DATABASE_URL — see "Environment variables" below).
  set -a; source /opt/evc-mesh/.env.prod; set +a
  DB_URL="postgres://${DB_USER}:${DB_PASSWORD}@${DB_HOST}:${DB_PORT:-5432}/${DB_NAME}?sslmode=${DB_SSL_MODE:-disable}"
  /opt/evc-mesh/bin/goose -dir /opt/evc-mesh/migrations postgres "$DB_URL" up

  # STEP 2 — Swap binary (only after migrations succeed).
  # Verified live 2026-08-23: the unit is `mesh-mcp.service` and the binary is
  # /opt/evc-mesh/bin/mesh-mcp — there is no /opt/evc-mesh-mcp directory, which
  # is what the earlier version of these steps named.
  mv /opt/evc-mesh/bin/mesh-mcp /opt/evc-mesh/bin/mesh-mcp.rollback-$(date +%Y%m%d-%H%M%S)-manual
  mv /opt/evc-mesh/bin/mesh-mcp.new /opt/evc-mesh/bin/mesh-mcp

  # STEP 3 — Restart
  sudo systemctl restart mesh-mcp

  # STEP 4 — Smoke test. The MCP SSE server has no /health route; it serves
  # /metrics, /sse and /message. /metrics is the unauthenticated liveness probe.
  curl -sf http://localhost:8081/metrics >/dev/null || echo "SMOKE FAILED"
```

---

## Migration numbering policy

Migrations use a `YYYYMMDDNNN` prefix where `NNN` is a per-day sequence starting at `001`.

**Rule**: every new migration **must** have a version > the highest version already merged to `main`.

```
Good:  prod max = 20260518045  →  new file = 20260519001_add_index.sql
Bad:   prod max = 20260518045  →  new file = 20260315046_add_index.sql  ← BLOCKED by CI
```

The CI gate (`migration-check.yml`) enforces this at PR merge time.  
The API binary (`goose.WithAllowMissing`) is a second line of defence: if an out-of-order migration somehow reaches prod, the server will still start and apply it rather than crash-loop.

---

## Handling an out-of-order migration (emergency procedure)

This happened once (PR #51 / task 6296db42) and must not become standard practice.  
If CI was bypassed and an out-of-order migration lands on prod:

```bash
# On prod host, before deploying the new binary:

# 1. Check current goose version table (via docker exec — DB is containerized)
docker exec <postgres-container> psql -U mesh -d mesh \
  -c "SELECT version_id, is_applied FROM goose_db_version ORDER BY id;"

# 2. Copy the out-of-order migration into the container and apply it manually
docker cp /tmp/the_migration.sql <postgres-container>:/tmp/the_migration.sql
docker exec <postgres-container> psql -U mesh -d mesh -f /tmp/the_migration.sql

# 3. Stamp goose so it tracks the migration as applied.
#    The goose CLI is not installed on the host; the binary applies migrations
#    automatically on startup via goose.WithAllowMissing — deploy the new binary
#    and it will stamp the version itself.

# 4. Deploy the new binary normally — it will start clean
sudo systemctl restart mesh-api
```

**Escalate to Garfield / Riker if unsure.**

---

## Rollback

```bash
# Stop service
sudo systemctl stop mesh-api

# Restore previous binary (most recent .bak)
cd /opt/evc-mesh
mv bin/mesh-api bin/mesh-api.failed
mv bin/mesh-api.bak.$(ls bin/ | grep '\.bak\.' | sort | tail -1 | grep -oP '(?<=bak\.).*') bin/mesh-api
# or simply: cp bin/mesh-api.bak.<timestamp> bin/mesh-api

# Rollback migrations if the new version added migrations.
# goose CLI is installed at /opt/evc-mesh/bin/goose by CI. Rolls back one migration at a time:
set -a; source /opt/evc-mesh/.env.prod; set +a
/opt/evc-mesh/bin/goose -dir /opt/evc-mesh/migrations postgres \
  "postgres://${DB_USER}:${DB_PASSWORD}@${DB_HOST}:${DB_PORT:-5432}/${DB_NAME}?sslmode=${DB_SSL_MODE:-disable}" \
  down

# Restart with old binary
sudo systemctl start mesh-api
sudo systemctl status mesh-api
```

> **Note**: rolling back data-destructive migrations (DROP TABLE, DELETE) requires restoring from the pre-deploy pg_dump backup in `/opt/evc-mesh/db-backups/`.

---

## Systemd unit reference

Unit file location: `/etc/systemd/system/mesh-api.service`

```ini
[Unit]
Description=EVC Mesh API Server
After=network.target docker.service
Requires=docker.service

[Service]
Type=simple
User=root
WorkingDirectory=/opt/evc-mesh
EnvironmentFile=/opt/evc-mesh/.env.prod
ExecStart=/opt/evc-mesh/bin/mesh-api
Restart=on-failure
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
```

Key commands:

| Action | Command |
|--------|---------|
| Status | `sudo systemctl status mesh-api` |
| Start  | `sudo systemctl start mesh-api` |
| Stop   | `sudo systemctl stop mesh-api` |
| Restart | `sudo systemctl restart mesh-api` |
| Logs (live) | `sudo journalctl -u mesh-api -f` |
| Logs (last 100) | `sudo journalctl -u mesh-api -n 100` |

---

## Health check

```bash
curl -s http://localhost:8005/health
# Expected: {"service":"evc-mesh-api","status":"ok"}
```

---

## Environment variables

The full reference — every variable, its real default, and whether the container
actually receives it — lives in
[self-hosting.md](self-hosting.md#environment-variables-reference). It is
written from the code rather than from memory; do not maintain a second copy
here.

Two naming traps this runbook used to fall into:

- There is **no `DATABASE_URL`, `REDIS_URL` or `MESH_JWT_SECRET`.** The API
  reads component variables (`DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`,
  `DB_NAME`, `DB_SSL_MODE`; `REDIS_HOST`, `REDIS_PORT`, …) and the JWT secret is
  plain `JWT_SECRET`. `NATS_URL` is real.
- The **Docker Compose** deployment reads `deploy/docker/mesh/.env`, not
  `.env.prod` and not the repo-root `.env`. The `/opt/evc-mesh/.env.prod` paths
  in this runbook belong to the systemd/binary deployment described here, which
  is a different thing from the compose stack in the self-hosting guide.
