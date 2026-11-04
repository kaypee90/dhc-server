# syntax=docker/dockerfile:1

# ---- Build stage ----
FROM golang:1.26-alpine AS build
WORKDIR /src

# Download modules first so this layer is cached between code changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
# The SQLite driver is pure Go, so the binary is fully static.
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.Version=${VERSION}" -o /out/dhc-server . \
    && mkdir -p /out/data

# ---- Runtime stage ----
# Distroless: no shell or package manager, runs as an unprivileged user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/dhc-server /dhc-server
COPY --from=build --chown=nonroot:nonroot /out/data /data

ENV DHC_DATABASE_PATH=/data/dhc.db \
    GIN_MODE=release
VOLUME ["/data"]
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/dhc-server"]
