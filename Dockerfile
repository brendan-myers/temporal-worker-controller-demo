# Builds one worker image per demo workflow version.
#
#   docker build --build-arg WORKER_VERSION=v2 -t order-worker:v2 .
#
# The version selects a build tag (see cmd/worker/version_*.go), so the binary
# contains only that version's workflow code. DEMO_WORKER_VERSION is carried
# into the image purely so activity results can name themselves in the UI.
#
# The module and compile caches are BuildKit cache mounts shared across builds.
# Without them each version recompiles every dependency from scratch, which turns
# `make images` into several minutes of repeated work.
ARG WORKER_VERSION=v1

FROM golang:1.25 AS builder
ARG WORKER_VERSION

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build -tags "demo_${WORKER_VERSION}" -o /out/worker ./cmd/worker

FROM gcr.io/distroless/static-debian12
ARG WORKER_VERSION

ENV DEMO_WORKER_VERSION=${WORKER_VERSION}
COPY --from=builder /out/worker /worker

USER 65532:65532
ENTRYPOINT ["/worker"]
