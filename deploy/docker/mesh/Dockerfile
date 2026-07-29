# Stage 1: Build Go backends
FROM golang:1.25-alpine AS go-builder
RUN apk add --no-cache git
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Build metadata for cmd/api's BuildSHA/BuildTime/BuildVersion/BuildEnv vars
# (see docs/deploy.md#build-metadata-ldflags) — derived from the .git
# directory already present in the build context (no .dockerignore excludes
# it), so /api/version reports the real commit with no extra self-hoster
# action. A `git clone` (including --depth 1) always has .git; only a
# tarball built without it falls back to "dev". Scoped to the mesh-api build
# only: cmd/mcp has no matching vars, and -X against an unknown symbol is a
# silent linker no-op that just adds noise.
RUN GIT_SHA=$(git rev-parse HEAD 2>/dev/null || echo dev) && \
    GIT_VERSION=$(git describe --tags --always 2>/dev/null || echo dev) && \
    BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ) && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w \
      -X main.BuildSHA=${GIT_SHA} \
      -X main.BuildTime=${BUILD_TIME} \
      -X main.BuildVersion=${GIT_VERSION} \
      -X main.BuildEnv=prod" \
      -o /bin/mesh-api ./cmd/api
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/mesh-mcp ./cmd/mcp

# Stage 2: Build frontend
FROM node:20-alpine AS web-builder
RUN corepack enable
WORKDIR /app
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
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
