# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
COPY azure-video-indexer-service/go.mod azure-video-indexer-service/go.sum ./azure-video-indexer-service/

RUN cd azure-video-indexer-service && go mod download

COPY internal ./internal
COPY azure-video-indexer-service ./azure-video-indexer-service

WORKDIR /src/azure-video-indexer-service

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/azure-video-indexer-service .

FROM alpine:3.21

RUN addgroup -S appgroup && adduser -S appuser -G appgroup && \
    apk add --no-cache ffmpeg ca-certificates wget

WORKDIR /app
COPY --from=build /out/azure-video-indexer-service /app/azure-video-indexer-service

USER appuser:appgroup

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s CMD wget -qO- http://127.0.0.1:8080/health >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/app/azure-video-indexer-service"]
