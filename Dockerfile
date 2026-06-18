# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /build

# Install git for version info
RUN apk add --no-cache git

# Copy go mod files first for caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Build with version info
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${BUILD_DATE}" \
    -o cascade \
    ./cmd/cascade

# Runtime stage - minimal image with git
FROM alpine:3.19

RUN apk add --no-cache git ca-certificates

COPY --from=builder /build/cascade /usr/local/bin/cascade

WORKDIR /work

ENTRYPOINT ["/usr/local/bin/cascade"]
