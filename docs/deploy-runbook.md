# evc-mesh Deploy Runbook

Hands-on guide for deploying the evc-mesh API server on prod (systemd, bare-metal / VPS).

---

## Prerequisites

- SSH access to prod host
- `gh` CLI authenticated (or direct `git` access to the repo)
- PostgreSQL client (`psql`) for manual migration steps when needed
- `systemctl` access (sudo or dedicated deploy user)

---

## Normal deploy (no out-of-order migrations)

```bash
# 1. Build binary locally or pull from CI artifact
go build -o bin/api ./cmd/api

# 2. Copy binary to prod
scp bin/api deploy@prod-host:/opt/evc-mesh/api.new

# 3. On the prod host: swap binary + restart
ssh deploy@prod-host
  cd /opt/evc-mesh
  # Migrations run automatically on startup via goose.Up (WithAllowMissing).
  # Backup first:
  pg_dump -U mesh -d mesh -Fc -f /opt/evc-mesh/backups/pre-deploy-$(date +%Y%m%dT%H%M%S).dump
  # Swap binary atomically
  mv api api.bak && mv api.new api
  sudo systemctl restart evc-mesh-api
  # Verify
  sudo systemctl status evc-mesh-api
  curl -s http://localhost:8080/health | jq .
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

# 1. Check current goose version table
psql -U mesh -d mesh -c "SELECT version_id, is_applied FROM goose_db_version ORDER BY id;"

# 2. Manually apply the out-of-order migration
psql -U mesh -d mesh -f /tmp/the_migration.sql

# 3. Stamp goose so it knows the migration is applied
goose -dir /opt/evc-mesh/migrations -dbstring "postgres://mesh:mesh@localhost/mesh?sslmode=disable" up-to <version>

# 4. Deploy the new binary normally — it will start clean
sudo systemctl restart evc-mesh-api
```

**Escalate to Garfield / Riker if unsure.**

---

## Rollback

```bash
# Stop service
sudo systemctl stop evc-mesh-api

# Restore previous binary
cd /opt/evc-mesh && mv api api.failed && mv api.bak api

# Rollback migrations if the new version added migrations
# (goose down rolls back exactly one migration at a time)
goose -dir /opt/evc-mesh/migrations \
      -dbstring "postgres://mesh:mesh@localhost/mesh?sslmode=disable" \
      down

# Restart with old binary
sudo systemctl start evc-mesh-api
sudo systemctl status evc-mesh-api
```

> **Note**: rolling back data-destructive migrations (DROP TABLE, DELETE) requires restoring from the pre-deploy pg_dump backup.

---

## Systemd unit reference

Unit file location: `/etc/systemd/system/evc-mesh-api.service`

```ini
[Unit]
Description=evc-mesh API
After=network.target postgresql.service

[Service]
User=evc
EnvironmentFile=/opt/evc-mesh/.env
ExecStart=/opt/evc-mesh/api
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

Key commands:

| Action | Command |
|--------|---------|
| Status | `sudo systemctl status evc-mesh-api` |
| Start  | `sudo systemctl start evc-mesh-api` |
| Stop   | `sudo systemctl stop evc-mesh-api` |
| Restart | `sudo systemctl restart evc-mesh-api` |
| Logs (live) | `sudo journalctl -u evc-mesh-api -f` |
| Logs (last 100) | `sudo journalctl -u evc-mesh-api -n 100` |

---

## Health check

```bash
curl -s http://localhost:8080/health
# Expected: {"service":"evc-mesh-api","status":"ok"}
```

---

## Environment variables (`.env`)

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
