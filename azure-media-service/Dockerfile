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
# Distroless nonroot image: no shell, no package manager, minimal attack
# surface — appropriate for a stateless network service handling tokens.
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=build /out/azure-media-service /app/azure-media-service

USER nonroot:nonroot

EXPOSE 8080

ENTRYPOINT ["/app/azure-media-service"]
