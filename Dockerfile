# Build stage. CGO is disabled because modernc.org/sqlite is pure Go, which
# keeps the final image free of libc entirely.
FROM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/gateway ./cmd/gateway

# Runtime stage. distroless/static ships CA certificates and tzdata-free
# minimal userland; the gateway needs no shell and makes no outbound calls.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/gateway /usr/local/bin/gateway

# /data holds netprobe.db and must be a volume.
VOLUME ["/data"]
ENV DATA_DIR=/data

EXPOSE 8080 9090

USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/gateway"]
