# Build stage
FROM golang:1.23-alpine AS builder

WORKDIR /build

# Install git (needed for go mod download)
RUN apk add --no-cache git

# Copy go mod files first for caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build binary
RUN CGO_ENABLED=0 GOOS=linux go build -o advo_cache .

# Runtime stage
FROM alpine:latest

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/advo_cache .

# Expose WebSocket port
EXPOSE 8081

# Run the binary
CMD ["./advo_cache"]
