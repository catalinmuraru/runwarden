# Ward base image: the server with the UI embedded, on Debian slim so scripts get bash and
# coreutils. Scripts and the tools they need are added by derived images (see examples/Dockerfile).
#
#   docker build --build-arg VERSION=v0.1.0 -t ward .

FROM --platform=$BUILDPLATFORM node:26.13.1-trixie-slim@sha256:abe7e5a82b61a8ef67b315b85497c9e65173387ccf2f4a029248184b8e020be5 AS ui
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-fund --no-audit
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.27.4-trixie@sha256:430985542100b871e108ccc00e8dcc5eef08ca14ee5825e98ebf563d8ffec4ee AS build
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
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/ward ./cmd/ward

FROM debian:trixie-slim@sha256:62bd423aeee912639ca8ee681c2b762cb3e25137e0a92b20be455898a9f034ad AS runtime
ARG VERSION=dev
LABEL org.opencontainers.image.title="ward" \
      org.opencontainers.image.description="Web UI to run scripts with parameters, live output, history and access control" \
      org.opencontainers.image.source="https://github.com/catalinmuraru/ward" \
      org.opencontainers.image.version="${VERSION}"

# tini runs as PID 1 so processes orphaned by scripts are reaped.
# upgrade pulls Debian security fixes released after the pinned base digest.
RUN apt-get update \
 && apt-get upgrade -y --no-install-recommends \
 && apt-get install -y --no-install-recommends bash ca-certificates tini \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --gid 10001 ward \
 && useradd --uid 10001 --gid 10001 --home-dir /home/ward --create-home --shell /bin/bash ward \
 && mkdir -p /ward/runners /opt/ward /var/lib/ward \
 && chown ward:ward /var/lib/ward

COPY --from=build /out/ward /usr/local/bin/ward

ENV RUNNERS_DIR=/ward/runners \
    BASE_DIR=/opt/ward \
    DATA_DIR=/var/lib/ward \
    LISTEN_ADDR=:8080 \
    HEALTH_ADDR=:8081 \
    HOME=/home/ward
WORKDIR /opt/ward
USER 10001:10001
EXPOSE 8080 8081
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/ward"]
