# syntax=docker/dockerfile:1
# CertiFire: Go backend + React/Vite frontend embedded in the binary. Multi-arch via buildx.
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
COPY --from=web /web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/app

# Alpine (not distroless): the app runs the openssl CLI to generate certificates.
FROM alpine:3.24
# hadolint ignore=DL3018
RUN apk add --no-cache openssl \
    && rm -f /usr/bin/wget \
    && adduser -D -H -u 10001 -s /sbin/nologin app
COPY --from=build /out/app /app
ENV PORT=8080
EXPOSE 8080
USER 10001:10001
# `app healthcheck` GETs http://127.0.0.1:$PORT/healthz and exits non-zero on failure
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 CMD ["/app", "healthcheck"]
ENTRYPOINT ["/app"]
