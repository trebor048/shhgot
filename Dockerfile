# ===========================================================================
# shhgit — container image
#
#   docker build -t shhgit .
#   docker run --rm -p 8080:8080 -v "$PWD/config.yaml:/app/config.yaml" shhgit
#
# Or use docker compose (see docker-compose.yml).
# ===========================================================================

ARG GO_VERSION=1.26
ARG NODE_VERSION=20

# ---------------------------------------------------------------------------
# Stage 1: build the React dashboard.
#
# The UI under cmd/shhgit/web is embedded into the Go binary with go:embed. A
# committed bundle lets `go build` work with no Node toolchain, but the image
# always rebuilds it from source so a release cannot ship a stale UI.
# ---------------------------------------------------------------------------
FROM node:${NODE_VERSION}-alpine AS web

WORKDIR /web

# Copy the manifests first so editing sources does not invalidate the npm layer.
COPY cmd/shhgit/web/package.json cmd/shhgit/web/package-lock.json ./
RUN npm ci --no-audit --no-fund

COPY cmd/shhgit/web/ ./
RUN npm run build

# ---------------------------------------------------------------------------
# Stage 2: build the static Go binary, embedding the dashboard built above.
# ---------------------------------------------------------------------------
FROM golang:${GO_VERSION}-alpine AS builder

WORKDIR /src

# Copy the dependency manifests first so editing source does not invalidate
# the module-download layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Overwrite whatever bundle is in the build context with the freshly built one.
COPY --from=web /web/dist ./cmd/shhgit/web/dist

# Static binary - no cgo, no libc, runs on a bare alpine runtime.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/shhgit ./cmd/shhgit

# ---------------------------------------------------------------------------
FROM alpine:3.20 AS runtime

# git              shhgit clones repositories to scan them
# ca-certificates  HTTPS to the GitHub API, webhooks and LLM providers
# tzdata           readable timestamps in logs
RUN apk add --no-cache git ca-certificates tzdata \
    && adduser -D -u 10001 -h /app shhgit

WORKDIR /app

COPY --from=builder /out/shhgit /usr/local/bin/shhgit
COPY --from=builder /src/config.yaml.example /app/config.yaml.example

RUN mkdir -p /app/logs /app/cache && chown -R shhgit:shhgit /app

USER shhgit

# The dashboard has NO authentication. Binding 0.0.0.0 here only makes it
# reachable from outside the container; keep the published port on 127.0.0.1
# (docker-compose does) and never expose it to the internet directly.
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/health >/dev/null 2>&1 || exit 1

ENTRYPOINT ["shhgit"]
CMD ["--web", "--web-host", "0.0.0.0", "--web-port", "8080", "--config-path", "/app"]
