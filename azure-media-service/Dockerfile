# syntax=docker/dockerfile:1

# ---- Build stage ----
FROM golang:1.25-alpine AS build

WORKDIR /src

# Cache module downloads separately from source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/azure-media-service .

# ---- Runtime stage ----
# Uses alpine rather than distroless so the render endpoint has access to
# ffmpeg. The /tmp filesystem is used as a transient workspace for
# downloading source clips and writing the render output before uploading it
# to OneDrive.
FROM alpine:3.21

RUN addgroup -S appgroup && adduser -S appuser -G appgroup && \
    apk add --no-cache ffmpeg

WORKDIR /app
COPY --from=build /out/azure-media-service /app/azure-media-service

USER appuser:appgroup

EXPOSE 8080

ENTRYPOINT ["/app/azure-media-service"]
