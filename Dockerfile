# Runwarden base image: the server with the UI embedded, on Debian slim so scripts get bash and
# coreutils. Scripts and the tools they need are added by derived images (see examples/Dockerfile).
#
#   docker build --build-arg VERSION=v0.1.0 -t runwarden .

FROM --platform=$BUILDPLATFORM node:26.12.1-trixie-slim@sha256:94816674c62774da0d099a947bf4613b9e1972a2733abd0d0cd80e795f8411d0 AS ui
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-fund --no-audit
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.27.3-trixie@sha256:fbb7ae40b62ff8afe31662df570d39b4f5052ac32b90dae8e60d7657a20a74f4 AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY --from=ui /src/internal/web/dist ./internal/web/dist
# Go cross-compiles, so multi-arch builds don't need emulation for this stage.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/runwarden ./cmd/runwarden

FROM debian:trixie-slim@sha256:62bd423aeee912639ca8ee681c2b762cb3e25137e0a92b20be455898a9f034ad
ARG VERSION=dev
LABEL org.opencontainers.image.title="runwarden" \
      org.opencontainers.image.description="Web UI to run scripts with parameters, live output, history and access control" \
      org.opencontainers.image.source="https://github.com/catalinmuraru/runwarden" \
      org.opencontainers.image.version="${VERSION}"

# tini runs as PID 1 so processes orphaned by scripts are reaped.
RUN apt-get update \
 && apt-get install -y --no-install-recommends bash ca-certificates tini \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --gid 10001 runwarden \
 && useradd --uid 10001 --gid 10001 --home-dir /home/runwarden --create-home --shell /bin/bash runwarden \
 && mkdir -p /runwarden/runners /opt/runwarden /var/lib/runwarden \
 && chown runwarden:runwarden /var/lib/runwarden

COPY --from=build /out/runwarden /usr/local/bin/runwarden

ENV RUNNERS_DIR=/runwarden/runners \
    BASE_DIR=/opt/runwarden \
    DATA_DIR=/var/lib/runwarden \
    LISTEN_ADDR=:8080 \
    HEALTH_ADDR=:8081 \
    HOME=/home/runwarden
WORKDIR /opt/runwarden
USER 10001:10001
EXPOSE 8080 8081
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/runwarden"]
