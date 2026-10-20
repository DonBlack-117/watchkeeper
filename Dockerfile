# syntax=docker/dockerfile:1

# 1. Frontend. It runs on the build machine's platform: the output is static files.
FROM --platform=$BUILDPLATFORM docker.io/library/node:26-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
# Writes to /src/internal/ui/dist/app, where the Go build embeds it.
RUN npm run build

# 2. Go binary, cross-compiled for the target platform (amd64 or arm64).
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/ui/dist/app ./internal/ui/dist/app
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/watchkeeper ./cmd/watchkeeper

# 3. Runtime: no shell, no package manager, non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/watchkeeper /watchkeeper
ENV WK_ADDR=0.0.0.0:8090
EXPOSE 8090
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s CMD ["/watchkeeper", "healthcheck"]
ENTRYPOINT ["/watchkeeper"]
