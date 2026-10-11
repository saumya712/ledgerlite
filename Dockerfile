# syntax=docker/dockerfile:1

# ---- build stage: full Go toolchain ----
# Keep this version equal to the "go" line in your go.mod (or newer).
ARG GO_VERSION=1.26
FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src

# Copy only the module files first, so the dependency download is cached
# until go.mod or go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ledgerlite ./cmd/server

# ---- run stage: no shell, no package manager, runs as a non-root user ----
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ledgerlite /ledgerlite
EXPOSE 8080
ENTRYPOINT ["/ledgerlite"]
