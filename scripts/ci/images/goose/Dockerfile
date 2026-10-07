# CI tool image: keep the Go compiler for the four existing Go test jobs, and
# add only the pinned goose CLI they currently install at job start.
FROM golang:1.25-alpine@sha256:78c9e40f10516d7070156948a1747347cc5093022f06deeff3c9decd5faa3af7

ARG GOOSE_VERSION
ARG SOURCE_COMMIT

RUN test "$(go version | awk '{print $3}')" = "go1.25.14" \
    && test "$(go env GOOS)/$(go env GOARCH)" = "linux/amd64" \
    && test -n "${GOOSE_VERSION}" \
    && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOBIN=/usr/local/bin \
       go install "github.com/pressly/goose/v3/cmd/goose@${GOOSE_VERSION}" \
    && goose -version 2>&1 | grep -F "${GOOSE_VERSION}"

LABEL org.opencontainers.image.source="https://git.entire.host/entire-vc/evc-mesh" \
      org.opencontainers.image.revision="${SOURCE_COMMIT}" \
      org.opencontainers.image.version="${GOOSE_VERSION}" \
      org.opencontainers.image.architecture="linux/amd64"
