# syntax=docker/dockerfile:1

ARG GO_VERSION=1.27.3
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot

FROM --platform=$BUILDPLATFORM docker.io/library/golang:${GO_VERSION}-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/

ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
ARG BUILD_DATE=unknown
# modernc SQLite is pure Go: the runtime needs no libc, compiler or shell.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X main.Version=${VERSION} -X main.BuildTime=${BUILD_DATE}" \
    -o /out/argus ./cmd/argus \
    && mkdir -p /out/data

FROM ${RUNTIME_IMAGE}
ARG VERSION=dev
LABEL org.opencontainers.image.title="argus" \
      org.opencontainers.image.description="NetFlow collection and local enrichment" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="MIT"

COPY --from=build /out/argus /usr/local/bin/argus
COPY --from=build --chown=65532:65532 /out/data/ /data/
COPY etc/container.yaml.example /etc/argus/config.yaml
COPY LICENSE /usr/share/licenses/argus/LICENSE

# All relative SQLite files and logs go to the one writable state mount.
WORKDIR /data
ENV TMPDIR=/tmp
USER 65532:65532
EXPOSE 9600/tcp 2055/udp
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/argus"]
CMD ["-c", "/etc/argus/config.yaml"]
