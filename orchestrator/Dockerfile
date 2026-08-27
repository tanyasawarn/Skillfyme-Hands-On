# Environment Orchestrator (Go) -- PHASE1_MVP_COMPLETION.md §1.1: a real
# image so `docker compose --profile app up` is a production-like bring-up
# instead of infra-only. Multi-stage: a full Go toolchain builds a static
# binary, the runtime stage ships only that binary on a distroless base.
#
# Build context is orchestrator/ (see docker-compose.yml's build.context).
# contracts/ is NOT needed inside this image -- the Go side vendors its
# generated stubs under pkg/pb/; only practice-core loads contracts/*.proto
# at runtime.

# --- build stage ---
FROM golang:1.26 AS build
WORKDIR /src

# Cache module downloads separately from the source tree.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Static, stripped binary. CGO off so it runs on the distroless base.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' \
    -o /out/orchestrator ./cmd/orchestrator

# --- runtime stage ---
# distroless static: no shell, no package manager, just libc-less runtime.
# The critical K8s manifests the binary applies at runtime are go:embed'd
# (internal/manifests, internal/clusterbootstrap); the top-level manifests/
# tree is kubectl-applied-by-hand and not read by this process, so it is
# deliberately not copied in.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app

COPY --from=build /out/orchestrator /app/orchestrator

# gRPC (50051), WS gateway (8081), Prometheus metrics + healthz (9090).
EXPOSE 50051 8081 9090
USER nonroot:nonroot
ENTRYPOINT ["/app/orchestrator"]
