# syntax=docker/dockerfile:1.4
# ==============================================================================
# Stage 1: Hermetic Go Builder
# ==============================================================================
FROM golang:1.27-alpine AS builder

# Install build prerequisites
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /workspace

# Copy Go module manifests from all local workspace modules for layer caching
COPY --from=core go.mod go.sum ./core/
COPY --from=resumeee-storage-sql go.mod go.sum ./resumeee-storage-sql/
COPY --from=resumeee-auth-firebase go.mod go.sum ./resumeee-auth-firebase/
COPY go.mod go.sum ./server/

# Point inter-module dependencies to local workspace paths before downloading public modules
RUN go -C ./resumeee-storage-sql mod edit \
        -replace github.com/resumeee-app/core=../core && \
    go -C ./resumeee-auth-firebase mod edit \
        -replace github.com/resumeee-app/core=../core && \
    go -C ./server mod edit \
        -replace github.com/resumeee-app/core=../core \
        -replace github.com/resumeee-app/resumeee-storage-sql=../resumeee-storage-sql \
        -replace github.com/resumeee-app/resumeee-auth-firebase=../resumeee-auth-firebase && \
    go -C ./server mod download

# Copy application and module source trees
COPY --from=core . ./core/
COPY --from=resumeee-storage-sql . ./resumeee-storage-sql/
COPY --from=resumeee-auth-firebase . ./resumeee-auth-firebase/
COPY . ./server/

# Re-apply local module replacements over copied go.mod files
RUN go -C ./resumeee-storage-sql mod edit \
        -replace github.com/resumeee-app/core=../core && \
    go -C ./resumeee-auth-firebase mod edit \
        -replace github.com/resumeee-app/core=../core && \
    go -C ./server mod edit \
        -replace github.com/resumeee-app/core=../core \
        -replace github.com/resumeee-app/resumeee-storage-sql=../resumeee-storage-sql \
        -replace github.com/resumeee-app/resumeee-auth-firebase=../resumeee-auth-firebase

WORKDIR /workspace/server

# Build fully static pure-Go binaries (CGO_ENABLED=0)
# -trimpath strips host paths from stack traces
# -ldflags="-s -w -extldflags '-static'" strips debug tables
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -extldflags '-static'" \
    -o /bin/resumeee-server \
    ./cmd/server && \
    CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -extldflags '-static'" \
    -o /bin/resumeee-mcp \
    ./cmd/mcp

# Provide symlink /bin/resumeee -> /bin/resumeee-server for backward compatibility
RUN ln -s /bin/resumeee-server /bin/resumeee

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
COPY --from=builder /bin/resumeee /usr/local/bin/resumeee

# Copy entrypoint router script
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

# 12-Factor default environment configuration
ENV PORT=8080 \
    DB_PATH=/data/resumeee.db \
    DEFAULT_USER_ID=default \
    WORKER_CONCURRENCY=2 \
    ENV=production \
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
