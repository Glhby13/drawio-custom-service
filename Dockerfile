# syntax=docker/dockerfile:1.7
# =============================================================================
# drawio-custom-service: static Go binaries on scratch, non-root (uid/gid 65532).
# One Dockerfile, one target per deployable via SERVICE_NAME (ADR-0005, ADR-0012):
#   docker buildx build --platform linux/arm64,linux/amd64 --build-arg SERVICE_NAME=gateway .
# CGO_ENABLED=0, no native libraries. CA bundle is the image's; internal CAs are mounted at runtime
# (EXTRA_CA_FILE), never baked in and never bypassed with InsecureSkipVerify.
# =============================================================================
ARG GO_IMAGE=golang:1.27.3-alpine

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS go-build
ARG TARGETOS=linux
ARG TARGETARCH
ARG SERVICE_NAME=gateway
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -buildid=" -o /out/app ./cmd/${SERVICE_NAME}
RUN echo "app:x:65532:65532:app:/nonexistent:/sbin/nologin" > /out/passwd && \
    echo "app:x:65532:" > /out/group

FROM scratch
ARG SERVICE_NAME=gateway
LABEL org.opencontainers.image.title="drawio-${SERVICE_NAME}"
COPY --from=go-build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=go-build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=go-build /out/passwd /etc/passwd
COPY --from=go-build /out/group /etc/group
COPY --from=go-build /out/app /app
ENV PORT=8080 TZ=Asia/Jakarta
USER 65532:65532
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=4s --start-period=10s --retries=3 CMD ["/app", "healthcheck"]
ENTRYPOINT ["/app"]
