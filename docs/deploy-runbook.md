# evc-mesh Deploy Runbook

Hands-on guide for deploying the evc-mesh API server on prod (systemd, bare-metal / VPS).

---

## Prerequisites

- SSH access to prod host
- `gh` CLI authenticated (or direct `git` access to the repo)
- `systemctl` access (root or sudo)
- Docker access (PostgreSQL runs in docker-compose — bare `psql`/`pg_dump` are not on the host)

---

## Normal deploy (no out-of-order migrations)

**Mandatory order (per [CLAUDE-workflow.md §1b Deploy Discipline](../CLAUDE-workflow.md)):**
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
  # goose CLI is not installed on the host; use the official docker image.
  # If this exits non-zero, STOP — do NOT proceed to the binary swap.
  DB_URL=$(grep ^DATABASE_URL /opt/evc-mesh/.env.prod | cut -d= -f2-)
  docker run --rm --network host \
    -v /opt/evc-mesh/migrations:/migrations \
    ghcr.io/pressly/goose:latest \
    goose -dir /migrations postgres "$DB_URL" up

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

## evc-mesh-mcp deploy (manual — no CI)

evc-mesh-mcp has no CD pipeline. **Mandatory order (per
[CLAUDE-workflow.md §1b Deploy Discipline](../CLAUDE-workflow.md)):**
`migrate (goose up)` → `binary swap` → `restart`. Never swap the binary before migrations pass.

> evc-mesh-mcp does not run its own migrations — it reads the same DB as the evc-mesh API.
> The goose step below ensures the shared schema is up-to-date before the new binary serves traffic.

```bash
# 1. Build for the prod target (cross-compile from Mac/Linux dev machine)
GOOS=linux GOARCH=amd64 go build -o evc-mesh-mcp ./cmd/mesh-mcp

# 2. Copy binary to prod
scp evc-mesh-mcp root@prod-host:/opt/evc-mesh-mcp/evc-mesh-mcp.new

# 3. On the prod host: run migrations FIRST, then swap binary
ssh root@prod-host

  # STEP 1 — Run evc-mesh DB migrations (fail-closed).
  # goose CLI is not installed on the host — use the official docker image.
  # If this exits non-zero, STOP — do NOT swap the binary.
  DB_URL=$(grep ^DATABASE_URL /opt/evc-mesh/.env.prod | cut -d= -f2-)
  docker run --rm --network host \
    -v /opt/evc-mesh/migrations:/migrations \
    ghcr.io/pressly/goose:latest \
    goose -dir /migrations postgres "$DB_URL" up

  # STEP 2 — Swap binary (only after migrations succeed)
  mv /opt/evc-mesh-mcp/evc-mesh-mcp /opt/evc-mesh-mcp/evc-mesh-mcp.bak.$(date +%Y%m%d-%H%M%S)
  mv /opt/evc-mesh-mcp/evc-mesh-mcp.new /opt/evc-mesh-mcp/evc-mesh-mcp

  # STEP 3 — Restart
  sudo systemctl restart evc-mesh-mcp

  # STEP 4 — Smoke test
  curl -sf http://localhost:8081/health || echo "SMOKE FAILED"
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
# goose CLI is not on the host — run via docker exec with the migrations dir mounted,
# or use a one-off goose container. Rolls back exactly one migration at a time:
docker run --rm \
  --network host \
  -v /opt/evc-mesh/migrations:/migrations \
  ghcr.io/pressly/goose:latest \
  goose -dir /migrations postgres \
  "$(grep DATABASE_URL /opt/evc-mesh/.env.prod | cut -d= -f2-)" \
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

## Environment variables (`.env.prod`)

| Variable | Description |
|----------|-------------|
| `DATABASE_URL` | PostgreSQL DSN |
| `REDIS_URL` | Redis address |
| `NATS_URL` | NATS address |
| `MESH_JWT_SECRET` | JWT signing secret |
| `MESH_SEED_ADMIN` | Set to `true` on first boot |
| `MESH_ADMIN_EMAIL` | Seed admin email |
| `MESH_ADMIN_PASSWORD` | Seed admin password (change immediately) |

See `internal/config/config.go` for the full list.
