# syntax=docker/dockerfile:1
#
# cexd container image (week 7). Two stages:
#   build  : the full Go toolchain compiles a static binary
#   runtime: distroless "static" (no shell, no package manager, ~2 MB) plus
#            that one binary, running as an unprivileged user
#
# Build from the repository root (Compose does this):
#   docker build -f deploy/Dockerfile --build-arg VERSION=$(git describe --always) -t cex/cexd .

# Same Go version as go.mod, so the image is built exactly like CI builds.
FROM golang:1.26.10-alpine AS build
WORKDIR /src

# Dependencies first, in their own layer: they change rarely, so a code-only
# change rebuilds from here in seconds. The cache mount keeps downloaded
# modules between builds without putting them in any image layer.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG VERSION=dev
# CGO_ENABLED=0: a fully static binary that needs no libc, so it runs on
# distroless/static. -trimpath drops local file paths from the binary;
# -s -w drop symbol and debug tables (smaller; pprof still symbolizes Go
# functions from the runtime's own tables).
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/cexd ./cmd/cexd \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/cexd /cexd
# /data must exist and belong to the runtime user (65532 = "nonroot"):
# Docker copies this ownership into a new named volume mounted here.
COPY --from=build --chown=65532:65532 /out/data /data

# Inside the container both listeners bind every interface; what is reachable
# from OUTSIDE is decided by the ports the operator publishes (Compose
# publishes the admin port on the host's loopback only).
ENV CEX_HTTP_ADDR=:8080 \
    CEX_ADMIN_ADDR=:6060 \
    CEX_DATA_DIR=/data

USER 65532:65532
EXPOSE 8080 6060
VOLUME ["/data"]

# No curl in distroless: the binary checks its own /readyz.
# start-period covers journal replay after a restart.
HEALTHCHECK --interval=5s --timeout=3s --start-period=60s --retries=3 CMD ["/cexd", "healthcheck"]

# SIGTERM (docker stop) triggers cexd's graceful shutdown: stop HTTP, drain
# the engine, close the journal. Exec form: cexd is PID 1 and gets the signal.
ENTRYPOINT ["/cexd"]
