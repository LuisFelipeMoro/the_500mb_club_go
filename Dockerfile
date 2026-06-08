# syntax=docker/dockerfile:1

# Stage 1 — build (runs natively on the build host; cross-compiles; no QEMU).
# TARGETOS/TARGETARCH are populated by buildx from --platform, so the same
# Dockerfile produces arm64 (submission) or amd64 (local native stress testing).
ARG BUILDPLATFORM
FROM --platform=${BUILDPLATFORM:-linux/amd64} golang:1.26-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /app
# Dependency layer: only invalidated when go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-arm64} \
    go build -trimpath -ldflags="-s -w" -o /api ./cmd/api

# Stage 2 — runtime (scratch: no libc, no shell, zero overhead). buildx sets the
# runtime platform from --platform.
FROM scratch
COPY --from=builder /api /api
USER 10000:10000
EXPOSE 3000
ENTRYPOINT ["/api"]
