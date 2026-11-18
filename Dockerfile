# syntax=docker/dockerfile:1
# ==============================================================================
# Stage 1: Hermetic Go Builder
# ==============================================================================
FROM golang:1.27-alpine AS builder

# Install build prerequisites (gcc and musl-dev are required for CGO / mattn/go-sqlite3)
RUN apk add --no-cache ca-certificates tzdata gcc musl-dev

WORKDIR /workspace

# Copy Go module manifests for layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy application source tree
COPY . .

# Build static CGO binaries (required by mattn/go-sqlite3)
# -trimpath strips host paths from stack traces
# -ldflags="-s -w -extldflags '-static'" strips debug tables and links statically against musl
RUN CGO_ENABLED=1 go build \
    -trimpath \
    -ldflags="-s -w -extldflags '-static'" \
    -o /bin/resumeee-server \
    ./cmd/server && \
    CGO_ENABLED=1 go build \
    -trimpath \
    -ldflags="-s -w -extldflags '-static'" \
    -o /bin/resumeee-mcp \
    ./cmd/mcp

# ==============================================================================
# Stage 2: Minimal Rootless Runtime (Alpine Base)
# ==============================================================================
FROM alpine:3.21 AS runtime

# Install CA certificates, tzdata, curl for healthchecks, and mailcap for MIME types
RUN apk --no-cache add ca-certificates tzdata curl mailcap && \
    addgroup -g 10001 -S appgroup && \
    adduser -u 10001 -S appuser -G appgroup && \
    mkdir -p /data /home/appuser && \
    chown -R appuser:appgroup /data /home/appuser

WORKDIR /home/appuser

# Copy static binaries from builder
COPY --from=builder /bin/resumeee-server /usr/local/bin/resumeee-server
COPY --from=builder /bin/resumeee-mcp /usr/local/bin/resumeee-mcp

# Copy entrypoint router script
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

# Container environment defaults
ENV ENV=production \
    LOG_LEVEL=INFO

# Drop root privileges immediately
USER 10001:10001

# Expose unified server port (8080)
EXPOSE 8080

# Persistent SQLite database volume
VOLUME ["/data"]

# Healthcheck monitoring unified server
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:${PORT:-8080}/api/v1/health || exit 1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["server"]
