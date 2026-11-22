# mono-agent CLI — minimal production image.
#
# This builds the DEFAULT binary, including social platform nodes.
# Build with -tags nosocial to leave those nodes out; never use devaccount
# in a production image. A monoes.me sign-in is required since 2026-10-11 (UTC); sign in inside the container.
#
# Stage 1: build (static, CGO-free → runs on any base image, incl. scratch)
FROM golang:1.26-alpine AS build

WORKDIR /src

# Cache module downloads separately from source changes.
COPY go.mod go.sum ./
RUN go mod download

# Everything else. NOTE: data/ (migrations, actions, skills) and
# internal/workflow/{schemas,templates} are go:embed-ed — they must stay in
# the build context (see .dockerignore).
COPY . .

# Stamp the version and build date as release.yml does; without them the image reports "dev",
# skips the downgrade guard, and has no build-date floor for the sign-in gate.
ARG VERSION=dev
ARG BUILD_DATE=
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-X main.version=${VERSION} -X main.buildDate=${BUILD_DATE}" -o /out/monoagentcli ./cmd/monoagentcli

# Stage 2: runtime
FROM alpine:3.22

# TLS roots for outbound HTTPS (API nodes, RSS, webhooks, release updates).
RUN apk add --no-cache ca-certificates

COPY --from=build /out/monoagentcli /usr/local/bin/monoagentcli

# All state lives under $HOME/.monoagent (SQLite DB at
# ~/.monoagent/monoagent.db, workflows at ~/.monoagent/workflows, secrets
# vault at ~/.monoagent/vault — resolved via os.UserHomeDir/$HOME in
# cmd/monoagentcli/root.go and internal/vault/vault.go). Pointing HOME at
# /data puts ALL of it on one volume.
ENV HOME=/data
# Non-root runtime user; /data is chowned so the volume is writable by it.
RUN adduser -D -u 10001 monoagent \
    && mkdir -p /data \
    && chown monoagent:monoagent /data
USER monoagent
VOLUME ["/data"]

WORKDIR /app

# Liveness = the CLI answers `version` (exit 0). The daemon is the
# entrypoint, so any successful subcommand invocation proves the process
# tree is alive and the binary still executes. This does not test account
# readiness: a locked daemon in a future enforced build stays up. Make the
# monoes.me sign-in after `up` (README.md "Docker" and docker-compose.yml say how)
# and keep it in /data/.monoagent/account/ only, signing in separately per machine
# and never baking or restoring a machine sign-in into an image. Check it with
# `docker exec <container> monoagentcli account status --offline`.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD monoagentcli version

# Webhook trigger port. The engine binds 127.0.0.1:9321 by default
# (internal/workflow/engine.go applyEngineDefaults); set
# MONOAGENT_WEBHOOK_ADDR=0.0.0.0:9321 (see docker-compose.yml) so the
# published port is reachable from outside the container. Secure webhook
# nodes (auth token / HMAC secret) before exposing the port further.
EXPOSE 9321

ENTRYPOINT ["/usr/local/bin/monoagentcli", "daemon"]
