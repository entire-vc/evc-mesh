# Post-mortem: 2026-05-20 17:07 UTC — "Broken Build" Incident

**Severity**: P0 (perceived) → P2 (actual)
**Duration**: 17:07–17:13 UTC (6 minutes until rollback)
**Affected**: mesh.entire.host API (prod, tw-mesh)
**Resolution**: Garfield rolled back to `pre-pr58` binary at 17:13 UTC

---

## Timeline

| Time (UTC) | Event |
|---|---|
| 07:56 | Stable binary `pre-pr58` deployed (25.3MB, stripped) |
| 11:32 | `9e8251d` — docs: added ldflags cross-compile command (only `-X` flags, no `-s -w`) |
| 15:46 | PR #65 merged and deployed — `70fa6a9` |
| 16:40 | Binary `164026` (25.3MB) deployed — CI artifact after PR #65 |
| 16:41 | Binary `164133-pre-ldflags` (35.8MB) built manually by Garfield — **debug build without `-s -w`** |
| 17:04 | PR #67 merged — `f109c82` — `PATCH /auth/me` + Profile UI |
| 17:06 | Binary `2006` (25.3MB) deployed — CI artifact after PR #67 |
| 17:07:14 | **Broken binary deployed** (35.8MB, from `f109c82`, no `-s -w`) — PID 2788670 |
| 17:07:19 | Health check fires: `GET /api/v1/version → 401` |
| 17:07:28 | `POST /api/v1/auth/login → 200` — **login actually works** |
| 17:08:15 | `event_bus_messages_pkey` duplicate key error (pre-existing, continues post-rollback) |
| 17:10:14 | `POST /api/v1/auth/login → 401` — monitoring script with bad credentials |
| 17:12:57 | Garfield issues `systemctl stop mesh-api` |
| 17:13:16 | `pre-pr58` binary restored and started — PID 2791347 |
| 17:13:32 | `GET /api/v1/version → 401` still occurring on rollback binary — confirms issue is structural |
| 17:19:23 | `event_bus_messages_pkey` duplicate key error continues after rollback |

---

## Root Causes

### RC-1: Manual build without `-s -w` → 35.8MB debug binary

**Forensic proof**: `go version -m` embedded in broken binary confirms `vcs.revision=f109c82ec8f8a1cf20000939f921bc4ef3acc887`, `vcs.modified=false`. Correct commit, clean tree.

The docs commit (`9e8251d`) at 11:32 UTC documented a manual build command:
```bash
GOOS=linux GOARCH=amd64 go build \
  -ldflags "-X main.BuildSHA=${SHA} -X main.BuildTime=${BUILT}" \
  -o mesh-api ./cmd/api
```

This adds build metadata (`-X`) but **omits `-s -w`** (strip debug symbols + DWARF). CI uses `go build -o bin/api ./cmd/api` with no stripping either. The stable 25.3MB binaries come from an older build script that DOES include `-s -w`.

Result: valid binary, correct code, but 42% larger (debug info takes 10.5MB extra). **Does not affect runtime behavior.**

### RC-2: Wrong health check URL → false alarm

The monitoring script and Garfield's manual health checks used `/api/v1/version`. This path does not exist in the codebase (the real endpoint is `/api/version`, registered on the root Echo instance with no auth).

In Echo v4, the `api := v1.Group("")` group registers DualAuth middleware for all routes under the `/api/v1/` prefix. When a request arrives for `/api/v1/version` (no specific handler), Echo still runs the group's middleware chain — DualAuth executes before the 404 handler and returns:

```json
{"code": 401, "message": "Authentication required"}
```

This is observable: any unauthenticated request to an unknown path under `/api/v1/` returns 401, not 404. Verified on the current running binary:

```bash
$ curl -s http://127.0.0.1:8005/api/v1/totally-random-path
{"code":401,"message":"Authentication required"}

$ curl -s http://127.0.0.1:8005/api/version
# → 404 (endpoint not present in pre-pr58, correct)
```

The monitoring 401s persisted after rollback to `pre-pr58` at 17:13:32, confirming the issue is structural, not binary-specific.

### RC-3: event_bus duplicate key (pre-existing, NOT caused by this deploy)

`pq: duplicate key value violates unique constraint "event_bus_messages_pkey" (23505)` first appeared at 17:08:15 and continued at 17:19:23 (after rollback). The stable pre-pr58 binary exhibits the same error. This is a separate pre-existing bug in the eventbus persistence layer — likely UUID collision in message ID generation or a seq-vs-UUID mismatch. **Out of scope for this post-mortem.**

---

## What DID and DID NOT Happen

| Claim | Reality |
|---|---|
| `POST /auth/login → 401 "Authentication required"` | Login returned **200** at 17:07:28. The 401s on login were wrong credentials from a monitoring script. The message was "invalid email or password", not "Authentication required". |
| `GET /api/v1/version → 401` | **True**, but caused by Echo middleware routing on a non-existent path — happens on ALL binaries. Not a PR #67 regression. |
| `POST /agents/heartbeat → 401` | **Pre-existing intermittent issue** (visible in logs at 17:00:47, before the deploy). Different agents with expired/rotated keys. Normal behavior. |
| PR #67 broke public endpoints | **False.** PR #67 only added `api.PATCH("/auth/me", authHandler.UpdateMe)` — one line to the protected group. Login route (`authGroup.POST("/login", ...)`) was untouched. |

---

## Prevention

### P1 — Standardize build flags (merged in this PR)

The build script and documentation must use:
```bash
go build -ldflags "-s -w -X main.BuildSHA=${SHA} -X main.BuildTime=${BUILT}" \
  -o mesh-api ./cmd/api
```

Added to `docs/self-hosting.md`. CI `ci.yml` updated to use the same flags.

### P2 — Add a public health/version endpoint under `/api/v1/`

Register `GET /api/v1/healthz/version` on the root `e` instance (not under `api` group) so monitoring scripts can use the versioned path without triggering DualAuth.

Done: `e.GET("/api/v1/healthz/version", versionHandler)` added to main.go.

### P3 — Fix deploy script health check URL

The deploy script (wherever it lives) must use `/api/version` or `/api/v1/healthz/version`, not `/api/v1/version`. See P2.

### P4 — CI-only deploys to prod

Manual builds on the server should be prohibited for prod deploys. The deploy process must:
1. Download the artifact built by the CI pipeline for the merged commit SHA
2. Verify artifact SHA matches the expected commit
3. Deploy

This prevents the class of errors where a dev-workstation or server-side build (with different toolchain state, no `-s -w`, etc.) gets deployed.

### P5 — Regression test: public endpoint accessibility

Add integration test:
```go
// public endpoints must return non-401 without Authorization header
assertPublic(t, http.MethodPost, "/api/v1/auth/login", ...)
assertPublic(t, http.MethodGet,  "/api/version", ...)
assertPublic(t, http.MethodGet,  "/api/v1/healthz/version", ...)
```

### P6 — event_bus duplicate key (tracked separately)

See separate task for `event_bus_messages_pkey` investigation. Root cause: likely UUID collision or idempotency issue in the eventbus PG writer goroutine.

---

## Summary

**The binary was correct.** The code from PR #67 was sound — route registration was unchanged for public endpoints. The perceived outage was caused by:

1. Garfield's manual deploy overwriting the CI binary with a debug build (no `-s -w`) — harmless to functionality but alarming in size
2. The monitoring/health-check script querying the wrong URL (`/api/v1/version` → always 401 due to Echo middleware behavior on unregistered paths)
3. The `event_bus` errors, which predated this deploy and continued after rollback

The rollback to `pre-pr58` was unnecessary. PR #67's changes (`PATCH /auth/me`) are safe and should be re-deployed from the CI artifact.

— Linus, 2026-05-20
