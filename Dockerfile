# Stage 1: Build Go backends
FROM golang:1.22-alpine AS go-builder
RUN apk add --no-cache git
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/mesh-api ./cmd/api
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/mesh-mcp ./cmd/mcp

# Stage 2: Build frontend
FROM node:20-alpine AS web-builder
RUN corepack enable
WORKDIR /app
COPY web/package.json web/pnpm-lock.yaml ./
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
