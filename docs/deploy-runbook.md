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

```bash
# 1. Build binary locally or pull from CI artifact
go build -o bin/mesh-api ./cmd/api

# 2. Copy binary to prod
scp bin/mesh-api root@prod-host:/opt/evc-mesh/bin/mesh-api.new

# 3. On the prod host: backup DB, swap binary, restart
ssh root@prod-host

  cd /opt/evc-mesh

  # Backup DB (Postgres runs in docker-compose — use docker exec)
  docker exec <postgres-container> pg_dump -U mesh -d mesh -Fc \
    > /opt/evc-mesh/db-backups/pre-deploy-$(date +%Y%m%dT%H%M%S).dump

  # Swap binary atomically (timestamped .bak matches prod convention)
  mv bin/mesh-api bin/mesh-api.bak.$(date +%Y%m%d-%H%M%S)
  mv bin/mesh-api.new bin/mesh-api

  # Restart — goose.Up(WithAllowMissing) runs migrations on startup automatically
  sudo systemctl restart mesh-api

  # Verify
  sudo systemctl status mesh-api
  curl -s http://localhost:8005/health | jq .
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
