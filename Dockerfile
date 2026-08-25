# syntax=docker/dockerfile:1

# ---- web ---------------------------------------------------------------------
# The SPA is compiled here rather than expected in the build context, so
# `docker build .` alone produces a complete image on any machine.
FROM node:22-alpine AS web

WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund

COPY web/ ./
RUN npm run build

# ---- build -------------------------------------------------------------------
FROM golang:1.27-alpine AS build

WORKDIR /src

# Dependencies first, so a source-only change does not re-download the module cache.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
# The built SPA is embedded into the binary via web/embed.go.
COPY --from=web /web/dist ./web/dist

ARG VERSION=dev
# CGO off keeps the binary fully static, so the runtime stage needs no libc.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/klaras ./cmd/klaras

# ---- runtime -----------------------------------------------------------------
FROM alpine:3.22

# ca-certificates: the metadata providers are HTTPS.
# tzdata: timestamps are shown in the user's timezone.
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -u 1000 -h /app klaras

# OCI labels. Unraid reads these for the container's version and links, and
# compares the registry digest for the tag against the local one to decide
# whether an update is available.
ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED
LABEL org.opencontainers.image.title="Klaras Library" \
      org.opencontainers.image.description="Fast self-hosted ebook library with Kobo sync" \
      org.opencontainers.image.source="https://github.com/Karlmit/Klaras-Library" \
      org.opencontainers.image.url="https://github.com/Karlmit/Klaras-Library" \
      org.opencontainers.image.documentation="https://github.com/Karlmit/Klaras-Library#readme" \
      org.opencontainers.image.licenses="AGPL-3.0-or-later" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}"

WORKDIR /app
COPY --from=build /out/klaras /usr/local/bin/klaras

# The library is bind-mounted; these exist so a first run without mounts still
# starts rather than failing on a missing path.
RUN mkdir -p /library /ingest /cache && chown -R klaras:klaras /library /ingest /cache
USER klaras

ENV KLARAS_LISTEN_ADDR=:8083 \
    KLARAS_LIBRARY_ROOT=/library \
    KLARAS_INGEST_DIR=/ingest \
    KLARAS_CACHE_DIR=/cache

EXPOSE 8083

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8083/healthz >/dev/null || exit 1

ENTRYPOINT ["klaras"]
CMD ["serve"]
