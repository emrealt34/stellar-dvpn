# syntax=docker/dockerfile:1
# dmesh coordinator — non-custodial Soroban settlement backend.
# Pure-Go build (modernc.org/sqlite, no cgo) so the runtime image is static.
FROM golang:1.27-alpine AS build
WORKDIR /src

# Cache module downloads independently of source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w" -o /out/dmeshd ./cmd/dmeshd
# Empty data dir staged so it can be copied with non-root ownership into the
# distroless image, which has no shell to create it at runtime.
RUN mkdir -p /out/data

# Minimal static runtime: no shell, runs as a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --chown=65532:65532 --from=build /out/dmeshd /app/dmeshd
COPY --chown=65532:65532 --from=build /out/data /app/data

ENV DB_PATH=/app/data/dmesh.db \
    GEO_COUNTRY_DB=/app/data/GeoLite2-Country.mmdb \
    GEO_ASN_DB=/app/data/GeoLite2-ASN.mmdb \
    APP_ADDR=:8080

EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/app/dmeshd"]
