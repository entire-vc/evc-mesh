# Stage 1: Build Go backends
FROM golang:1.25-alpine AS go-builder
RUN apk add --no-cache git
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/mesh-api ./cmd/api
# The MCP server is NOT built here: it lives in entire-vc/evc-mesh-mcp. This
# repo's copy was a duplicate and was deleted (Mesh #e85e4e05). The image that
# self-hosting actually uses is deploy/docker/mesh/Dockerfile, which installs
# the MCP server from that module at a pinned SHA.

# Stage 2: Build frontend
FROM node:22-alpine AS web-builder
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
COPY --from=web-builder /app/dist /srv/web
COPY migrations/ /app/migrations/
WORKDIR /app
EXPOSE 8005
# nosemgrep: dockerfile.security.missing-user.missing-user — deliberately root for now (self-hosting.md); switching to a non-root USER here without also fixing volume ownership on migrations/uploads can silently break writes at container start, and that would only surface in prod. Tracked as a separate hardening card, not fixed in Mesh #48f243e4.
CMD ["mesh-api"]
