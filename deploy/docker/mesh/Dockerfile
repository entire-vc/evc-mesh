# Stage 1: Build Go backends
FROM golang:1.25-alpine AS go-builder
RUN apk add --no-cache git
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

# mesh-mcp lives in a separate repository/module (github.com/entire-vc/evc-mesh-mcp) —
# evc-mesh/cmd/mcp was a duplicate of it that has been removed (see docs/self-hosting.md
# and the "MCP server" section for why). `go install <module>@<pinned-sha>` is
# proxy-cached and sumdb-verified — the same trust model `go mod download` above already
# applies to every third-party dependency this build fetches over the network, not a new
# class of requirement. Placed before `COPY . .` so this layer caches on the pin alone and
# is unaffected by evc-mesh source changes. `go install` names the binary after the
# module (`evc-mesh-mcp`); renamed below to `mesh-mcp` to match the `command:` in
# docker-compose.prod.yml and the systemd unit on mesh-vm. The -X target below is the
# exact symbol evc-mesh-mcp's own deploy workflow sets
# (.github/workflows/deploy-mesh-vm.yml, internal/mcp.BuildSHA) — not a guess — so
# `mesh-mcp --version` reports the pinned commit instead of falling back to "dev".
#
# Bumping MESH_MCP_REF is a routine dependency bump (like any go.mod version bump), not a
# release process — point it at a newer commit on evc-mesh-mcp's main branch.
ARG MESH_MCP_REF=0c04be5ba0120e4a5d9d24dbbce0d5914bb3bd3e
RUN CGO_ENABLED=0 GOOS=linux GOBIN=/bin go install -trimpath \
      -ldflags="-s -w -X github.com/entire-vc/evc-mesh-mcp/internal/mcp.BuildSHA=${MESH_MCP_REF}" \
      github.com/entire-vc/evc-mesh-mcp@${MESH_MCP_REF} && \
    mv /bin/evc-mesh-mcp /bin/mesh-mcp

COPY . .
# Build metadata for cmd/api's BuildSHA/BuildTime/BuildVersion/BuildEnv vars
# (see docs/deploy.md#build-metadata-ldflags) — derived from the .git
# directory already present in the build context (no .dockerignore excludes
# it), so /api/version reports the real commit with no extra self-hoster
# action. A `git clone` (including --depth 1) always has .git; only a
# tarball built without it falls back to "dev".
RUN GIT_SHA=$(git rev-parse HEAD 2>/dev/null || echo dev) && \
    GIT_VERSION=$(git describe --tags --always 2>/dev/null || echo dev) && \
    BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ) && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w \
      -X main.BuildSHA=${GIT_SHA} \
      -X main.BuildTime=${BUILD_TIME} \
      -X main.BuildVersion=${GIT_VERSION} \
      -X main.BuildEnv=prod" \
      -o /bin/mesh-api ./cmd/api

# Stage 2: Build frontend
FROM node:20-alpine AS web-builder
RUN corepack enable
WORKDIR /app
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
# pnpm-workspace.yaml declares patchedDependencies -> patches/@milkdown__ctx@7.22.1.patch.
# --frozen-lockfile applies patches during install, so the patch file has to be here
# BEFORE the install, not with the rest of web/ after it.
COPY web/patches/ ./patches/
RUN pnpm install --frozen-lockfile
COPY web/ .
RUN pnpm build

# Stage 3: Production image
FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata wget
COPY --from=go-builder /bin/mesh-api /usr/local/bin/mesh-api
COPY --from=go-builder /bin/mesh-mcp /usr/local/bin/mesh-mcp
COPY --from=web-builder /app/dist /srv/web
COPY migrations/ /app/migrations/
WORKDIR /app
EXPOSE 8005
CMD ["mesh-api"]
