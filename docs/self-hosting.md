# Self-Hosting Guide

## Prerequisites

- **Docker** and **Docker Compose v2+**
- **Go 1.22+** (for building the API server)
- **Node.js 18+** and **pnpm** (for the frontend)
- 2 GB RAM minimum
- 10 GB disk space

## Quick Start

```bash
# 1. Clone the repository
git clone https://github.com/entire-vc/evc-mesh && cd evc-mesh

# 2. Create your env file -- at minimum, change JWT_SECRET!
cp .env.example .env
#    nano .env
#    JWT_SECRET=your-strong-secret-at-least-32-chars

# 3. Start infrastructure (PostgreSQL, Redis, NATS, MinIO)
cd deploy/docker/mesh && docker compose up -d
#    or from the repo root: make docker-up

# 4. Build and start the API server
cd ../..
go run ./cmd/api

# 5. In a separate terminal, start the frontend
cd web && pnpm install && pnpm dev
```

A fresh install has **no users and no default password** — open
http://localhost:3000/register and create the first account, or see
[Seeding the first admin](#seeding-the-first-admin) to have the server create it
for you.

The services will be available at:

| Service | URL | Description |
|---------|-----|-------------|
| Frontend | http://localhost:3000 | Web UI (React) |
| API | http://localhost:8005 | REST API + WebSocket |
| MCP (SSE) | http://localhost:8081 | MCP over SSE (optional) |
| MinIO Console | http://localhost:9003 | Object storage UI |

---

## Environment Variables Reference

### Server

| Variable | Default | Description |
|----------|---------|-------------|
| `SERVER_HOST` | `0.0.0.0` | API server bind host |
| `SERVER_PORT` | `8005` | API server bind port |
| `SERVER_READ_TIMEOUT` | `30s` | HTTP read timeout |
| `SERVER_WRITE_TIMEOUT` | `30s` | HTTP write timeout |

### PostgreSQL

| Variable | Default | Description |
|----------|---------|-------------|
| `DB_HOST` | `localhost` | PostgreSQL host |
| `DB_PORT` | `5437` | PostgreSQL port (mapped from container 5432) |
| `DB_USER` | `mesh` | PostgreSQL user |
| `DB_PASSWORD` | `mesh` | PostgreSQL password |
| `DB_NAME` | `mesh` | Database name |
| `DB_SSL_MODE` | `disable` | SSL mode (`disable`, `require`, `verify-full`) |

### Redis

| Variable | Default | Description |
|----------|---------|-------------|
| `REDIS_HOST` | `localhost` | Redis host |
| `REDIS_PORT` | `6383` | Redis port (mapped from container 6379) |
| `REDIS_PASSWORD` | *(empty)* | Redis password |
| `REDIS_DB` | `0` | Redis database number |

### NATS JetStream

| Variable | Default | Description |
|----------|---------|-------------|
| `NATS_URL` | `nats://localhost:4223` | NATS connection URL |

### S3 / MinIO

| Variable | Default | Description |
|----------|---------|-------------|
| `S3_ENDPOINT` | `localhost:9002` | S3-compatible endpoint |
| `S3_ACCESS_KEY_ID` | `minioadmin` | S3 access key |
| `S3_SECRET_ACCESS_KEY` | `minioadmin` | S3 secret key |
| `S3_BUCKET` | `mesh-artifacts` | Bucket name for artifacts |
| `S3_REGION` | `us-east-1` | S3 region |
| `S3_USE_SSL` | `false` | Use SSL for S3 connections |
| `S3_PUBLIC_URL` | *(empty)* | Public base URL for artifact downloads (e.g. `https://mesh.example.com/s3`). Leave empty to use presigned S3 URLs. |

### Authentication

| Variable | Default | Description |
|----------|---------|-------------|
| `JWT_SECRET` | `change-me-in-production` | JWT signing secret (use 32+ chars) |
| `CASDOOR_ENDPOINT` | *(empty)* | Casdoor SSO endpoint (optional) |
| `CASDOOR_CLIENT_ID` | *(empty)* | Casdoor client ID (optional) |
| `AGENT_KEY_PREFIX` | `agk` | Prefix for agent API keys |
| `MESH_ALLOW_REGISTRATION` | `true` | Whether `POST /auth/register` accepts new signups. See [Closing registration](#closing-registration) below. |

### First admin

| Variable | Default | Description |
|----------|---------|-------------|
| `MESH_SEED_ADMIN` | `false` | Create the first admin at boot. Runs **only** when the database has zero users. |
| `MESH_ADMIN_EMAIL` | `admin@localhost` | Email for the seeded admin |
| `MESH_ADMIN_PASSWORD` | *(generated)* | Password for the seeded admin. If unset, a strong one is generated and printed **once** at boot. |
| `MESH_ADMIN_NAME` | `Admin` | Display name for the seeded admin |

See [Seeding the first admin](#seeding-the-first-admin) below.

### CORS

| Variable | Default | Description |
|----------|---------|-------------|
| `MESH_CORS_ORIGINS` | `*` | Comma-separated list of allowed origins (e.g. `https://mesh.example.com,https://app.example.com`). Use `*` for development only. |

### Rate Limiting

| Variable | Default | Description |
|----------|---------|-------------|
| `MESH_RATE_LIMIT_ENABLED` | `true` | Enable or disable rate limiting globally |
| `MESH_RATE_LIMIT_AUTH_RPM` | `20` | Maximum requests per minute for auth endpoints (per IP) |
| `MESH_RATE_LIMIT_API_RPM` | `600` | Maximum requests per minute for API endpoints (per authenticated actor) |

### Spark Catalog

| Variable | Default | Description |
|----------|---------|-------------|
| `MESH_SPARK_URL` | `https://spark.entire.vc` | Spark agent catalog API base URL |
| `MESH_SPARK_ENABLED` | `false` | Enable Spark catalog routes (`/api/v1/spark/...`) |

---

## Seeding the first admin

A fresh install has **no users and no default password**. Until an account
exists, every login attempt correctly returns `401 invalid email or password`.

There are two ways to create that first account.

### Option 1 — register in the web UI (simplest)

Open `/register` and sign up. The first account you register is yours and gets
its own workspace. Registration is a public endpoint by default, so nothing
needs to be configured first — see [Closing registration](#closing-registration)
once you have that first account and want to lock the door behind you.

Passwords must be 8+ characters with an uppercase letter, a lowercase letter,
and a digit — a weaker one is rejected with `400`, not `401`.

### Option 2 — seed at boot (scripted or headless installs)

Start the API once with `MESH_SEED_ADMIN=true`:

```bash
MESH_SEED_ADMIN=true \
MESH_ADMIN_EMAIL=you@example.com \
MESH_ADMIN_PASSWORD='<strong-password>' \
docker compose -f docker-compose.prod.yml up -d
```

Omit `MESH_ADMIN_PASSWORD` and the server generates a strong one and prints it
once:

```
[bootstrap] ────────────────────────────────────────────────────────
[bootstrap] First admin created: admin@localhost
[bootstrap] Generated password:  XaSfco7rmK4jSkhSGZETGM6a
[bootstrap] This password is shown ONCE and is not stored anywhere.
```

Copy it before the log scrolls away, then change it after logging in.

### Two rules worth knowing

**The seed only runs on a database with zero users.** This is deliberate — it
means an accidental `MESH_SEED_ADMIN=true` on a running instance can never
overwrite or re-create accounts. If you registered a user first and *then* set
the flag, the seed will not run.

**The seed only runs at API startup.** Setting the environment variable on an
already-running container does nothing; restart the API for it to take effect.

### Reading the boot log

The API states what it did on every boot, so you never have to guess:

| Log line | Meaning | What to do |
|---|---|---|
| `First admin created: <email>` | Seed succeeded | Log in with that account |
| `MESH_SEED_ADMIN=true, but the database already has N user(s)` | Skipped by design | Log in with an existing account |
| `The database has no users yet, so nobody can log in.` | No account, no seed requested | Register at `/register`, or restart with `MESH_SEED_ADMIN=true` |
| `first-admin seed FAILED: ...` | Seed errored | Fix the reported cause and restart |

### Lost the admin password?

A self-hosted install has no password-reset email. Passwords are stored as
bcrypt hashes, so the fix is to write a new hash directly into the `users` row.

Generate a bcrypt hash (any bcrypt tool works; Python's `bcrypt` package is the
shortest path):

```bash
python3 -c "import bcrypt; print(bcrypt.hashpw(b'<new-password>', bcrypt.gensalt()).decode())"
```

Then set it for the account, and log in with the new password:

```bash
docker compose exec postgres psql -U mesh -d mesh \
  -c "UPDATE users SET password_hash = '<hash-from-above>' WHERE email = 'you@example.com';"
```

The new password must satisfy the same rules as registration (8+ characters,
upper, lower, digit) or you will not be able to change it later from the UI.

If no account is recoverable at all and the install holds nothing you need, you
can wipe the users and let the seed run again on the next boot:

```bash
docker compose exec postgres psql -U mesh -d mesh \
  -c 'TRUNCATE users, workspaces, workspace_members CASCADE;'
# then restart the API with MESH_SEED_ADMIN=true
```

⚠️ This deletes **all** workspace content — tasks, projects, comments,
artifacts — owned by those users. Back up first (see
[Backup & Restore](#backup--restore)).

### Closing registration

By default `POST /auth/register` is a **public, unauthenticated endpoint** —
anyone who can reach the instance can create an account. Each new account gets
its own isolated workspace, so a stranger cannot see anyone else's data, but on
an internet-facing self-host instance this still means: unlimited accounts
consuming your database/S3/JetStream, and behavior most operators running a
small team (3-10 people) do not expect from a "closed" tool.

Set `MESH_ALLOW_REGISTRATION=false` and restart the API to close it:

```bash
MESH_ALLOW_REGISTRATION=false docker compose -f docker-compose.prod.yml up -d
```

With the flag off:

- `POST /auth/register` returns `403` with
  `"registration is closed on this instance — ask an admin for an invite"`.
- The web UI's login page stops showing the "Register" link (via
  `GET /auth/config`), and `/register` shows a "Registration is closed" notice
  instead of the signup form.
- **The very first user can always register**, regardless of the flag — the
  server checks `COUNT(*) FROM users` and only enforces the flag once at least
  one account exists. This is deliberate: it is the same bootstrap invariant
  [Seeding the first admin](#seeding-the-first-admin) relies on, and without it
  a closed instance could never be stood up in the first place. So the safe
  sequence for a brand-new install you intend to keep closed is: deploy with
  the default (`MESH_ALLOW_REGISTRATION` unset/`true`) → register the first
  admin account → **then** set `MESH_ALLOW_REGISTRATION=false` and restart.

**Closed registration does not close the instance to new people** — it closes
the walk-up path. An existing member with `PermManageMembers` can still invite
anyone from a workspace's Members page (or `POST /workspaces/:ws_id/invites`),
which is a **separate code path** from `/auth/register` and is unaffected by
this flag either way:

```bash
curl -X POST https://mesh.yourdomain.com/api/v1/workspaces/<ws_id>/invites \
  -H "Authorization: Bearer <your-jwt>" \
  -H "Content-Type: application/json" \
  -d '{"email":"newperson@example.com","role":"member"}'
```

The invited person gets a link to `/accept-invite/<token>`, sets their own
password there, and is added straight to that workspace — no open `/register`
needed. `GET /api/v1/invites/:token` and `POST /api/v1/invites/:token/accept`
stay public precisely so this path works on a fully closed instance. If SMTP
is not configured (`SMTP_HOST` unset), the invite is not emailed; the link is
logged instead (`[email] SMTP not configured — invite link for ...`) so you
can pass it along manually.

**Default stays `true` for now.** Flipping the default to `false` would be a
breaking change for every instance that already relies on the current
"register at `/register`" flow described above, so this release only adds the
flag — a future release may switch the default, called out in its own
CHANGELOG entry.

---

## Production Deployment

### Docker Compose (Production)

For production, use `deploy/docker/mesh/docker-compose.prod.yml` instead of `deploy/docker/mesh/docker-compose.yml`. It builds all services from source and adds nginx, Prometheus, and Grafana.

```bash
# Fill in production env vars in deploy/docker/mesh/.env
# For a production template, start from deploy/docker/mesh/.env.prod.example

# Build and start all services
cd deploy/docker/mesh
docker compose -f docker-compose.prod.yml --env-file .env up -d --build
# or from the repo root: make docker-prod-up

# Verify all services are healthy
docker compose -f docker-compose.prod.yml ps
```

Services included in `docker-compose.prod.yml`:

| Service | Port | Description |
|---------|------|-------------|
| `postgres` | *(internal)* | PostgreSQL 16 — required env: `POSTGRES_PASSWORD` |
| `redis` | *(internal)* | Redis 7 with password — required env: `REDIS_PASSWORD` |
| `nats` | *(internal)* | NATS 2.10 with JetStream enabled |
| `minio` | *(internal)* | MinIO object storage — required env: `MINIO_ACCESS_KEY`, `MINIO_SECRET_KEY` |
| `api` | `${API_PORT:-8005}` | Mesh API server (Go binary, runs DB migrations on startup) |
| `mcp` | `${MCP_PORT:-8081}` | MCP server in SSE mode for remote agents |
| `nginx` | `${HTTP_PORT:-80}` | Nginx serving the React SPA, proxying `/api` and `/ws` to the API |
| `prometheus` | `${PROMETHEUS_PORT:-9090}` | Prometheus scraping `/metrics` from the API |
| `grafana` | `${GRAFANA_PORT:-3001}` | Grafana dashboards — default password: `${GRAFANA_PASSWORD:-admin}` |

Required environment variables for production:

```bash
POSTGRES_PASSWORD=your-strong-db-password
REDIS_PASSWORD=your-redis-password
JWT_SECRET=your-32-char-minimum-secret
MINIO_ACCESS_KEY=your-minio-access-key
MINIO_SECRET_KEY=your-minio-secret-key
# Optional:
CORS_ORIGINS=https://mesh.yourdomain.com
GRAFANA_PASSWORD=your-grafana-admin-password
```

### Security Checklist

1. **JWT_SECRET** -- Generate a strong random secret (32+ characters):
   ```bash
   openssl rand -base64 32
   ```

2. **Database password** -- Change `DB_PASSWORD` from the default:
   ```bash
   # Also update deploy/docker/mesh/.env
   DB_PASSWORD=your-strong-db-password
   ```

3. **MinIO credentials** -- Change from defaults:
   ```bash
   S3_ACCESS_KEY_ID=your-access-key
   S3_SECRET_ACCESS_KEY=your-secret-key
   # Also update deploy/docker/mesh/.env
   ```

4. **Redis password** -- Set a password:
   ```bash
   REDIS_PASSWORD=your-redis-password
   # The production compose file already passes this to redis-server
   ```

5. **CORS** -- Configure allowed origins (currently allows `*`). Update the API server configuration for your domain.

6. **Registration** -- If this instance is reachable from the public internet,
   set `MESH_ALLOW_REGISTRATION=false` **after** creating your first admin
   account, so strangers cannot self-register. See
   [Closing registration](#closing-registration). Add people afterward via
   workspace invites, not the open `/register` endpoint.

7. **Workspace isolation** -- Every endpoint that names workspace-owned data
   requires the caller to be a member of that workspace (agents: to belong to
   it), enforced for the whole authenticated API group rather than per route.
   A logged-in user from another workspace gets `403` on all of them.

   This covers any route whose path carries `:ws_id`, `:proj_id`, `:task_id`,
   `:artifact_id`, `:agent_id`, `:field_id` or `:init_id` — the tenant is
   resolved from whichever of those the route has, and membership is required
   for the workspace that comes out. A route whose parameter resolves to nothing
   (an unknown or deleted id) answers `403` rather than `404`: the guard runs
   before the handler, and it does not confirm which ids exist.

   The live event stream (`/ws`) applies the same rule. It is upgraded before any
   API middleware runs, so it checks membership itself: the `workspace=` query
   parameter is verified during the handshake, and every later
   `{"action":"subscribe"}` is authorised too — a connection may subscribe only
   to its own workspace channel, to projects inside that workspace, and to its
   own user's mention feed. Agent connections take their workspace from the agent
   key, never from the query string.

   Two consequences worth knowing:

   - Looking people up by name or email
     (`GET /workspaces/:ws_id/users/search`) searches the whole instance, not
     just the workspace — that is what makes "add an existing user" work. It is
     therefore restricted to roles that can manage members (owner/admin). On a
     shared instance hosting unrelated tenants, treat workspace admin as
     "can see that an account exists"; invite by email if that is not
     acceptable.
   - Renaming a workspace or changing its slug
     (`PATCH /workspaces/:ws_id`) is owner/admin only, because the slug appears
     in every `/w/<slug>/...` URL your team has saved.

8. **TLS** -- Set up a reverse proxy (nginx or Caddy) with TLS termination:

   **Caddy example (`Caddyfile`):**
   ```
   mesh.yourdomain.com {
     reverse_proxy /api/* localhost:8005
     reverse_proxy /ws localhost:8005
     reverse_proxy /* localhost:3000
   }
   ```

   **nginx example:**
   ```nginx
   server {
     listen 443 ssl;
     server_name mesh.yourdomain.com;

     ssl_certificate /etc/ssl/certs/mesh.pem;
     ssl_certificate_key /etc/ssl/private/mesh.key;

     location /api/ {
       proxy_pass http://localhost:8005;
     }

     location /ws {
       proxy_pass http://localhost:8005;
       proxy_http_version 1.1;
       proxy_set_header Upgrade $http_upgrade;
       proxy_set_header Connection "upgrade";
     }

     location / {
       proxy_pass http://localhost:3000;
     }
   }
   ```

---

## Data Persistence

Docker Compose uses bind mounts rooted in `deploy/docker/mesh/volumes/`:

| Host path | Container | Path | Description |
|-----------|-----------|------|-------------|
| `deploy/docker/mesh/volumes/postgres/data/` | postgres | `/var/lib/postgresql/data` | Database storage |
| `deploy/docker/mesh/volumes/redis/data/` | redis | `/data` | Redis persistence (RDB/AOF) |
| `deploy/docker/mesh/volumes/nats/data/` | nats | `/data` | NATS JetStream storage |
| `deploy/docker/mesh/volumes/minio/data/` | minio | `/data` | Object storage (artifacts) |
| `deploy/docker/mesh/volumes/prometheus/data/` | prometheus | `/prometheus` | Prometheus TSDB |
| `deploy/docker/mesh/volumes/grafana/data/` | grafana | `/var/lib/grafana` | Grafana state |

To inspect the bind mount directories:
```bash
find deploy/docker/mesh/volumes -maxdepth 2 -type d
```

---

## Backup & Restore

### PostgreSQL

**Backup:**
```bash
cd deploy/docker/mesh && docker compose exec postgres pg_dump -U mesh mesh > ../../../backup_$(date +%Y%m%d).sql
```

**Restore:**
```bash
cd deploy/docker/mesh && docker compose exec -T postgres psql -U mesh mesh < ../../../backup_20250224.sql
```

### MinIO (Artifacts)

**Backup:**
```bash
# Install mc (MinIO client) if not already installed
# brew install minio/stable/mc

# Configure MinIO alias
mc alias set local http://localhost:9002 minioadmin minioadmin

# Mirror to local directory
mc mirror local/mesh-artifacts ./backup-artifacts/
```

**Restore:**
```bash
mc mirror ./backup-artifacts/ local/mesh-artifacts
```

### NATS JetStream

JetStream stores data on disk in `deploy/docker/mesh/volumes/nats/data/`. For backup:
```bash
cd deploy/docker/mesh
docker compose stop nats
tar czf ../../../nats_backup.tar.gz volumes/nats/data
docker compose start nats
```

### Full Backup Script

```bash
#!/bin/bash
BACKUP_DIR="./backups/$(date +%Y%m%d_%H%M%S)"
mkdir -p "$BACKUP_DIR"

# PostgreSQL
(
  cd deploy/docker/mesh
  docker compose exec -T postgres pg_dump -U mesh mesh
) > "$BACKUP_DIR/postgres.sql"

# MinIO
mc mirror local/mesh-artifacts "$BACKUP_DIR/artifacts/"

# NATS (stop briefly)
(
  cd deploy/docker/mesh
  docker compose stop nats
  tar czf "../../../$BACKUP_DIR/nats.tar.gz" volumes/nats/data
  docker compose start nats
)

echo "Backup complete: $BACKUP_DIR"
```

---

## Health Checks

All infrastructure containers have built-in health checks. Additionally:

| Service | Health Check | Expected |
|---------|-------------|----------|
| API | `curl http://localhost:8005/health` | `{"status":"ok","service":"evc-mesh-api"}` |
| API version | `curl http://localhost:8005/api/version` | `{"commit":"abc1234","build_time":"...","version":"dev","environment":"dev","service":"evc-mesh-api"}` |
| PostgreSQL | `cd deploy/docker/mesh && docker compose exec postgres pg_isready -U mesh` | `accepting connections` |
| Redis | `cd deploy/docker/mesh && docker compose exec redis redis-cli ping` | `PONG` |
| NATS | `curl http://localhost:8223/healthz` | `ok` |
| MinIO | `cd deploy/docker/mesh && docker compose exec minio mc ready local` | exit code 0 |

Check all containers at once:
```bash
cd deploy/docker/mesh && docker compose ps
```

All services should show `healthy` status.

---

## Troubleshooting

### PostgreSQL connection refused

**Symptom:** API fails to start with `connection refused` on port 5437.

**Solution:** Wait for PostgreSQL to fully initialize:
```bash
cd deploy/docker/mesh
docker compose up -d postgres
# Wait for health check
until docker compose exec postgres pg_isready -U mesh; do sleep 1; done
# Then start the API
cd ../..
go run ./cmd/api
```

### MinIO bucket not found

**Symptom:** Artifact uploads fail with "bucket not found".

**Solution:** The API auto-creates the bucket on startup. If it fails, create manually:
```bash
mc alias set local http://localhost:9002 minioadmin minioadmin
mc mb local/mesh-artifacts
```

### NATS JetStream not available

**Symptom:** Events fail with "JetStream not available".

**Solution:** Ensure NATS started with JetStream enabled:
```bash
cd deploy/docker/mesh && docker compose logs nats | grep "JetStream"
# Should show: "JetStream is ready"
```

### Port conflicts

**Symptom:** `address already in use` errors.

**Solution:** Check what is using the ports:
```bash
lsof -i :5437  # PostgreSQL
lsof -i :6383  # Redis
lsof -i :4223  # NATS
lsof -i :9002  # MinIO S3
lsof -i :9003  # MinIO Console
lsof -i :8005  # API
lsof -i :3000  # Frontend
```

Adjust ports in `deploy/docker/mesh/docker-compose.yml` and `deploy/docker/mesh/.env` if needed.

### Frontend build fails

**Symptom:** `pnpm install` or `pnpm dev` fails.

**Solution:**
```bash
cd web
rm -rf node_modules .next
pnpm install
pnpm dev
```

### WebSocket connection drops

**Symptom:** Real-time updates stop working in the frontend.

**Solution:** If behind a reverse proxy, ensure WebSocket upgrade headers are forwarded (see the nginx config above). Also check that `SERVER_READ_TIMEOUT` and `SERVER_WRITE_TIMEOUT` are not too short for long-lived connections.

---

## Upgrading

1. Pull the latest code:
   ```bash
   git pull origin main
   ```

2. Run database migrations (applied automatically on API startup).

3. Rebuild and restart:
   ```bash
   # API — use make build-prod for cross-compilation with all ldflags
   make build-prod

   # Frontend
   cd web && pnpm install && pnpm build && pnpm start
   ```

4. Verify health and version:
   ```bash
   curl http://localhost:8005/health
   curl http://localhost:8005/api/version
   # → {"commit":"abc1234","build_time":"2026-05-20T...","version":"v1.2.3","environment":"prod","service":"evc-mesh-api"}
   ```
