# syntax=docker/dockerfile:1
# Stage 1: Build binaries
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=0.0.0
ARG CHANNEL=dev

WORKDIR /app

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY . .

# Build main binary
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
    -ldflags="-w -s -X github.com/sirrobot01/decypharr/pkg/version.Version=${VERSION} -X github.com/sirrobot01/decypharr/pkg/version.Channel=${CHANNEL}" \
    -o /decypharr

# Build healthcheck (optimized)
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-w -s" \
    -o /healthcheck cmd/healthcheck/main.go

# Stage 2: Final image
FROM alpine:latest

ARG VERSION=0.0.0
ARG CHANNEL=dev

LABEL version="${VERSION}-${CHANNEL}" \
      org.opencontainers.image.source="https://github.com/sirrobot01/decypharr" \
      org.opencontainers.image.title="decypharr" \
      org.opencontainers.image.authors="sirrobot01" \
      org.opencontainers.image.documentation="https://github.com/sirrobot01/decypharr/blob/main/README.md"

# Install dependencies including rclone
RUN apk add --no-cache fuse3 ca-certificates su-exec shadow curl unzip && \
    echo "user_allow_other" >> /etc/fuse.conf && \
    case "$(uname -m)" in \
        x86_64) ARCH=amd64 ;; \
        aarch64) ARCH=arm64 ;; \
        armv7l) ARCH=arm ;; \
        *) echo "Unsupported architecture: $(uname -m)" && exit 1 ;; \
    esac && \
    curl -O "https://downloads.rclone.org/rclone-current-linux-${ARCH}.zip" && \
    unzip "rclone-current-linux-${ARCH}.zip" && \
    cp rclone-*/rclone /usr/local/bin/ && \
    chmod +x /usr/local/bin/rclone && \
    rm -rf rclone-* && \
    apk del curl unzip

# Copy binaries and entrypoint
COPY --from=builder /decypharr /usr/bin/decypharr
COPY --from=builder /healthcheck /usr/bin/healthcheck
COPY scripts/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Set environment variables
ENV PUID=1000
ENV PGID=1000
ENV LOG_PATH=/app/logs

EXPOSE 8282
VOLUME ["/app"]

HEALTHCHECK --interval=10s --retries=10 CMD ["/usr/bin/healthcheck", "--config", "/app", "--basic"]

ENTRYPOINT ["/entrypoint.sh"]
CMD ["/usr/bin/decypharr", "--config", "/app"]