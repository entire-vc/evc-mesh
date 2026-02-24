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

# 2. Copy environment file
cp .env.example .env

# 3. Edit .env -- at minimum, change JWT_SECRET!
#    nano .env
#    JWT_SECRET=your-strong-secret-at-least-32-chars

# 4. Start infrastructure (PostgreSQL, Redis, NATS, MinIO)
docker compose up -d

# 5. Build and start the API server
go run ./cmd/api

# 6. In a separate terminal, start the frontend
cd web && pnpm install && pnpm dev
```

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

### Authentication

| Variable | Default | Description |
|----------|---------|-------------|
| `JWT_SECRET` | `change-me-in-production` | JWT signing secret (use 32+ chars) |
| `CASDOOR_ENDPOINT` | *(empty)* | Casdoor SSO endpoint (optional) |
| `CASDOOR_CLIENT_ID` | *(empty)* | Casdoor client ID (optional) |
| `AGENT_KEY_PREFIX` | `agk` | Prefix for agent API keys |

---

## Production Deployment

### Security Checklist

1. **JWT_SECRET** -- Generate a strong random secret (32+ characters):
   ```bash
   openssl rand -base64 32
   ```

2. **Database password** -- Change `DB_PASSWORD` from the default:
   ```bash
   # Also update docker-compose.yml POSTGRES_PASSWORD
   DB_PASSWORD=your-strong-db-password
   ```

3. **MinIO credentials** -- Change from defaults:
   ```bash
   S3_ACCESS_KEY_ID=your-access-key
   S3_SECRET_ACCESS_KEY=your-secret-key
   # Also update docker-compose.yml MINIO_ROOT_USER and MINIO_ROOT_PASSWORD
   ```

4. **Redis password** -- Set a password:
   ```bash
   REDIS_PASSWORD=your-redis-password
   # Add requirepass to Redis container command in docker-compose.yml
   ```

5. **CORS** -- Configure allowed origins (currently allows `*`). Update the API server configuration for your domain.

6. **TLS** -- Set up a reverse proxy (nginx or Caddy) with TLS termination:

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

Docker Compose creates four named volumes:

| Volume | Container | Path | Description |
|--------|-----------|------|-------------|
| `postgres_data` | postgres | `/var/lib/postgresql/data` | Database storage |
| `redis_data` | redis | `/data` | Redis persistence (RDB/AOF) |
| `nats_data` | nats | `/data` | NATS JetStream storage |
| `minio_data` | minio | `/data` | Object storage (artifacts) |

To list volumes:
```bash
docker volume ls | grep evc-mesh
```

---

## Backup & Restore

### PostgreSQL

**Backup:**
```bash
docker compose exec postgres pg_dump -U mesh mesh > backup_$(date +%Y%m%d).sql
```

**Restore:**
```bash
docker compose exec -T postgres psql -U mesh mesh < backup_20250224.sql
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

JetStream stores data on disk in the `nats_data` volume. For backup:
```bash
docker compose stop nats
docker run --rm -v evc-mesh_nats_data:/data -v $(pwd):/backup alpine \
  tar czf /backup/nats_backup.tar.gz /data
docker compose start nats
```

### Full Backup Script

```bash
#!/bin/bash
BACKUP_DIR="./backups/$(date +%Y%m%d_%H%M%S)"
mkdir -p "$BACKUP_DIR"

# PostgreSQL
docker compose exec -T postgres pg_dump -U mesh mesh > "$BACKUP_DIR/postgres.sql"

# MinIO
mc mirror local/mesh-artifacts "$BACKUP_DIR/artifacts/"

# NATS (stop briefly)
docker compose stop nats
docker run --rm -v evc-mesh_nats_data:/data -v "$BACKUP_DIR":/backup alpine \
  tar czf /backup/nats.tar.gz /data
docker compose start nats

echo "Backup complete: $BACKUP_DIR"
```

---

## Health Checks

All infrastructure containers have built-in health checks. Additionally:

| Service | Health Check | Expected |
|---------|-------------|----------|
| API | `curl http://localhost:8005/health` | `{"status":"ok","service":"evc-mesh"}` |
| PostgreSQL | `docker compose exec postgres pg_isready -U mesh` | `accepting connections` |
| Redis | `docker compose exec redis redis-cli ping` | `PONG` |
| NATS | `curl http://localhost:8223/healthz` | `ok` |
| MinIO | `docker compose exec minio mc ready local` | exit code 0 |

Check all containers at once:
```bash
docker compose ps
```

All services should show `healthy` status.

---

## Troubleshooting

### PostgreSQL connection refused

**Symptom:** API fails to start with `connection refused` on port 5437.

**Solution:** Wait for PostgreSQL to fully initialize:
```bash
docker compose up -d postgres
# Wait for health check
until docker compose exec postgres pg_isready -U mesh; do sleep 1; done
# Then start the API
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
docker compose logs nats | grep "JetStream"
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

Adjust ports in `docker-compose.yml` and `.env` if needed.

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
   # API
   go build -o evc-mesh-api ./cmd/api && ./evc-mesh-api

   # Frontend
   cd web && pnpm install && pnpm build && pnpm start
   ```

4. Verify health:
   ```bash
   curl http://localhost:8005/health
   ```
